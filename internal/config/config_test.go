package config

import (
	"os"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("CODEREVIEW_MODEL", "")
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load("../../examples/console.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Executor.Type != "local" || cfg.Executor.Kubernetes.Namespace != "code-review" || cfg.Executor.Docker.Memory != "1g" {
		t.Errorf("executor = %+v", cfg.Executor)
	}
	if cfg.Model != "claude" || len(cfg.Models) != 2 || cfg.GitHub.AppID != 123456 || cfg.BotName != "" {
		t.Errorf("engine settings not inlined: model=%q models=%d app=%d", cfg.Model, len(cfg.Models), cfg.GitHub.AppID)
	}
	if !cfg.Admin.Enabled || cfg.Server.Workers != 4 || cfg.Server.Debounce.Seconds() != 30 {
		t.Errorf("console settings = %+v %+v", cfg.Admin, cfg.Server)
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
		"admin:\n  enabled: true\n":       "admin.enabled needs database.url",
		"repos:\n  allowlist: true\n":     "repos.allowlist needs database.url",
		"executor:\n  type: kubernetes\n  kubernetes:\n    image: x:1.0.0\n    ca_bundle: {config_map: a, secret: b}\n": "set config_map or secret, not both",
	}
	for body, want := range cases {
		if _, err := Load(write(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}
