package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// The free core: no license, so rules, branch-sync skip and extra hosts are
// off. Installing a license turns them on without a restart.
func TestFreeCore(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{
		{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret", BotName: "custom-bot"},
		{BaseURL: "http://127.0.0.2:1", Token: "t2", WebhookSecret: "other"},
	}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	cfg.Repos.Allowlist = true
	st := storetest.New()
	k := licensetest.NewKey(t)
	lic := k.Manager(t) // nothing installed: the free core
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: lic})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ctx := context.Background()

	// One host per platform; the bot identity is the token user's.
	if hs := s.hosts.Enabled(hosts.GitLab); len(hs) != 1 || hs[0].Host != "127.0.0.1:1" || hs[0].BotName != "" {
		t.Fatalf("free core: want only the first GitLab host, without its custom bot name: %+v", hs)
	}
	if _, ok := s.hosts.Find(hosts.GitLab, "127.0.0.2:1"); ok {
		t.Error("the second GitLab host must be inactive without the hosts feature")
	}

	// Rules are ignored; a repository's own mode still works.
	on := true
	_, _ = st.CreateRule(ctx, store.Rule{Platform: "gitlab", Host: "127.0.0.1:1", Pattern: "acme", Enabled: &on}, "admin@x")
	ref := vcs.Ref{Platform: vcs.GitLab, Host: "127.0.0.1:1", Repo: "acme/finance"}
	if ok, _ := s.gate(ctx, ref); ok {
		t.Error("free core: a group rule must not enable a repository")
	}

	// Branch syncs are reviewed (skipping them is paid).
	repo, _ := st.TouchRepo(ctx, "gitlab", "127.0.0.1:1", "acme/finance")
	_, _ = st.UpdateRepo(ctx, repo.ID, store.ModeEnabled, config.Overrides{}, "admin@x")
	mr := `{"object_kind":"merge_request","project":{"path_with_namespace":"acme/finance","default_branch":"master","web_url":"http://127.0.0.1:1/acme/finance"},` +
		`"object_attributes":{"iid":1,"action":"open","state":"opened","source_branch":"master","target_branch":"release-1"}}`
	req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(mr)))
	req.Header.Set("X-Gitlab-Token", "glsecret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Errorf("free core: a branch sync is reviewed: %d %s", w.Code, w.Body)
	}

	// Install a license: everything turns on at once.
	if err := lic.Install(k.Issue(t, time.Hour, "*"), "database"); err != nil {
		t.Fatal(err)
	}
	if hs := s.hosts.Enabled(hosts.GitLab); len(hs) != 2 {
		t.Errorf("licensed: both hosts active: %d", len(hs))
	}
	if ok, _ := s.gate(ctx, vcs.Ref{Platform: vcs.GitLab, Host: "127.0.0.1:1", Repo: "acme/new"}); !ok {
		t.Error("licensed: the group rule enables acme/new")
	}
	mr2 := strings.Replace(mr, `"iid":1`, `"iid":2`, 1)
	req = httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(mr2)))
	req.Header.Set("X-Gitlab-Token", "glsecret")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "branch sync") {
		t.Errorf("licensed: the branch sync is skipped: %d %s", w.Code, w.Body)
	}
}
