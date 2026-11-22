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
	// A console profile of the same name replaces the config file one.
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
	if len(v.Profiles) != 3 || v.Default != "file" || v.ChoiceSource != "config" {
		t.Fatalf("view: %+v", v)
	}
	if c := by["claude"]; c.Source != "console" || c.KeyEnv != "TEAM_CLAUDE_KEY" || !c.KeySet || c.Config["base_url"] != nil {
		t.Errorf("claude: %+v", c)
	}
	if sh := by["shared"]; sh.Source != "console" || !sh.Shadows || sh.KeyEnv != "OPENAI_API_KEY" || sh.KeySet {
		t.Errorf("shared (default key env, not set): %+v", sh)
	}
	if f := by["file"]; f.Source != "config" || !f.KeySet || !f.KeyInConfig || f.Config["api_key"] != nil {
		t.Errorf("config profile with a literal key: never shown, counted as set: %+v", f)
	}

	if err := s.SetChoice(ctx, Choice{Default: "claude", Fallback: []string{"shared"}}, "a@x"); err != nil {
		t.Fatal(err)
	}
	models, def, fb, err := s.Effective(ctx)
	if err != nil || def != "claude" || len(fb) != 1 || models["shared"].String("model") != "gpt-y" || models["file"].String("api_key") != "sk-literal" {
		t.Fatalf("effective: %v %s %v %v", models, def, fb, err)
	}
	keys, _ := s.KeyEnvs(ctx)
	if len(keys) != 1 || keys["TEAM_CLAUDE_KEY"] != "sk-ant" {
		t.Errorf("only set key envs of console profiles go to jobs: %v", keys)
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
	// "shared" is a fallback, but the config file profile takes its place.
	if err := s.Delete(ctx, "shared", "a@x"); err != nil {
		t.Errorf("a shadowing profile can go (the config file one is used again): %v", err)
	}
	repo, _ := st.CreateRepo(ctx, "gitlab", "g.example", "team/app", "enabled", "a@x")
	m := "spare"
	_, _ = st.UpdateRepo(ctx, repo.ID, "enabled", config.Overrides{Model: &m}, "a@x")
	if err := s.Delete(ctx, "spare", "a@x"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "team/app") {
		t.Errorf("a profile a repository uses can't be deleted: %v", err)
	}
	if err := s.Delete(ctx, "file", "a@x"); err == nil {
		t.Error("config file profiles aren't deleted from the console")
	}
}
