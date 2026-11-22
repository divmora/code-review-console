package hosts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

func fileConfig(t *testing.T) *config.Config {
	cfg := &config.Config{}
	cfg.GitHub = config.GitHub{APIURL: "https://api.github.com", Token: "ghp_file", WebhookSecret: "gh-hook"}
	cfg.GitLab = []config.GitLab{{BaseURL: "https://gitlab.example.com/", Token: "glpat-file", SigningToken: randomSigningToken(t)}}
	cfg.Bitbucket = []config.Bitbucket{{Token: "bb-token", WebhookSecret: "bb-hook"}}
	return cfg
}

func TestSeedEncryptAndReload(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	r := New(fileConfig(t), st, testBox(t), nil)
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
	cfg2 := fileConfig(t)
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
	r := New(fileConfig(t), st, nil, nil)
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
		{Host{Platform: GitHub, Settings: Settings{AppID: 1}, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "token, or a GitHub App"},
		{Host{Platform: GitLab, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "access token"},
		{Host{Platform: GitLab, Secrets: Secrets{Token: "t", SigningTokens: []string{"nope"}}}, "whsec_"},
		{Host{Platform: Bitbucket, Settings: Settings{Email: "a@x"}, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "API token"},
	} {
		if err := c.h.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: want %q, got %v", c.h, c.want, err)
		}
	}
	for _, c := range []struct {
		h    Host
		want string
	}{
		{Host{Platform: GitHub}, "webhook secret"},
		{Host{Platform: GitLab}, "signing token"},
		{Host{Platform: GitLab, Settings: Settings{RequireSignature: true}, Secrets: Secrets{WebhookSecrets: []string{"s"}}}, "require signature"},
		{Host{Platform: Bitbucket}, "webhook secret"},
	} {
		if err := c.h.ValidateShared(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("shared URL %+v: want %q, got %v", c.h, c.want, err)
		}
	}
	if err := (Host{Platform: GitHub, Secrets: Secrets{Token: "t"}}).Validate(); err != nil {
		t.Errorf("a host managed in the admin panel needs no webhook secret of its own: %v", err)
	}
	h := Host{Platform: "gitlab", BaseURL: "ftp://x"}
	if err := h.Normalize(); err == nil {
		t.Error("non-http URLs must be rejected")
	}
	if WebHost(GitHub, "https://ghe.example.com/api/v3") != "ghe.example.com" {
		t.Error("GitHub Enterprise web host")
	}
}

// randomSigningToken returns a fresh GitLab webhook signing token
// ("whsec_" + base64 of 32 random bytes), so no fixed token is in the code.
func randomSigningToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

func TestEndpointsEncryptedAndRouted(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	r := New(fileConfig(t), st, testBox(t), nil)
	if _, err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	gl, _ := r.Find(GitLab, "gitlab.example.com")
	e, secret, err := r.AddEndpoint(ctx, gl.ID, "  platform group  ", "a@x")
	if err != nil || len(secret) != 64 || e.Name != "platform group" || len(e.ID) != 36 {
		t.Fatalf("add: %+v %q %v", e, secret, err)
	}
	rows, _ := st.ListWebhookEndpoints(ctx)
	if len(rows) != 1 || strings.Contains(rows[0].Secrets, secret) || !strings.HasPrefix(rows[0].Secrets, "v1:") {
		t.Fatalf("endpoint secrets must be stored encrypted: %+v", rows)
	}
	// A fresh registry decrypts them.
	r2 := New(fileConfig(t), st, testBox(t), nil)
	if err := r2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	h, got, err := r2.Route(GitLab, strings.ToUpper(e.ID))
	if err != nil || got.ID != e.ID || h.ID != gl.ID || len(h.WebhookSecrets) != 1 || h.WebhookSecrets[0] != secret || len(h.SigningTokens) != 0 || h.Token != "glpat-file" {
		t.Fatalf("route: %+v %+v %v", h, got, err)
	}
	if _, _, err := r2.Route(GitHub, e.ID); err != ErrNoEndpoint {
		t.Errorf("another platform must not find it: %v", err)
	}
	// Signing tokens are GitLab-only and must be well formed.
	e.SigningTokens = []string{"nope"}
	if _, err := r2.SaveEndpoint(ctx, e, "a@x"); err == nil {
		t.Error("a malformed signing token must be rejected")
	}
	gh, _ := r2.Find(GitHub, "")
	ghEP, _, _ := r2.AddEndpoint(ctx, gh.ID, "", "a@x")
	if ghEP.Name != "Webhook" {
		t.Errorf("default name: %q", ghEP.Name)
	}
	ghEP.SigningTokens = []string{randomSigningToken(t)}
	if _, err := r2.SaveEndpoint(ctx, ghEP, "a@x"); err == nil {
		t.Error("signing tokens are for GitLab only")
	}
	// Disabling the host makes its endpoints unusable; deleting it removes them.
	gl.Enabled = false
	if _, err := r2.Save(ctx, gl, "a@x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r2.Route(GitLab, e.ID); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled host: %v", err)
	}
	if err := r2.Delete(ctx, gl.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.Endpoint(e.ID); ok {
		t.Error("deleting a host deletes its webhooks")
	}
	// Without a key (config mode) webhooks can't be managed.
	ro := New(fileConfig(t), storetest.New(), nil, nil)
	if _, err := ro.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ro.AddEndpoint(ctx, ro.All()[0].ID, "x", "a@x"); err == nil || !strings.Contains(err.Error(), secrets.EnvKey) {
		t.Errorf("config mode must refuse webhooks: %v", err)
	}
}
