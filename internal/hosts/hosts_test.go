package hosts

import (
	"context"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func testBox(t *testing.T) *secrets.Box {
	b, err := secrets.New("a test passphrase that is long enough for a key")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fileConfig() *config.Config {
	cfg := &config.Config{}
	cfg.GitHub = config.GitHub{APIURL: "https://api.github.com", Token: "ghp_file", WebhookSecret: "gh-hook"}
	cfg.GitLab = []config.GitLab{{BaseURL: "https://gitlab.example.com/", Token: "glpat-file", SigningToken: "whsec_" + "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}}
	cfg.Bitbucket = []config.Bitbucket{{Token: "bb-token", WebhookSecret: "bb-hook"}}
	return cfg
}

func TestSeedEncryptAndReload(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	r := New(fileConfig(), st, testBox(t), nil)
	notes, err := r.Init(ctx)
	if err != nil || r.Mode() != "database" || len(r.All()) != 3 || !strings.Contains(strings.Join(notes, ""), "copied 3 host") {
		t.Fatalf("seed: %v %s %d %v", err, r.Mode(), len(r.All()), notes)
	}
	rows, _ := st.ListHosts(ctx)
	for _, row := range rows {
		if strings.Contains(row.Secrets, "glpat") || strings.Contains(string(row.Config), "glpat") || !strings.HasPrefix(row.Secrets, "v1:") {
			t.Fatalf("secrets must be stored encrypted: %+v", row)
		}
	}
	gl, ok := r.Find(GitLab, "gitlab.example.com")
	if !ok || gl.Token != "glpat-file" || gl.BaseURL != "https://gitlab.example.com" || len(gl.SigningTokens) != 1 {
		t.Fatalf("decrypted gitlab host: %+v", gl)
	}
	if bb, ok := r.Find(Bitbucket, "bitbucket.org"); !ok || bb.BaseURL != "https://api.bitbucket.org/2.0" {
		t.Fatalf("bitbucket defaults: %+v", bb)
	}
	if gh, ok := r.Find(GitHub, ""); !ok || gh.Host != "github.com" {
		t.Fatalf("single github host: %+v", gh)
	}

	// A second start does not copy the file again, and the database wins.
	cfg2 := fileConfig()
	cfg2.GitLab[0].Token = "glpat-changed-in-file"
	r2 := New(cfg2, st, testBox(t), nil)
	if _, err := r2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if gl, _ := r2.Find(GitLab, "gitlab.example.com"); gl.Token != "glpat-file" {
		t.Errorf("the database must win over the config file: %q", gl.Token)
	}

	// Edits from the admin panel apply and notify.
	changed := false
	r2.OnChange = func() { changed = true }
	gl.Token, gl.Enabled = "glpat-new", false
	if _, err := r2.Save(ctx, gl, "a@x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.Find(GitLab, "gitlab.example.com"); ok || !changed {
		t.Errorf("a disabled host is not served; OnChange must run (changed=%v)", changed)
	}
	if h, _ := r2.Get(gl.ID); h.Token != "glpat-new" {
		t.Errorf("saved token: %q", h.Token)
	}
	// The wrong key cannot read the hosts.
	other, _ := secrets.New("a completely different passphrase of enough length")
	if err := New(cfg2, st, other, nil).Load(ctx); err == nil {
		t.Error("loading with the wrong key must fail")
	}
}

func TestConfigModeWithoutKey(t *testing.T) {
	st := storetest.New()
	r := New(fileConfig(), st, nil, nil)
	notes, err := r.Init(context.Background())
	if err != nil || r.Mode() != "config" || len(r.All()) != 3 || !strings.Contains(strings.Join(notes, ""), secrets.EnvKey) {
		t.Fatalf("without a key, hosts come from the file read-only: %v %s %v", err, r.Mode(), notes)
	}
	if rows, _ := st.ListHosts(context.Background()); len(rows) != 0 {
		t.Error("secrets must not be stored without a key")
	}
	if _, err := r.Save(context.Background(), r.All()[0], "a@x"); err == nil {
		t.Error("read-only mode must refuse edits")
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		h    Host
		want string
	}{
		{Host{Platform: GitHub, Secrets: Secrets{Token: "t"}}, "webhook secret"},
		{Host{Platform: GitHub, Settings: Settings{AppID: 1}, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "token, or a GitHub App"},
		{Host{Platform: GitLab, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "access token"},
		{Host{Platform: GitLab, Secrets: Secrets{Token: "t", SigningTokens: []string{"nope"}}}, "whsec_"},
		{Host{Platform: Bitbucket, Settings: Settings{Email: "a@x"}, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "API token"},
	} {
		if err := c.h.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: want %q, got %v", c.h, c.want, err)
		}
	}
	h := Host{Platform: "gitlab", BaseURL: "ftp://x"}
	if err := h.Normalize(); err == nil {
		t.Error("non-http URLs must be rejected")
	}
	if WebHost(GitHub, "https://ghe.example.com/api/v3") != "ghe.example.com" {
		t.Error("GitHub Enterprise web host")
	}
}
