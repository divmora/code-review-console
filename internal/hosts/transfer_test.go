package hosts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func managed(t *testing.T) *Registry {
	t.Helper()
	r := New(&config.Config{}, storetest.New(), testBox(t), nil)
	r.Lic = licensetest.Full(t)
	if _, err := r.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestExportImportRoundTrip moves hosts and webhooks to a fresh install:
// same webhook IDs (so URLs on the code host keep working) and secrets.
func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := New(fileConfig(t), storetest.New(), testBox(t), nil)
	src.Lic = licensetest.Full(t)
	if _, err := src.Init(ctx); err != nil {
		t.Fatal(err)
	}
	gl, _ := src.Find(GitLab, "gitlab.example.com")
	ep, secret, err := src.AddEndpoint(ctx, gl.ID, "platform group", "a@x")
	if err != nil {
		t.Fatal(err)
	}

	// Without secrets: nothing secret in the file.
	plain, _ := yaml.Marshal(src.Export(false, "https://review.example.com"))
	if strings.Contains(string(plain), secret) || strings.Contains(string(plain), "glpat-file") || !strings.Contains(string(plain), "https://review.example.com/webhooks/gitlab/"+ep.ID) {
		t.Fatalf("export without secrets:\n%s", plain)
	}

	full, _ := yaml.Marshal(src.Export(true, ""))
	f, err := ParseFile(full)
	if err != nil {
		t.Fatal(err)
	}
	dst := managed(t)
	res, err := dst.Import(ctx, f, "cli", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets) != 0 || len(dst.All()) != 3 || !strings.Contains(strings.Join(res.Lines, "\n"), "host gitlab:gitlab.example.com: created") {
		t.Fatalf("import: %+v", res)
	}
	h, got, err := dst.Route(GitLab, ep.ID)
	if err != nil || got.Name != "platform group" || h.WebhookSecrets[0] != secret || h.Token != "glpat-file" {
		t.Fatalf("moved webhook: %+v %+v %v", h, got, err)
	}

	// Importing the secret-free file again changes nothing secret.
	f2, _ := ParseFile(plain)
	if _, err := dst.Import(ctx, f2, "cli", false); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := dst.Route(GitLab, ep.ID); h.WebhookSecrets[0] != secret || h.Token != "glpat-file" {
		t.Errorf("absent secrets must be kept: %+v", h)
	}
}

func TestImportEnvNewWebhookAndDryRun(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TEST_GL_TOKEN", "glpat-from-env")
	r := managed(t)
	f, err := ParseFile([]byte(`
hosts:
  - platform: gitlab
    base_url: https://gitlab.example.com
    name: Our GitLab
    token: ${TEST_GL_TOKEN}
    settings: {bot_name: codereview}
    webhooks:
      - name: group a
`))
	if err != nil {
		t.Fatal(err)
	}
	dry, err := r.Import(ctx, f, "cli", true)
	if err != nil || len(r.All()) != 0 || len(dry.Secrets) != 0 || !strings.Contains(strings.Join(dry.Lines, "\n"), "would create") {
		t.Fatalf("dry run must write nothing: %+v %v %d", dry, err, len(r.All()))
	}
	res, err := r.Import(ctx, f, "cli", false)
	if err != nil || len(res.Secrets) != 1 {
		t.Fatalf("import: %+v %v", res, err)
	}
	h, _ := r.Find(GitLab, "gitlab.example.com")
	eps := r.Endpoints(h.ID)
	if h.Token != "glpat-from-env" || h.Name != "Our GitLab" || h.BotName != "codereview" || len(eps) != 1 || eps[0].Name != "group a" {
		t.Fatalf("imported: %+v %+v", h, eps)
	}
	for _, s := range res.Secrets {
		if eps[0].WebhookSecrets[0] != s {
			t.Error("the printed secret must be the stored one")
		}
	}
}

func TestImportRejects(t *testing.T) {
	ctx := context.Background()
	r := managed(t)
	other, _ := r.Save(ctx, Host{Platform: GitHub, BaseURL: "https://api.github.com", Enabled: true, Secrets: Secrets{Token: "t"}}, "a@x")
	foreign, _, _ := r.AddEndpoint(ctx, other.ID, "gh", "a@x")
	for name, c := range map[string]string{
		"unknown field": "hosts:\n  - platform: gitlab\n    tokn: x\n",
		"bad platform":  "hosts:\n  - platform: svn\n",
		"no token":      "hosts:\n  - platform: gitlab\n    base_url: https://g.example\n",
		"listed twice":  "hosts:\n  - {platform: gitlab, base_url: https://g.example, token: t}\n  - {platform: gitlab, base_url: https://g.example/, token: t}\n",
		"bad id":        "hosts:\n  - platform: gitlab\n    base_url: https://g.example\n    token: t\n    webhooks: [{id: nope}]\n",
		"foreign id":    "hosts:\n  - platform: gitlab\n    base_url: https://g.example\n    token: t\n    webhooks: [{id: " + foreign.ID + "}]\n",
		"github sig":    "hosts:\n  - platform: github\n    token: t\n    webhooks: [{name: x, signing_tokens: [" + randomSigningToken(t) + "]}]\n",
		"bad sig":       "hosts:\n  - platform: gitlab\n    base_url: https://g.example\n    token: t\n    webhooks: [{name: x, signing_tokens: [nope]}]\n",
	} {
		f, err := ParseFile([]byte(c))
		if err == nil {
			_, err = r.Import(ctx, f, "cli", false)
		}
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// A failed import writes nothing: the valid first host is not saved.
	f, _ := ParseFile([]byte("hosts:\n  - {platform: gitlab, base_url: https://ok.example, token: t}\n  - {platform: bitbucket}\n"))
	if _, err := r.Import(ctx, f, "cli", false); err == nil {
		t.Fatal("want an error for the bitbucket host without credentials")
	}
	if _, ok := r.Find(GitLab, "ok.example"); ok {
		t.Error("nothing may be written when any host is invalid")
	}
	// Config mode refuses.
	ro := New(fileConfig(t), storetest.New(), nil, nil)
	_, _ = ro.Init(ctx)
	if _, err := ro.Import(ctx, File{}, "cli", false); err == nil {
		t.Error("config mode must refuse imports")
	}
}

func TestImportFreeCore(t *testing.T) {
	ctx := context.Background()
	r := New(&config.Config{}, storetest.New(), testBox(t), nil) // nil license: the free core
	if _, err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	two, _ := ParseFile([]byte("hosts:\n  - {platform: gitlab, base_url: https://a.example, token: t}\n  - {platform: gitlab, base_url: https://b.example, token: t}\n"))
	var le *license.Error
	if _, err := r.Import(ctx, two, "cli", false); !errors.As(err, &le) || len(r.All()) != 0 {
		t.Errorf("free core: a second host on a platform needs the hosts feature: %v", err)
	}
	ident, err := ParseFile([]byte("hosts:\n  - {platform: gitlab, base_url: https://a.example, token: t, settings: {bot_name: x}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Import(ctx, ident, "cli", false); !errors.As(err, &le) {
		t.Errorf("free core: a custom bot identity needs the hosts feature: %v", err)
	}
	one, _ := ParseFile([]byte("hosts:\n  - {platform: gitlab, base_url: https://a.example, token: t}\n  - {platform: github, token: t}\n"))
	if _, err := r.Import(ctx, one, "cli", false); err != nil || len(r.All()) != 2 {
		t.Errorf("free core: one host per platform is fine: %v", err)
	}
}
