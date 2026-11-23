package server

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/tuning"
)

func TestEngineConfigMovesKeysAndPassesThrough(t *testing.T) {
	models := map[string]config.ModelProfile{
		"fast":  {"provider": "bedrock", "model": "m", "region": "us-east-1", "timeout": "90s"},
		"my-oa": {"provider": "openai", "model": "x", "api_key": "sk-SECRET"},
	}
	tn := tuning.Settings{BotName: "codereview", Engine: tuning.Engine{VerifyModel: "fast", Passes: 2, Defaults: map[string]any{"language": "German"}}}
	yml, env, err := EngineConfigWith(tn, models, "fast", []string{"openai:x"})
	if err != nil {
		t.Fatal(err)
	}
	out := string(yml)
	for _, secret := range []string{"sk-SECRET"} {
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
	got := back["models"].(map[string]any)
	if got["my-oa"].(map[string]any)["api_key_env"] != "CODEREVIEW_KEY_MY_OA" {
		t.Errorf("api_key_env not set: %v", got["my-oa"])
	}
	if got["fast"].(map[string]any)["timeout"] != "90s" {
		t.Errorf("unknown profile keys must pass through unchanged:\n%s", out)
	}
	review := back["review"].(map[string]any)
	if review["verify_model"] != "fast" || review["passes"] != 2 || review["defaults"].(map[string]any)["language"] != "German" || review["concurrency"] != nil {
		t.Errorf("review engine settings (unset ones left to the CLI):\n%s", out)
	}
	if back["bot_name"] != "codereview" {
		t.Errorf("bot name:\n%s", out)
	}
	if _, ok := back["github"]; ok {
		t.Errorf("default github api_url should be omitted:\n%s", out)
	}
	if models["my-oa"]["api_key"] != "sk-SECRET" {
		t.Error("building the job config must not modify the console config")
	}
}
