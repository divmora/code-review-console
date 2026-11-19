package server

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-console/internal/config"
)

func TestEngineConfigMovesKeysAndPassesThrough(t *testing.T) {
	cfg := &config.Config{
		Model:    "fast",
		Fallback: []string{"openai:x"},
		Models: map[string]config.ModelProfile{
			"fast":  {"provider": "bedrock", "model": "m", "region": "us-east-1", "timeout": "90s"},
			"my-oa": {"provider": "openai", "model": "x", "api_key": "sk-SECRET"},
		},
		Review:  map[string]any{"verify_model": "fast", "future_key": 7},
		BotName: "codereview",
		GitHub:  config.GitHub{APIURL: "https://api.github.com", Token: "ghp_SECRET", WebhookSecret: "whs"},
	}
	yml, env, err := EngineConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(yml)
	for _, secret := range []string{"sk-SECRET", "ghp_SECRET", "whs"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked into the job config:\n%s", secret, out)
		}
	}
	if env["CODEREVIEW_KEY_MY_OA"] != "sk-SECRET" {
		t.Errorf("api key not moved to env: %v", env)
	}
	var back map[string]any
	if err := yaml.Unmarshal(yml, &back); err != nil {
		t.Fatal(err)
	}
	models := back["models"].(map[string]any)
	if models["my-oa"].(map[string]any)["api_key_env"] != "CODEREVIEW_KEY_MY_OA" {
		t.Errorf("api_key_env not set: %v", models["my-oa"])
	}
	if models["fast"].(map[string]any)["timeout"] != "90s" || back["review"].(map[string]any)["future_key"] != 7 {
		t.Errorf("unknown keys must pass through unchanged:\n%s", out)
	}
	if _, ok := back["github"]; ok {
		t.Errorf("default github api_url should be omitted:\n%s", out)
	}
	if cfg.Models["my-oa"]["api_key"] != "sk-SECRET" {
		t.Error("building the job config must not modify the console config")
	}
}
