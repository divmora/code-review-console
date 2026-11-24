package hosts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestSharedSecretsAreDeleted: a host saved before 0.12 still stores the
// shared webhook URL's secrets; the next start deletes them and says so.
func TestSharedSecretsAreDeleted(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	box := testBox(t)
	pt, _ := json.Marshal(map[string]any{"token": "glpat-x", "webhook_secrets": []string{"old-secret"}, "signing_tokens": []string{randomSigningToken(t)}})
	sealed, _ := box.Seal(pt)
	if _, err := st.PutHost(ctx, store.HostRow{Platform: GitLab, Host: "gitlab.example.com", BaseURL: "https://gitlab.example.com", Enabled: true,
		Config: json.RawMessage(`{}`), Secrets: sealed}, "0.11"); err != nil {
		t.Fatal(err)
	}
	r := New(fileConfig(t), st, box, nil)
	notes, err := r.Init(ctx)
	if err != nil || !strings.Contains(strings.Join(notes, " "), "removed the webhook secrets of the shared") {
		t.Fatalf("init: %v %v", err, notes)
	}
	rows, _ := st.ListHosts(ctx)
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	open, _ := box.Open(rows[0].Secrets)
	if strings.Contains(string(open), "webhook_secrets") || strings.Contains(string(open), "signing_tokens") || !strings.Contains(string(open), "glpat-x") {
		t.Errorf("only the shared URL's secrets are deleted: %s", open)
	}
	// The audit entry says what was deleted, not "no visible change" (issue #133).
	entries, _ := st.ListAudit(ctx, 10)
	var got []string
	for _, e := range entries {
		if e.Actor == "upgrade: shared webhook URL removed" {
			var d struct {
				SecretsChanged []string `json:"secrets_changed"`
			}
			_ = json.Unmarshal(e.Detail, &d)
			got = d.SecretsChanged
		}
	}
	if strings.Join(got, ",") != "webhook_secrets:-1,signing_tokens:-1" {
		t.Errorf("audit secrets_changed = %v", got)
	}
	// Nothing left to delete: the next start says nothing.
	r2 := New(fileConfig(t), st, box, nil)
	if notes, err := r2.Init(ctx); err != nil || strings.Contains(strings.Join(notes, " "), "shared") {
		t.Errorf("second start: %v %v", err, notes)
	}
}

// TestConfigWebhookSecretsNotCopied: the config file's webhook secrets
// were for the shared URL, so they aren't copied.
func TestConfigWebhookSecretsNotCopied(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	box := testBox(t)
	if _, err := New(fileConfig(t), st, box, nil).Init(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.ListHosts(ctx)
	for _, row := range rows {
		if row.Secrets == "" {
			continue
		}
		pt, _ := box.Open(row.Secrets)
		if strings.Contains(string(pt), "webhook_secrets") || strings.Contains(string(pt), "signing_tokens") {
			t.Errorf("%s: %s", row.Host, pt)
		}
	}
}

// TestImportIgnoresSharedSecrets: an export from before 0.12 still
// imports; its host-level webhook secrets are ignored, with a line saying so.
func TestImportIgnoresSharedSecrets(t *testing.T) {
	ctx := context.Background()
	r := New(&config.Config{}, storetest.New(), testBox(t), nil)
	if _, err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := ParseFile([]byte("hosts:\n  - platform: bitbucket\n    token: t\n    webhook_secrets: [old]\n"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Import(ctx, f, "cli", false)
	if err != nil || !strings.Contains(strings.Join(res.Lines, "\n"), "were ignored") {
		t.Fatalf("import: %v %v", err, res.Lines)
	}
}
