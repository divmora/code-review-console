package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/hosts"
)

// TestCredentialAPI: credentials are added, listed (never their token),
// tested, shown on the repositories they cover, changed and deleted
// through the API, and need the hosts license feature (issue #136).
func TestCredentialAPI(t *testing.T) {
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/user" && r.Header.Get("PRIVATE-TOKEN") == "glpat-acme":
			_, _ = w.Write([]byte(`{"username":"group_7_bot"}`))
		case r.URL.Path == "/api/v4/personal_access_tokens/self":
			_, _ = w.Write([]byte(`{"expires_at":"2099-01-01"}`))
		default:
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		}
	}))
	defer gl.Close()
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	ctx := context.Background()
	h, err := e.adm.Hosts.Save(ctx, hosts.Host{Platform: hosts.GitLab, BaseURL: gl.URL, Enabled: true, Secrets: hosts.Secrets{Token: "glpat-host"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := e.st.CreateRepo(ctx, "gitlab", h.Host, "acme/app", "enabled", "a@x")
	other, _ := e.st.CreateRepo(ctx, "gitlab", h.Host, "other/app", "enabled", "a@x")

	code, body := e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/credentials", h.ID), csrf, map[string]any{"name": "Acme", "pattern": "Acme", "token": "glpat-acme", "settings": map[string]any{"bot_name": "@acme-bot"}})
	if code != 201 || bytes.Contains(body, []byte("glpat-acme")) || !bytes.Contains(body, []byte(`"pattern":"acme"`)) || !bytes.Contains(body, []byte(`"token":true`)) ||
		!bytes.Contains(body, []byte(`"bot_name":"acme-bot"`)) || !bytes.Contains(body, []byte(`"token_expiry":{"expires":"2099-01-01T00:00:00Z"`)) {
		t.Fatalf("create: %d %s", code, body)
	}
	var cv struct{ ID int64 }
	_ = json.Unmarshal(body, &cv)
	if code, body := e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/credentials", h.ID), csrf, map[string]any{"pattern": "acme", "token": "x"}); code != 400 || !bytes.Contains(body, []byte("already has a credential")) {
		t.Errorf("duplicate: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/credentials", h.ID), csrf, map[string]any{"pattern": "", "token": "x"}); code != 400 {
		t.Errorf("an empty pattern is the host's own token: %d", code)
	}

	_, body = e.do(t, "GET", "/admin/api/hosts", "", nil)
	if bytes.Contains(body, []byte("glpat-acme")) || !bytes.Contains(body, []byte(`"credentials":[{"id":`)) {
		t.Errorf("hosts list: %s", body)
	}
	_, body = e.do(t, "GET", "/admin/api/repos", "", nil)
	var repos []struct {
		ID         int64
		Credential *struct{ ID int64 } `json:"credential"`
	}
	_ = json.Unmarshal(body, &repos)
	for _, r := range repos {
		switch r.ID {
		case repo.ID:
			if r.Credential == nil || r.Credential.ID != cv.ID {
				t.Errorf("acme/app should use the credential: %s", body)
			}
		case other.ID:
			if r.Credential != nil {
				t.Errorf("other/app uses the host's own token: %s", body)
			}
		}
	}

	code, body = e.do(t, "POST", fmt.Sprintf("/admin/api/credentials/%d/test", cv.ID), csrf, nil)
	if code != 200 || !bytes.Contains(body, []byte(`"ok":true`)) || !bytes.Contains(body, []byte(`"user":"group_7_bot"`)) {
		t.Errorf("test: %d %s", code, body)
	}
	// Changing it without a token keeps the stored one.
	code, body = e.do(t, "PUT", fmt.Sprintf("/admin/api/credentials/%d", cv.ID), csrf, map[string]any{"name": "Acme group", "pattern": "acme", "enabled": false})
	if code != 200 || !bytes.Contains(body, []byte(`"enabled":false`)) || !bytes.Contains(body, []byte(`"token":true`)) {
		t.Errorf("update: %d %s", code, body)
	}
	if c, _ := e.adm.Hosts.Credential(cv.ID); c.Token != "glpat-acme" {
		t.Errorf("the token is kept: %q", c.Token)
	}
	// Without the hosts feature: no new credentials.
	e.adm.Lic = licensetest.Manager(t, "metrics")
	if code, _ := e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/credentials", h.ID), csrf, map[string]any{"pattern": "beta", "token": "x"}); code != http.StatusPaymentRequired {
		t.Errorf("unlicensed: %d", code)
	}
	e.adm.Lic = licensetest.Full(t)
	if code, _ := e.do(t, "DELETE", fmt.Sprintf("/admin/api/credentials/%d", cv.ID), csrf, nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := e.do(t, "DELETE", fmt.Sprintf("/admin/api/credentials/%d", cv.ID), csrf, nil); code != 404 {
		t.Errorf("delete again: %d", code)
	}
	entries, _ := e.st.ListAudit(ctx, 50)
	var actions []string
	for _, en := range entries {
		if bytes.Contains(en.Detail, []byte("glpat-acme")) {
			t.Errorf("the audit log has the token: %s", en.Detail)
		}
		actions = append(actions, en.Action)
	}
	if n := countOf(actions, "credential.put"); n < 2 || countOf(actions, "credential.delete") != 1 {
		t.Errorf("audit: %v", actions)
	}
}

func countOf(list []string, v string) int {
	n := 0
	for _, x := range list {
		if x == v {
			n++
		}
	}
	return n
}
