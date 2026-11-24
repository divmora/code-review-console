package expiry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestCredentialWarnings: a credential's token expiring soon is warned
// about like a host's, named after the host and the credential (issue #136).
func TestCredentialWarnings(t *testing.T) {
	soon := time.Now().UTC().Add(5 * 24 * time.Hour).Format("2006-01-02")
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/personal_access_tokens/self" && r.Header.Get("PRIVATE-TOKEN") == "glpat-acme" {
			_, _ = w.Write([]byte(`{"expires_at":"` + soon + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"expires_at":null}`))
	}))
	defer gl.Close()
	box, _ := secrets.New("a test passphrase that is long enough for a key")
	lic := licensetest.Full(t)
	reg := hosts.New(&config.Config{}, storetest.New(), box, nil)
	reg.Lic = lic
	ctx := context.Background()
	if _, err := reg.Init(ctx); err != nil {
		t.Fatal(err)
	}
	h, _ := reg.Save(ctx, hosts.Host{Platform: hosts.GitLab, BaseURL: gl.URL, Name: "GitLab", Enabled: true, Secrets: hosts.Secrets{Token: "glpat-host"}}, "a@x")
	c, err := reg.SaveCredential(ctx, hosts.Credential{HostID: h.ID, Name: "Acme", Pattern: "acme", Enabled: true, CredentialSecrets: hosts.CredentialSecrets{Token: "glpat-acme"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	reg.RefreshTokenExpiry(ctx)
	ws := All(lic.Status(), reg, time.Now())
	var got *Warning
	for i := range ws {
		if ws[i].CredentialID == c.ID {
			got = &ws[i]
		}
	}
	if got == nil || got.HostID != h.ID || got.Name != "GitLab › Acme" || !strings.Contains(got.Message, "The token of GitLab › Acme expires on") {
		t.Fatalf("warnings: %+v", ws)
	}
	for _, w := range ws {
		if w.Kind == "host" && w.CredentialID == 0 && w.HostID == h.ID {
			t.Errorf("the host's own token never expires: %+v", w)
		}
	}
}
