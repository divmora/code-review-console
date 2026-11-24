package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
)

// TestStartupHostCheck: the identity lookup at start is recorded as the
// host's check, so the Code hosts page shows the token user instead of
// "Not tested" (issue #131).
func TestStartupHostCheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			_, _ = w.Write([]byte(`{"id": 7, "username": "gitlab-bot"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	}))
	defer bad.Close()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: ok.URL, Token: "t", WebhookSecret: "a"}, {BaseURL: bad.URL, Token: "t", WebhookSecret: "b"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	seen := 0
	for _, h := range s.hosts.Enabled("") {
		c, found := s.hosts.CheckOf(h.ID)
		switch h.BaseURL {
		case ok.URL:
			seen++
			if !found || !c.OK || c.User != "gitlab-bot" || c.Source != "startup" || c.Summary != "OK as @gitlab-bot" {
				t.Errorf("working token: %+v (found %v)", c, found)
			}
		case bad.URL:
			seen++
			if !found || c.OK || c.Source != "startup" || c.Message == "" {
				t.Errorf("rejected token: %+v (found %v)", c, found)
			}
		}
	}
	if seen != 2 {
		t.Errorf("checked %d of 2 hosts", seen)
	}
}
