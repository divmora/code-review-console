package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func svc(t *testing.T) (*Service, *storetest.Memory) {
	t.Helper()
	st := storetest.New()
	s := New(&config.Config{Model: "file", Models: map[string]config.ModelProfile{
		"file":   {"provider": "openai", "model": "gpt-x", "api_key": "sk-literal"},
		"shared": {"provider": "ollama", "model": "qwen"},
	}}, st)
	env := map[string]string{"CODEREVIEW_MODEL_KEY_TEAM_CLAUDE": "sk-ant", "OPENAI_API_KEY": "", "CODEREVIEW_CONSOLE_SECRET_KEY": "top secret"}
	s.Getenv = func(k string) string { return env[k] }
	return s, st
}

func TestPutListAndEffective(t *testing.T) {
	ctx := context.Background()
	s, _ := svc(t)
	if err := s.Put(ctx, "claude", config.ModelProfile{"provider": "anthropic", "model": "claude-sonnet-5-5", "api_key_env": "CODEREVIEW_MODEL_KEY_TEAM_CLAUDE", "base_url": " "}, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "shared", config.ModelProfile{"provider": "openai", "model": "gpt-y"}, "a@x"); err != nil {
		t.Fatal(err)
	}
	v, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Profile{}
	for _, p := range v.Profiles {
		by[p.Name] = p
	}
	// With a database, the config file's profiles are not read (only Import copies them).
	if len(v.Profiles) != 2 || v.Default != "" || v.ChoiceSource != "" {
		t.Fatalf("view: %+v", v)
	}
	if c := by["claude"]; c.Source != "console" || c.KeyEnv != "CODEREVIEW_MODEL_KEY_TEAM_CLAUDE" || !c.KeySet || c.Config["base_url"] != nil {
		t.Errorf("claude: %+v", c)
	}
	if sh := by["shared"]; sh.Source != "console" || sh.KeyEnv != "OPENAI_API_KEY" || sh.KeySet {
		t.Errorf("shared (default key env, not set): %+v", sh)
	}

	if err := s.SetChoice(ctx, Choice{Default: "claude", Fallback: []string{"shared"}}, "a@x"); err != nil {
		t.Fatal(err)
	}
	models, def, fb, err := s.Effective(ctx)
	if err != nil || def != "claude" || len(fb) != 1 || models["shared"].String("model") != "gpt-y" || models["file"] != nil {
		t.Fatalf("effective: %v %s %v %v", models, def, fb, err)
	}
	keys, _ := s.KeyEnvs(ctx)
	if len(keys) != 1 || keys["CODEREVIEW_MODEL_KEY_TEAM_CLAUDE"] != "sk-ant" {
		t.Errorf("only set key envs of console profiles go to jobs: %v", keys)
	}
}

func TestWithoutDatabase(t *testing.T) {
	s := New(&config.Config{Model: "file", Models: map[string]config.ModelProfile{
		"file": {"provider": "openai", "model": "gpt-x", "api_key": "sk-literal"},
	}}, nil)
	models, def, _, err := s.Effective(context.Background())
	if err != nil || def != "file" || models["file"].String("api_key") != "sk-literal" {
		t.Fatalf("without a database the config file is used: %v %s %v", models, def, err)
	}
	v, _ := s.List(context.Background())
	if len(v.Profiles) != 1 || !v.Profiles[0].KeyInConfig || v.Profiles[0].Config["api_key"] != nil || v.ChoiceSource != "config" {
		t.Errorf("a literal key is never shown: %+v", v)
	}
}

func TestImport(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	cfg := &config.Config{Model: "fast", Fallback: []string{"smart"}, Models: map[string]config.ModelProfile{
		"fast":    {"provider": "ollama", "model": "qwen"},
		"smart":   {"provider": "anthropic", "model": "claude-sonnet-5-5", "api_key_env": "CODEREVIEW_MODEL_KEY_TEAM"},
		"literal": {"provider": "openai", "model": "gpt-x", "api_key": "sk-literal"},
		"kept":    {"provider": "openai", "model": "from-file"},
	}}
	s := New(cfg, st)
	// A profile already in the database is kept as it is.
	if err := s.Put(ctx, "kept", config.ModelProfile{"provider": "openai", "model": "from-panel"}, "a@x"); err != nil {
		t.Fatal(err)
	}
	imported, skipped, err := s.Import(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(imported, ",") != "fast,smart,default model fast" {
		t.Errorf("imported: %v", imported)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "models.literal") || !strings.Contains(skipped[0], "not stored") {
		t.Errorf("a literal key can't move to the database: %v", skipped)
	}
	models, def, fb, err := s.Effective(ctx)
	if err != nil || def != "fast" || len(fb) != 1 || fb[0] != "smart" || models["kept"].String("model") != "from-panel" || models["literal"] != nil {
		t.Fatalf("effective: %v %s %v %v", models, def, fb, err)
	}
	// Only once: a profile deleted in the panel doesn't come back.
	if err := s.SetChoice(ctx, Choice{Default: "smart"}, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "fast", "a@x"); err != nil {
		t.Fatal(err)
	}
	if imported, _, err := s.Import(ctx); err != nil || len(imported) != 0 {
		t.Errorf("second start: %v %v", imported, err)
	}
	if models, def, _, _ := s.Effective(ctx); models["fast"] != nil || def != "smart" {
		t.Errorf("the panel's changes stand: %v %s", models, def)
	}
}

func TestValidate(t *testing.T) {
	ctx := context.Background()
	s, _ := svc(t)
	for name, c := range map[string]struct {
		name string
		m    config.ModelProfile
		want string
	}{
		"bad name":      {"Has Space", config.ModelProfile{"provider": "openai", "model": "m"}, "name"},
		"provider":      {"x", config.ModelProfile{"provider": "nope", "model": "m"}, "provider"},
		"model":         {"x", config.ModelProfile{"provider": "openai"}, "model is required"},
		"literal key":   {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key": "sk"}, "not stored"},
		"env name":      {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "lower"}, "environment variable"},
		"console's own": {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "CODEREVIEW_CONSOLE_SECRET_KEY"}, "no other secret"},
		"host token":    {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "GITLAB_TOKEN"}, "no other secret"},
		// Issue #123: names the old list let through.
		"oidc secret":  {"x", config.ModelProfile{"provider": "openai-compatible", "model": "m", "base_url": "https://evil.example", "api_key_env": "OIDC_CLIENT_SECRET"}, "no other secret"},
		"aws":          {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "AWS_SECRET_ACCESS_KEY"}, "no other secret"},
		"postgres":     {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "PGPASSWORD"}, "no other secret"},
		"bare prefix":  {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "CODEREVIEW_MODEL_KEY_"}, "no other secret"},
		"headers":      {"x", config.ModelProfile{"provider": "openai", "model": "m", "headers": map[string]any{"a": "b"}}, "headers"},
		"base url":     {"x", config.ModelProfile{"provider": "openai", "model": "m", "base_url": "ftp://x"}, "base_url"},
		"azure no url": {"x", config.ModelProfile{"provider": "azure", "model": "m"}, "needs base_url"},
	} {
		err := s.Put(ctx, c.name, c.m, "a@x")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	for name, c := range map[string]Choice{
		"no default":    {},
		"unknown":       {Default: "nope"},
		"bad fallback":  {Default: "file", Fallback: []string{"nope"}},
		"default as fb": {Default: "file", Fallback: []string{"file"}},
		"listed twice":  {Default: "file", Fallback: []string{"shared", "shared"}},
	} {
		if err := s.SetChoice(ctx, c, "a@x"); err == nil {
			t.Errorf("choice %s: want an error", name)
		}
	}
}

func TestDeleteInUse(t *testing.T) {
	ctx := context.Background()
	s, st := svc(t)
	_ = s.Put(ctx, "claude", config.ModelProfile{"provider": "anthropic", "model": "c"}, "a@x")
	_ = s.Put(ctx, "spare", config.ModelProfile{"provider": "anthropic", "model": "c"}, "a@x")
	_ = s.Put(ctx, "shared", config.ModelProfile{"provider": "openai", "model": "gpt-y"}, "a@x")
	_ = s.SetChoice(ctx, Choice{Default: "claude", Fallback: []string{"shared"}}, "a@x")

	if err := s.Delete(ctx, "claude", "a@x"); !errors.Is(err, ErrInUse) {
		t.Errorf("the default can't be deleted: %v", err)
	}
	if err := s.Delete(ctx, "shared", "a@x"); !errors.Is(err, ErrInUse) {
		t.Errorf("a fallback can't be deleted: %v", err)
	}
	repo, _ := st.CreateRepo(ctx, "gitlab", "g.example", "team/app", "enabled", "a@x")
	m := "spare"
	_, _ = st.UpdateRepo(ctx, repo.ID, "enabled", config.Overrides{Model: &m}, "a@x")
	if err := s.Delete(ctx, "spare", "a@x"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "team/app") {
		t.Errorf("a profile a repository uses can't be deleted: %v", err)
	}
	if err := s.Delete(ctx, "file", "a@x"); err == nil {
		t.Error("a config file profile isn't in the database")
	}
}

func TestCheckKeyEnvAllowlist(t *testing.T) {
	for _, ok := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "AZURE_OPENAI_API_KEY", "OPENAI_COMPATIBLE_API_KEY", "CODEREVIEW_MODEL_KEY_KIMI"} {
		if err := CheckKeyEnv(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"OIDC_CLIENT_SECRET", "GITLAB_WEBHOOK_SIGNING_TOKEN", "DASHBOARD_TOKEN", "DATABASE_URL", "AWS_SECRET_ACCESS_KEY", "CODEREVIEW_KEY_X", "CODEREVIEW_CONSOLE_SECRET_KEY", "MY_KEY"} {
		if CheckKeyEnv(bad) == nil {
			t.Errorf("%s allowed", bad)
		}
	}
}

func TestKeyTarget(t *testing.T) {
	for _, c := range []struct {
		m       config.ModelProfile
		env     string
		host    string
		unusual bool
	}{
		{config.ModelProfile{"provider": "openai", "model": "m"}, "OPENAI_API_KEY", "", false},
		{config.ModelProfile{"provider": "openai", "model": "m", "base_url": "https://api.openai.com/v1"}, "OPENAI_API_KEY", "api.openai.com", false},
		{config.ModelProfile{"provider": "azure", "model": "m", "base_url": "https://team.openai.azure.com"}, "AZURE_OPENAI_API_KEY", "team.openai.azure.com", false},
		{config.ModelProfile{"provider": "openai-compatible", "model": "m", "base_url": "https://evil.example/v1", "api_key_env": "OPENAI_API_KEY"}, "OPENAI_API_KEY", "evil.example", true},
		{config.ModelProfile{"provider": "anthropic", "model": "m", "base_url": "https://proxy.internal"}, "ANTHROPIC_API_KEY", "proxy.internal", true},
		{config.ModelProfile{"provider": "ollama", "model": "m", "base_url": "http://ollama:11434"}, "", "", false},
	} {
		env, host, unusual := KeyTarget(c.m)
		if env != c.env || host != c.host || unusual != c.unusual {
			t.Errorf("%v: %q %q %v", c.m, env, host, unusual)
		}
	}
}

// A console profile stored before the allowlist with another name gets no
// key, and the Models page and start-up say so (issue #123).
func TestBlockedKeyEnvs(t *testing.T) {
	st := storetest.New()
	ctx := context.Background()
	// Written straight to the store, as an older console could have.
	if err := st.PutModelProfile(ctx, "old", config.ModelProfile{"provider": "openai-compatible", "model": "m", "base_url": "https://evil.example", "api_key_env": "OIDC_CLIENT_SECRET"}, "a@x"); err != nil {
		t.Fatal(err)
	}
	s := New(&config.Config{}, st)
	s.Getenv = func(string) string { return "secret" }
	if keys, _ := s.KeyEnvs(ctx); len(keys) != 0 {
		t.Errorf("the key isn't passed on: %v", keys)
	}
	if b, _ := s.BlockedKeyEnvs(ctx); len(b) != 1 || b[0] != "old: OIDC_CLIENT_SECRET" {
		t.Errorf("blocked: %v", b)
	}
	v, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.Profiles {
		if p.Name == "old" && (p.KeyEnvError == "" || p.KeySet || p.KeyHost != "evil.example") {
			t.Errorf("profile view: %+v", p)
		}
	}
}
