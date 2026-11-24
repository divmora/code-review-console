package hosts

import (
	"context"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestCredentialFor: the most specific enabled credential covers a
// repository, the way Rules resolve; the host's own token covers the rest
// (issue #136).
func TestCredentialFor(t *testing.T) {
	ctx := context.Background()
	r := managed(t)
	h, err := r.Save(ctx, Host{Platform: GitLab, BaseURL: "https://gitlab.example.com", Enabled: true,
		Settings: Settings{BotName: "review-bot"}, Secrets: Secrets{Token: "glpat-host"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	add := func(pattern, token string, settings CredentialSettings) Credential {
		t.Helper()
		c, err := r.SaveCredential(ctx, Credential{HostID: h.ID, Pattern: pattern, Enabled: true, Settings: settings, CredentialSecrets: CredentialSecrets{Token: token}}, "a@x")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	acme := add("acme", "glpat-acme", CredentialSettings{})
	platform := add("acme/platform", "glpat-platform", CredentialSettings{BotName: "platform-bot", Reviewer: "platform-reviewer"})
	svc := add("acme/svc-*", "glpat-svc", CredentialSettings{})
	h, _ = r.Get(h.ID)

	for repo, want := range map[string]string{
		"acme/app":               "glpat-acme",
		"acme/platform/api":      "glpat-platform", // the subgroup beats the group
		"acme/platform/sub/deep": "glpat-platform",
		"acme/svc-billing":       "glpat-svc", // acme/svc-* has more segments than acme
		"other/app":              "glpat-host",
		"acmeish/app":            "glpat-host", // a group matches whole segments only
		"":                       "glpat-host",
		"ACME/Platform/Api":      "glpat-platform", // paths compare case-insensitively
	} {
		if got := r.ForRepo(h, repo); got.Token != want {
			t.Errorf("%q: token %q, want %q", repo, got.Token, want)
		}
	}
	got := r.ForRepo(h, "acme/platform/api")
	if got.Credential != platform.ID || got.BotName != "platform-bot" || got.Reviewer != "platform-reviewer" {
		t.Errorf("credential identity: %+v", got)
	}
	if got := r.ForRepo(h, "acme/app"); got.Credential != acme.ID || got.BotName != "review-bot" {
		t.Errorf("a credential without a bot name keeps the host's: %+v", got)
	}
	if got := r.ForRepo(h, "acme/svc-billing"); got.Credential != svc.ID {
		t.Errorf("pattern: %+v", got)
	}

	// A disabled credential is skipped: the next one applies.
	platform.Enabled = false
	if _, err := r.SaveCredential(ctx, platform, "a@x"); err != nil {
		t.Fatal(err)
	}
	if got := r.ForRepo(h, "acme/platform/api"); got.Token != "glpat-acme" {
		t.Errorf("disabled credential: %q", got.Token)
	}
	// Deleted: the same.
	if err := r.DeleteCredential(ctx, acme.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if got := r.ForRepo(h, "acme/app"); got.Token != "glpat-host" || got.Credential != 0 {
		t.Errorf("after delete: %+v", got)
	}
	// Without the hosts license feature, credentials are not used.
	r.Lic = nil
	if got := r.ForRepo(h, "acme/svc-billing"); got.Token != "glpat-host" {
		t.Errorf("unlicensed: %q", got.Token)
	}
}

// TestCredentialGitHubApp: a credential's token replaces the host's GitHub App
// for the repositories it covers.
func TestCredentialGitHubApp(t *testing.T) {
	h := Host{ID: 1, Platform: GitHub, Host: "github.com", Settings: Settings{AppID: 7}, Secrets: Secrets{AppPrivateKey: "k"}}
	got := h.WithCredential(Credential{ID: 3, Pattern: "acme", CredentialSecrets: CredentialSecrets{Token: "ghp_acme"}})
	if got.AppID != 0 || got.AppPrivateKey != "" || got.Token != "ghp_acme" || got.Credential != 3 {
		t.Errorf("%+v", got)
	}
}

func TestSaveCredentialValidates(t *testing.T) {
	ctx := context.Background()
	r := managed(t)
	gl, err := r.Save(ctx, Host{Platform: GitLab, BaseURL: "https://gitlab.example.com", Enabled: true}, "a@x")
	if err != nil {
		t.Fatalf("a host without its own token is allowed: %v", err)
	}
	bb, err := r.Save(ctx, Host{Platform: Bitbucket, Enabled: true, Secrets: Secrets{Token: "bb"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		c    Credential
		want string
	}{
		"no pattern":      {Credential{HostID: gl.ID, CredentialSecrets: CredentialSecrets{Token: "t"}}, "the host's own token covers the whole host"},
		"bad pattern":     {Credential{HostID: gl.ID, Pattern: "a/../b", CredentialSecrets: CredentialSecrets{Token: "t"}}, "pattern"},
		"no token":        {Credential{HostID: gl.ID, Pattern: "acme"}, "needs a token"},
		"bitbucket half":  {Credential{HostID: bb.ID, Pattern: "ws", Settings: CredentialSettings{Email: "a@x"}}, "email and API token"},
		"api token on gl": {Credential{HostID: gl.ID, Pattern: "acme", CredentialSecrets: CredentialSecrets{Token: "t", APIToken: "x"}}, "for Bitbucket"},
		"no host":         {Credential{HostID: 999, Pattern: "acme", CredentialSecrets: CredentialSecrets{Token: "t"}}, "host not found"},
	} {
		if _, err := r.SaveCredential(ctx, c.c, "a@x"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
	if _, err := r.SaveCredential(ctx, Credential{HostID: gl.ID, Pattern: "/Acme/", Enabled: true, CredentialSecrets: CredentialSecrets{Token: "t"}}, "a@x"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveCredential(ctx, Credential{HostID: gl.ID, Pattern: "acme", CredentialSecrets: CredentialSecrets{Token: "t2"}}, "a@x"); err == nil || !strings.Contains(err.Error(), "already has a credential for acme") {
		t.Errorf("duplicate pattern: %v", err)
	}
	if _, err := r.SaveCredential(ctx, Credential{HostID: bb.ID, Pattern: "ws", Enabled: true, Settings: CredentialSettings{Email: "a@x"}, CredentialSecrets: CredentialSecrets{APIToken: "x"}}, "a@x"); err != nil {
		t.Errorf("bitbucket email and API token: %v", err)
	}
}

// TestCredentialsPersistEncrypted: credentials survive a reload, with
// their token encrypted at rest and only its name in the audit log.
func TestCredentialsPersistEncrypted(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	box := testBox(t)
	r := New(&config.Config{}, st, box, nil)
	r.Lic = licensetest.Full(t)
	if _, err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	h, _ := r.Save(ctx, Host{Platform: GitLab, BaseURL: "https://gitlab.example.com", Enabled: true}, "a@x")
	if _, err := r.SaveCredential(ctx, Credential{HostID: h.ID, Name: "Platform", Pattern: "acme", Enabled: true, CredentialSecrets: CredentialSecrets{Token: "glpat-secret"}}, "a@x"); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.ListHostCredentials(ctx)
	if len(rows) != 1 || strings.Contains(rows[0].Secrets, "glpat-secret") || rows[0].Secrets == "" {
		t.Fatalf("stored: %+v", rows)
	}
	entries, _ := st.ListAudit(ctx, 10)
	for _, e := range entries {
		if strings.Contains(string(e.Detail), "glpat-secret") {
			t.Errorf("the audit log has the token: %s", e.Detail)
		}
	}
	r2 := New(&config.Config{}, st, box, nil)
	r2.Lic = licensetest.Full(t)
	if _, err := r2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cs := r2.Credentials(h.ID)
	if len(cs) != 1 || cs[0].Token != "glpat-secret" || cs[0].Name != "Platform" {
		t.Errorf("reloaded: %+v", cs)
	}
}

// TestExportImportCredentials moves credentials with their host.
func TestExportImportCredentials(t *testing.T) {
	ctx := context.Background()
	src := managed(t)
	h, _ := src.Save(ctx, Host{Platform: GitLab, BaseURL: "https://gitlab.example.com", Enabled: true, Secrets: Secrets{Token: "host"}}, "a@x")
	if _, err := src.SaveCredential(ctx, Credential{HostID: h.ID, Pattern: "acme", Enabled: true, Settings: CredentialSettings{BotName: "acme-bot"}, CredentialSecrets: CredentialSecrets{Token: "glpat-acme"}}, "a@x"); err != nil {
		t.Fatal(err)
	}
	dst := managed(t)
	if _, err := dst.Import(ctx, src.Export(true, ""), "cli", false); err != nil {
		t.Fatal(err)
	}
	dh, _ := dst.Find(GitLab, "gitlab.example.com")
	cs := dst.Credentials(dh.ID)
	if len(cs) != 1 || cs[0].Token != "glpat-acme" || cs[0].Settings.BotName != "acme-bot" {
		t.Fatalf("imported: %+v", cs)
	}
	// Exported without secrets, an import keeps the stored token.
	if _, err := dst.Import(ctx, src.Export(false, ""), "cli", false); err != nil {
		t.Fatal(err)
	}
	if cs := dst.Credentials(dh.ID); len(cs) != 1 || cs[0].Token != "glpat-acme" {
		t.Errorf("after an import without secrets: %+v", cs)
	}
	// Without the hosts feature, credentials can't be imported.
	free := New(&config.Config{}, storetest.New(), testBox(t), nil)
	if _, err := free.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := free.Import(ctx, src.Export(true, ""), "cli", true); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("unlicensed import: %v", err)
	}
}
