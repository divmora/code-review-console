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
	env := map[string]string{"TEAM_CLAUDE_KEY": "sk-ant", "OPENAI_API_KEY": "", "CODEREVIEW_CONSOLE_SECRET_KEY": "top secret"}
	s.Getenv = func(k string) string { return env[k] }
	return s, st
}

func TestPutListAndEffective(t *testing.T) {
	ctx := context.Background()
	s, _ := svc(t)
	if err := s.Put(ctx, "claude", config.ModelProfile{"provider": "anthropic", "model": "claude-sonnet-5-5", "api_key_env": "TEAM_CLAUDE_KEY", "base_url": " "}, "a@x"); err != nil {
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
	if c := by["claude"]; c.Source != "console" || c.KeyEnv != "TEAM_CLAUDE_KEY" || !c.KeySet || c.Config["base_url"] != nil {
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
	if len(keys) != 1 || keys["TEAM_CLAUDE_KEY"] != "sk-ant" {
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
		"smart":   {"provider": "anthropic", "model": "claude-sonnet-5-5", "api_key_env": "TEAM_KEY"},
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
		"console's own": {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "CODEREVIEW_CONSOLE_SECRET_KEY"}, "console's own"},
		"host token":    {"x", config.ModelProfile{"provider": "openai", "model": "m", "api_key_env": "GITLAB_TOKEN"}, "console's own"},
		"headers":       {"x", config.ModelProfile{"provider": "openai", "model": "m", "headers": map[string]any{"a": "b"}}, "headers"},
		"base url":      {"x", config.ModelProfile{"provider": "openai", "model": "m", "base_url": "ftp://x"}, "base_url"},
		"azure no url":  {"x", config.ModelProfile{"provider": "azure", "model": "m"}, "needs base_url"},
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
