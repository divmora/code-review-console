package server

import (
	"context"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
)

// Issue #111: the dashboard said "mention @codereview" on a host where the
// bot acts as its token user.
func TestDashboardMentions(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.BotName = ""
	cfg.GitLab = []config.GitLab{
		{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "x1"},
		{BaseURL: "http://127.0.0.2:1", Token: "t", WebhookSecret: "y1", Identity: config.Identity{BotName: "ybot"}},
	}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	s.selfLogins["127.0.0.1:1"] = "gitlab-bot"
	ms, common := s.mentions(false)
	if common != "" || len(ms) != 2 {
		t.Fatalf("two handles: %q %+v", common, ms)
	}
	got := map[string]string{}
	for _, m := range ms {
		got[m.Host] = m.Handle
	}
	if got["127.0.0.1:1"] != "gitlab-bot" || got["127.0.0.2:1"] != "ybot" {
		t.Errorf("each host's handle: %+v", got)
	}
	if ms, _ := s.mentions(true); len(ms) != 2 || ms[0].Host != "" || ms[1].Host != "" {
		t.Errorf("host names hidden with the repositories: %+v", ms)
	}

	s.selfLogins["127.0.0.1:1"] = "ybot" // the same handle everywhere: one
	if ms, common := s.mentions(true); common != "ybot" || len(ms) != 1 {
		t.Errorf("one handle for all hosts: %q %+v", common, ms)
	}
}
