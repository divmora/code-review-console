package admin

import "testing"

// Issue #123: an operator's model change says where the key would go, for
// the admin approving it.
func TestModelKeyNote(t *testing.T) {
	for body, want := range map[string]string{
		`{"config":{"provider":"openai-compatible","model":"m","base_url":"https://evil.example/v1","api_key_env":"OPENAI_API_KEY"}}`: " (sends OPENAI_API_KEY to evil.example)",
		`{"config":{"provider":"openai","model":"m"}}`:                                        "",
		`{"config":{"provider":"openai","model":"m","base_url":"https://api.openai.com/v1"}}`: "",
		`not json`: "",
	} {
		if got := modelKeyNote([]byte(body)); got != want {
			t.Errorf("%s: %q, want %q", body, got, want)
		}
	}
}
