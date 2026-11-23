package config

import (
	"os"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	for _, e := range removedEnv {
		t.Setenv(e, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load("../../examples/console.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Executor.Type != "local" || cfg.Executor.Kubernetes.Namespace != "code-review" || cfg.Executor.Docker.Image == "" {
		t.Errorf("executor = %+v", cfg.Executor)
	}
	if !cfg.Admin.Enabled || cfg.Server.Addr != ":8080" || cfg.Database.URL != "postgres://x" {
		t.Errorf("console settings = %+v %+v", cfg.Admin, cfg.Server)
	}
	if keys := cfg.RemovedKeys(); len(keys) != 0 {
		t.Errorf("the example uses keys managed in the admin panel: %v", keys)
	}
}

func TestRemovedKeys(t *testing.T) {
	for _, e := range removedEnv {
		t.Setenv(e, "")
	}
	t.Setenv("GITLAB_TOKEN", "glpat-x")
	p := t.TempDir() + "/c.yaml"
	_ = os.WriteFile(p, []byte(`gitlab:
  - token: x
models:
  m: {provider: ollama, model: q}
server:
  workers: 2
  dashboard: {title: Hi, token: t}
executor:
  kubernetes:
    namespace: ns
    cpu: "2"
`), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cfg.RemovedKeys(), ",")
	if got != "gitlab,models,server.workers,server.dashboard,executor.kubernetes.cpu,$GITLAB_TOKEN" {
		t.Errorf("removed keys: %s", got)
	}
	// Still read, so they can be copied into the database once.
	if len(cfg.GitLab) != 1 || cfg.Executor.Kubernetes.CPU != "2" {
		t.Errorf("removed keys are still read for the first start: %+v", cfg.GitLab)
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := dir + "/c.yaml"
		_ = os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	t.Setenv("DATABASE_URL", "")
	cases := map[string]string{
		"executor:\n  type: docker\n":     "executor.docker.image",
		"executor:\n  type: kubernetes\n": "executor.kubernetes.image",
		"executor:\n  type: ssh\n":        "want local, docker or kubernetes",
		"executor:\n  type: kubernetes\n  kubernetes:\n    image: x:1.0.0\n    ca_bundle: {config_map: a, secret: b}\n": "set config_map or secret, not both",
	}
	for body, want := range cases {
		if _, err := Load(write(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}

func clearStartupEnv(t *testing.T) {
	t.Helper()
	for _, e := range EnvVars {
		t.Setenv(e.Name, "")
	}
	for _, e := range removedEnv {
		t.Setenv(e, "")
	}
	t.Setenv("CODEREVIEW_CONSOLE_CONFIG", "")
}

func TestEnvironmentOnly(t *testing.T) {
	clearStartupEnv(t)
	t.Chdir(t.TempDir()) // no ./console.yaml
	t.Setenv("DATABASE_URL", "postgres://db")
	t.Setenv("CODEREVIEW_CONSOLE_ADDR", ":9090")
	t.Setenv("CODEREVIEW_CONSOLE_BASE_URL", "https://review.example.com/")
	t.Setenv("CODEREVIEW_CONSOLE_OIDC_ISSUER", "https://idp.example.com")
	t.Setenv("CODEREVIEW_CONSOLE_OIDC_CLIENT_ID", "console")
	t.Setenv("CODEREVIEW_CONSOLE_OIDC_ALLOWED_EMAILS", "a@example.com, b@example.com")
	t.Setenv("CODEREVIEW_CONSOLE_SESSION_TTL", "2h")
	t.Setenv("CODEREVIEW_CONSOLE_EXECUTOR", "kubernetes")
	t.Setenv("CODEREVIEW_CONSOLE_IMAGE", "ghcr.io/divmora/code-review-bot:0.4.1")
	t.Setenv("CODEREVIEW_CONSOLE_KUBE_NAMESPACE", "reviews")
	t.Setenv("CODEREVIEW_CONSOLE_KUBE_AUTOMOUNT_TOKEN", "false")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.File() != "" || cfg.Database.URL != "postgres://db" || cfg.Server.Addr != ":9090" || cfg.Admin.BaseURL != "https://review.example.com" {
		t.Errorf("server: %+v %+v", cfg.Server, cfg.Admin)
	}
	if !cfg.Admin.Enabled || cfg.Admin.SessionTTL.Hours() != 2 || len(cfg.Admin.OIDC.AllowedEmails) != 2 || cfg.Admin.OIDC.AllowedEmails[1] != "b@example.com" {
		t.Errorf("admin is on once an issuer is set: %+v", cfg.Admin)
	}
	k := cfg.Executor.Kubernetes
	if cfg.Executor.Type != "kubernetes" || k.Image != "ghcr.io/divmora/code-review-bot:0.4.1" || k.Namespace != "reviews" {
		t.Errorf("executor: %+v", cfg.Executor)
	}
	if len(cfg.EnvUsed()) != 11 || len(cfg.RemovedKeys()) != 0 {
		t.Errorf("env used: %v, removed: %v", cfg.EnvUsed(), cfg.RemovedKeys())
	}
}

func TestEnvironmentWinsOverFile(t *testing.T) {
	clearStartupEnv(t)
	p := t.TempDir() + "/c.yaml"
	_ = os.WriteFile(p, []byte("server: {addr: ':8000'}\nadmin: {enabled: false, oidc: {issuer: https://file.example.com}}\n"), 0o600)
	t.Setenv("CODEREVIEW_CONSOLE_ADDR", ":7000")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":7000" || cfg.File() != p {
		t.Errorf("addr = %s", cfg.Server.Addr)
	}
	if cfg.Admin.Enabled {
		t.Error("admin.enabled: false in the file is kept")
	}
	t.Setenv("CODEREVIEW_CONSOLE_ADMIN_ENABLED", "yes please")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "CODEREVIEW_CONSOLE_ADMIN_ENABLED") {
		t.Errorf("a bad boolean names the variable: %v", err)
	}
}

func TestTrustedProxyOptions(t *testing.T) {
	clearStartupEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS", "1")
	cfg, err := Load("")
	if err != nil || cfg.Server.TrustedProxyHops != 1 {
		t.Fatalf("hops from the environment: %v %+v", err, cfg)
	}
	t.Setenv("CODEREVIEW_CONSOLE_TRUSTED_PROXIES", "10.0.5.7")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("addresses and hops together are refused: %v", err)
	}
	t.Setenv("CODEREVIEW_CONSOLE_TRUSTED_PROXIES", "")
	t.Setenv("CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS", "many")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS") {
		t.Errorf("a bad number names the variable: %v", err)
	}
}
