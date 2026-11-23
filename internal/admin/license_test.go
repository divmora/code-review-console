package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
)

// Without a license the paid admin APIs answer 402; a key pasted in the
// panel unlocks them at once, is stored, and is audited without the key.
func TestLicenseGates(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"boss@x.io"}, AllowedDomains: []string{"x.io"}})
	k := licensetest.NewKey(t)
	e.adm.Lic = k.Manager(t)
	e.adm.Hosts.Lic = e.adm.Lic

	// Domain rules need the sso_rules feature: only the exact email signs in.
	e.idp.as(map[string]any{"sub": "y", "email": "dev@x.io", "email_verified": true})
	if resp := e.login(t); resp.StatusCode != http.StatusForbidden {
		t.Errorf("free core: a domain rule must not admit dev@x.io: %d", resp.StatusCode)
	}
	e.idp.as(map[string]any{"sub": "x", "email": "boss@x.io", "email_verified": true})
	if resp := e.login(t); resp.StatusCode != http.StatusOK {
		t.Fatalf("free core: allowed_emails still works: %d", resp.StatusCode)
	}
	csrf := e.csrf(t)

	_, body := e.do(t, "GET", "/admin/api/me", "", nil)
	var me struct {
		License license.Status `json:"license"`
	}
	_ = json.Unmarshal(body, &me)
	if me.License.State != license.None || len(me.License.Features) != 0 {
		t.Errorf("me.license: %+v", me.License)
	}

	paid := []struct{ method, path string }{
		{"GET", "/admin/api/audit"},
		{"GET", "/admin/api/metrics"},
		{"GET", "/admin/api/repos/scores"},
		{"POST", "/admin/api/rules"},
		{"POST", "/admin/api/learnings"},
	}
	for _, p := range paid {
		code, b := e.do(t, p.method, p.path, csrf, map[string]any{"platform": "gitlab", "host": "gitlab.example.com", "pattern": "acme", "text": "x"})
		if code != http.StatusPaymentRequired || !bytes.Contains(b, []byte(`"feature"`)) {
			t.Errorf("free core: %s %s: %d %s", p.method, p.path, code, b)
		}
	}
	// One host per platform is free; a second, or a custom bot name, is not.
	host := func(base, bot string) (int, []byte) {
		return e.do(t, "POST", "/admin/api/hosts", csrf, map[string]any{"platform": "gitlab", "base_url": base, "token": "glpat-x",
			"settings": map[string]any{"bot_name": bot}})
	}
	if code, b := host("https://gitlab.one.example", "custom"); code != http.StatusPaymentRequired {
		t.Errorf("free core: a custom bot name needs the hosts feature: %d %s", code, b)
	}
	if code, b := host("https://gitlab.one.example", ""); code != http.StatusCreated {
		t.Fatalf("free core: the first host is free: %d %s", code, b)
	}
	if code, _ := host("https://gitlab.two.example", ""); code != http.StatusPaymentRequired {
		t.Errorf("free core: a second GitLab host needs the hosts feature: %d", code)
	}

	// Bad keys are refused; a good one unlocks everything.
	if code, _ := e.do(t, "PUT", "/admin/api/license", csrf, map[string]string{"key": "DIV1.nope.nope"}); code != http.StatusBadRequest {
		t.Errorf("garbage key: %d", code)
	}
	key := k.Issue(t, 30*24*time.Hour, "*")
	code, b := e.do(t, "PUT", "/admin/api/license", csrf, map[string]string{"key": key})
	if code != http.StatusOK || !bytes.Contains(b, []byte(`"state":"active"`)) {
		t.Fatalf("install: %d %s", code, b)
	}
	if code, _ := e.do(t, "GET", "/admin/api/audit", "", nil); code != http.StatusOK {
		t.Errorf("licensed: audit: %d", code)
	}
	if code, _ := host("https://gitlab.two.example", "custom"); code != http.StatusCreated {
		t.Errorf("licensed: a second host with a bot name: %d", code)
	}
	var stored StoredLicense
	if ok, _ := e.st.GetSetting(context.Background(), LicenseSetting, &stored); !ok || stored.Key != key {
		t.Error("the pasted key must be stored so it survives a restart")
	}
	entries, _ := e.st.ListAudit(context.Background(), 50)
	for _, a := range entries {
		if a.Target == LicenseSetting && bytes.Contains(mustJSON(a.Detail), []byte("DIV1.")) {
			t.Errorf("the audit log must not hold the license key: %s", mustJSON(a.Detail))
		}
	}

	// A key from the environment cannot be replaced in the panel.
	_ = e.adm.Lic.Install(key, "env")
	if code, _ := e.do(t, "PUT", "/admin/api/license", csrf, map[string]string{"key": ""}); code != http.StatusConflict {
		t.Errorf("managed license: %d", code)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
