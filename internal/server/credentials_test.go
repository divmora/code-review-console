package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/hosts"
)

// TestCredentialsPerGroup: a repository's jobs, comments and identity use
// the credential that covers its group; other repositories the host's own
// token; and every bot user on the host counts as the bot (issue #136).
func TestCredentialsPerGroup(t *testing.T) {
	users := map[string]string{"glpat-host": "host-bot", "glpat-acme": "group_7_bot_ab12"}
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			if u, ok := users[r.Header.Get("PRIVATE-TOKEN")]; ok {
				_, _ = w.Write([]byte(`{"username":"` + u + `"}`))
				return
			}
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer gl.Close()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: gl.URL, Token: "glpat-host", WebhookSecret: "s"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	h := s.hosts.Enabled(hosts.GitLab)[0]
	ctx := context.Background()
	acme, err := s.hosts.SaveCredential(ctx, hosts.Credential{HostID: h.ID, Name: "Acme", Pattern: "acme", Enabled: true,
		CredentialSecrets: hosts.CredentialSecrets{Token: "glpat-acme"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := s.hosts.SaveCredential(ctx, hosts.Credential{HostID: h.ID, Pattern: "broken", Enabled: true,
		CredentialSecrets: hosts.CredentialSecrets{Token: "glpat-revoked"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}

	secret := func(repo string) (string, error) {
		env, err := s.gitlabTarget(h, repo).secrets(ctx)
		return env["GITLAB_TOKEN"], err
	}
	if tok, err := secret("acme/app"); err != nil || tok != "glpat-acme" {
		t.Errorf("acme/app job token: %q %v", tok, err)
	}
	if tok, err := secret("other/app"); err != nil || tok != "glpat-host" {
		t.Errorf("other/app job token: %q %v", tok, err)
	}

	// Identities: the startup lookup ran per credential (OnChange).
	if c, ok := s.hosts.CredentialCheckOf(acme.ID); !ok || !c.OK || c.User != "group_7_bot_ab12" || c.Source != "startup" {
		t.Errorf("acme check: %+v %v", c, ok)
	}
	if c, ok := s.hosts.CredentialCheckOf(bad.ID); !ok || c.OK {
		t.Errorf("revoked credential check: %+v %v", c, ok)
	}
	acmeTgt := s.gitlabTarget(h, "acme/app")
	if acmeTgt.botName != "group_7_bot_ab12" || s.reviewerFor(acmeTgt) != "group_7_bot_ab12" {
		t.Errorf("acme identity: mention %q reviewer %q", acmeTgt.botName, s.reviewerFor(acmeTgt))
	}
	if other := s.gitlabTarget(h, "other/app"); other.botName != "host-bot" {
		t.Errorf("host identity: %q", other.botName)
	}
	// Either bot's comments are the bot's: they never answer each other.
	if !s.isSelf(h.Host, "host-bot") || !s.isSelf(h.Host, "GROUP_7_BOT_AB12") || s.isSelf(h.Host, "alice") || s.isSelf("other.example", "host-bot") {
		t.Error("isSelf must cover the host's and its credentials' bot users, on that host only")
	}
	// The status dashboard lists both handles.
	all, _ := s.mentions(false, allRepos)
	var handles []string
	for _, m := range all {
		handles = append(handles, m.Handle)
	}
	if got := strings.Join(handles, ","); !strings.Contains(got, "host-bot") || !strings.Contains(got, "group_7_bot_ab12") {
		t.Errorf("mentions: %s", got)
	}

	// Without a token of its own, a repository no credential covers fails clearly.
	h.Token = ""
	if _, err := s.hosts.Save(ctx, h, "a@x"); err != nil {
		t.Fatal(err)
	}
	h = s.hosts.Enabled(hosts.GitLab)[0]
	if _, err := secret("other/app"); err == nil || !strings.Contains(err.Error(), "no credential covers other/app") {
		t.Errorf("uncovered repository: %v", err)
	}
	if tok, err := secret("acme/app"); err != nil || tok != "glpat-acme" {
		t.Errorf("covered repository without a host token: %q %v", tok, err)
	}
	if c, ok := s.hosts.CheckOf(h.ID); !ok || c.Summary != "Credentials only" {
		t.Errorf("host check without a token: %+v", c)
	}
}
