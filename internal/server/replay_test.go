package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// replica is one console sharing st with the others.
func replica(t *testing.T, st *storetest.Memory) *Server {
	t.Helper()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", SigningToken: testSigningToken}}
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "s3cret", "t", "http://127.0.0.1:1"
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	var deps Deps
	deps.License = licensetest.Full(t)
	if st != nil {
		deps.Store = st
	}
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.queue.Shutdown(context.Background()) })
	return s
}

func signedGitLab(s *Server, id string, body []byte, ts int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", hookPath("gitlab", 1), bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	for k, v := range signedHeaders(testSigningKey, id, ts, body) {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

// TestReplayAcrossReplicasAndRestarts: a captured delivery is processed
// once, whichever replica receives the replay and after a restart.
func TestReplayAcrossReplicasAndRestarts(t *testing.T) {
	st := storetest.New()
	a, b := replica(t, st), replica(t, st)
	body := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":5,"action":"approved"}}`)
	now := time.Now().Unix()
	if w := signedGitLab(a, "d1", body, now); strings.Contains(w.Body.String(), "duplicate") || w.Code != http.StatusOK {
		t.Fatalf("first delivery: %d %s", w.Code, w.Body)
	}
	if w := signedGitLab(b, "d1", body, now); !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("the same delivery on another replica is a replay: %d %s", w.Code, w.Body)
	}
	// A new webhook-id doesn't help: the body was already received.
	if w := signedGitLab(b, "d2", body, now); !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("a re-signed copy of the same event is a duplicate: %d %s", w.Code, w.Body)
	}
	restarted := replica(t, st)
	if w := signedGitLab(restarted, "d1", body, now); !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("after a restart the replay is still caught: %d %s", w.Code, w.Body)
	}

	// GitHub: X-GitHub-Delivery isn't signed, so changing it doesn't make a
	// replay new.
	gh := []byte(`{"action":"opened","number":7,"repository":{"full_name":"o/r","html_url":"https://github.com/o/r"}}`)
	send := func(s *Server, delivery string) string {
		req := httptest.NewRequest("POST", hookPath("github", 1), bytes.NewReader(gh))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", delivery)
		req.Header.Set("X-Hub-Signature-256", sign("s3cret", gh))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Body.String()
	}
	if got := send(a, "g1"); strings.Contains(got, "duplicate") {
		t.Fatalf("first github delivery: %s", got)
	}
	if got := send(b, "g2"); !strings.Contains(got, "duplicate") {
		t.Errorf("a github replay with a new delivery id on another replica: %s", got)
	}
}

// Without a database, replays to the same console are still caught.
func TestReplayInMemoryWithoutDatabase(t *testing.T) {
	s := replica(t, nil)
	body := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":5,"action":"approved"}}`)
	now := time.Now().Unix()
	_ = signedGitLab(s, "m1", body, now)
	if w := signedGitLab(s, "m1", body, now); !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("in-memory replay protection: %s", w.Body)
	}
}

func TestDeliveriesExpire(t *testing.T) {
	var d deliveries
	t0 := time.Now()
	if !d.first("k", time.Minute, t0) || d.first("k", time.Minute, t0.Add(30*time.Second)) {
		t.Error("seen within ttl")
	}
	if !d.first("k", time.Minute, t0.Add(2*time.Minute)) {
		t.Error("expired keys are new again")
	}
}
