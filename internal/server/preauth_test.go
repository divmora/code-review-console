package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// unreadable is a request body that fails the test if it is read.
type unreadable struct{ t *testing.T }

func (u unreadable) Read([]byte) (int, error) {
	u.t.Error("the body must not be read before the delivery is authenticated")
	return 0, errors.New("read")
}

// preauthServer has two GitLab hosts (token auth), a GitHub and a
// Bitbucket host, and records webhook history.
func preauthServer(t *testing.T) (*Server, *storetest.Memory) {
	t.Helper()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{
		{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "secret-a"},
		{BaseURL: "http://127.0.0.2:1", Token: "t", WebhookSecret: "secret-b"},
	}
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "gh-secret", "t", "http://127.0.0.1:1"
	cfg.Bitbucket = []config.Bitbucket{{APIURL: "http://127.0.0.3:1", Token: "t", WebhookSecret: "bb-secret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	st := storetest.New()
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.queue.Shutdown(context.Background()) })
	return s, st
}

func history(t *testing.T, s *Server, st *storetest.Memory) []store.WebhookEvent {
	t.Helper()
	s.hooks.close()
	ev, _ := st.ListWebhookEvents(context.Background(), store.WebhookFilter{Limit: 1000})
	return ev
}

const forged = `{"object_kind":"merge_request","user":{"username":"ceo"},"project":{"path_with_namespace":"acme/payroll","web_url":"http://127.0.0.1:1/acme/payroll"},"repository":{"full_name":"acme/payroll","html_url":"https://github.com/acme/payroll"},"object_attributes":{"iid":9,"action":"open"}}`

func TestRejectedDeliveriesRecordNoPayload(t *testing.T) {
	s, st := preauthServer(t)
	h := s.Handler()
	do := func(path string, hdr map[string]string, body []byte) int {
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	b := []byte(forged)
	codes := []int{
		do("/webhooks/gitlab", map[string]string{"X-Gitlab-Event": "Merge Request Hook", "X-Gitlab-Token": "wrong"}, b),
		do("/webhooks/gitlab", map[string]string{"X-Gitlab-Event": "Merge Request Hook"}, b),
		do("/webhooks/github", map[string]string{"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": sign("wrong", b)}, b),
		do("/webhooks/bitbucket", map[string]string{"X-Event-Key": "pullrequest:created", "X-Hub-Signature": bbSign("wrong", b)}, b),
	}
	for i, c := range codes {
		if c != http.StatusUnauthorized {
			t.Errorf("delivery %d: %d, want 401", i, c)
		}
	}
	ev := history(t, s, st)
	if len(ev) != 4 {
		t.Fatalf("want 4 recorded, got %d", len(ev))
	}
	for _, e := range ev {
		if e.Project != "" || e.Number != 0 || e.Sender != "" || e.Kind != "" || e.Action != "" || e.Auth != "failed" {
			t.Errorf("a rejected delivery carries payload fields: %+v", e)
		}
	}
}

func TestBodyNotReadBeforeAuth(t *testing.T) {
	s, _ := preauthServer(t)
	h := s.Handler()
	for name, hdr := range map[string]map[string]string{
		"gitlab wrong token": {"X-Gitlab-Token": "wrong"},
		"gitlab no auth":     {},
		"github unsigned":    {},
		"bitbucket unsigned": {},
	} {
		path := "/webhooks/gitlab"
		switch name[:6] {
		case "github":
			path = "/webhooks/github"
		case "bitbuc":
			path = "/webhooks/bitbucket"
		}
		req := httptest.NewRequest("POST", path, unreadable{t})
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

// Without X-Gitlab-Instance and with several GitLab hosts, the token picks
// the host (the payload is not used to choose).
func TestGitLabTokenPicksHost(t *testing.T) {
	s, st := preauthServer(t)
	body := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"g/p","web_url":"http://127.0.0.1:1/g/p"},"object_attributes":{"iid":3,"action":"approved"}}`)
	req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	req.Header.Set("X-Gitlab-Token", "secret-b")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ev := history(t, s, st)
	if len(ev) != 1 || ev[0].Host != "127.0.0.2:1" || ev[0].Auth != "token" || ev[0].Project != "g/p" {
		t.Errorf("the second host's token must select it, whatever the payload says: %+v", ev)
	}
}

func TestRejectedDeliveriesAreCapped(t *testing.T) {
	s, st := preauthServer(t)
	h := s.Handler()
	for i := 0; i < 3*maxRejectedPerMinute; i++ {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(nil))
		req.Header.Set("X-Gitlab-Token", "wrong")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	// At most two minute windows are touched, even across a boundary.
	if n := len(history(t, s, st)); n > 2*maxRejectedPerMinute || n < maxRejectedPerMinute {
		t.Errorf("recorded %d of %d rejected deliveries", n, 3*maxRejectedPerMinute)
	}
}

func TestRejectLimiterWindows(t *testing.T) {
	var l rejectLimiter
	t0 := time.Date(2026, 11, 22, 10, 0, 5, 0, time.UTC)
	ok := 0
	for i := 0; i < maxRejectedPerMinute+10; i++ {
		if l.allow(t0.Add(time.Duration(i)*time.Millisecond), nil) {
			ok++
		}
	}
	if ok != maxRejectedPerMinute || l.skipped != 10 {
		t.Errorf("first minute: %d allowed, %d skipped", ok, l.skipped)
	}
	if !l.allow(t0.Add(time.Minute), nil) || l.skipped != 0 {
		t.Error("a new minute starts a new window")
	}
}
