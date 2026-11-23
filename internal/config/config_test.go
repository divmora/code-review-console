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
