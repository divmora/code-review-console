package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// gateServer has a GitLab host whose API knows dev (Developer) and
// visitor (Guest), from fakeGitLabAPI.
func gateServer(t *testing.T, access string) (*Server, *fakeGitLabAPI, func(id int, user, note string) *httptest.ResponseRecorder) {
	t.Helper()
	api := &fakeGitLabAPI{starter: "gitlab-bot"}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: srv.URL, Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	st := storetest.New()
	if access != "" {
		_ = st.PutSetting(context.Background(), settings.Key, settings.Stored{CommandAccess: access}, "a@x")
	}
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.queue.Shutdown(context.Background()) })
	send := func(id int, user, note string) *httptest.ResponseRecorder {
		body := `{"object_kind":"note","user":{"username":"` + user + `"},"project":{"path_with_namespace":"acme/pay"},"merge_request":{"iid":7},` +
			`"object_attributes":{"id":` + strconv.Itoa(id) + `,"note":"` + note + `","noteable_type":"MergeRequest"}}`
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	return s, api, send
}

func TestCommandsNeedWriteAccess(t *testing.T) {
	_, api, send := gateServer(t, "")
	// A Guest can't start a review or ask a question; told once.
	for i, note := range []string{"@gitlab-bot full review", "@gitlab-bot what does this do?"} {
		if w := send(100+i, "visitor", note); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "need write access") {
			t.Errorf("guest %q: %d %s", note, w.Code, w.Body)
		}
	}
	// A non-member neither.
	if w := send(110, "stranger", "@gitlab-bot review"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "not a member") {
		t.Errorf("stranger: %d %s", w.Code, w.Body)
	}
	// help costs nothing: anyone may ask for it.
	if w := send(120, "visitor", "@gitlab-bot help"); w.Code != http.StatusAccepted {
		t.Errorf("help: %d %s", w.Code, w.Body)
	}
	// A Developer may.
	if w := send(130, "dev", "@gitlab-bot review"); w.Code != http.StatusAccepted {
		t.Errorf("developer: %d %s", w.Code, w.Body)
	}
	waitFor(t, func() bool { api.mu.Lock(); defer api.mu.Unlock(); return len(api.notes) >= 3 })
	time.Sleep(100 * time.Millisecond)
	api.mu.Lock()
	defer api.mu.Unlock()
	refusals := 0
	for _, n := range api.notes {
		if strings.Contains(n, "Only people who can push") {
			refusals++
		}
	}
	// visitor told once (two refusals), stranger once; plus the help reply.
	if refusals != 2 {
		t.Errorf("refusal notices: %d in %q", refusals, api.notes)
	}
}

func TestCommandsFromAnyoneWhenAllowed(t *testing.T) {
	_, _, send := gateServer(t, settings.CommandAccessAnyone)
	if w := send(200, "stranger", "@gitlab-bot review"); w.Code != http.StatusAccepted {
		t.Errorf("with command access 'anyone' a non-member may: %d %s", w.Code, w.Body)
	}
}

func TestCommandRateLimit(t *testing.T) {
	s, _, send := gateServer(t, "")
	trustAuthors(s)
	for i := 0; i < perPRPerHour; i++ {
		if w := send(300+i, "dev", "@gitlab-bot review"); w.Code != http.StatusAccepted {
			t.Fatalf("command %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := send(399, "dev", "@gitlab-bot review"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "rate limit") {
		t.Errorf("over the per-MR limit: %d %s", w.Code, w.Body)
	}
	// help isn't counted.
	if w := send(398, "dev", "@gitlab-bot help"); w.Code != http.StatusAccepted {
		t.Errorf("help is not rate limited: %d", w.Code)
	}
}

func TestCommandAccessSetting(t *testing.T) {
	bad := settings.Stored{CommandAccess: "guests"}
	if err := bad.Validate(nil, true); err == nil {
		t.Error("unknown command access must be refused")
	}
	if e := settings.New(&config.Config{}, nil).Get(context.Background()); e.CommandAccess != settings.CommandAccessWrite {
		t.Errorf("default: %q", e.CommandAccess)
	}
}

func TestBitbucketAuthorRole(t *testing.T) {
	bb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/permissions-config/users/557058:writer"):
			_, _ = w.Write([]byte(`{"permission":"write"}`))
		case strings.HasSuffix(r.URL.Path, "/permissions-config/users/557058:reader"):
			_, _ = w.Write([]byte(`{"permission":"read"}`))
		default:
			w.WriteHeader(403) // the token isn't a repository admin
		}
	}))
	defer bb.Close()
	s := testServer(t)
	tgt := target{host: hostsHost("bitbucket", bb.URL), secrets: func(context.Context) (map[string]string, error) { return map[string]string{}, nil }}
	ref := vcsRef("bitbucket", "acme/app")
	for author, want := range map[string]struct {
		role    string
		trusted bool
	}{"557058:writer": {"write", true}, "557058:reader": {"read", false}, "557058:other": {"unknown", false}, "nickname": {"unknown", false}} {
		if role, trusted := s.authorRole(context.Background(), tgt, ref, author); role != want.role || trusted != want.trusted {
			t.Errorf("%s: %s %v", author, role, trusted)
		}
	}
}
