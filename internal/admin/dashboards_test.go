package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/dashboards"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
)

// TestDashboardAPI: admins create, list, change and delete dashboards and
// rotate their tokens; other roles list only the dashboards they may open;
// tokens never reach the audit log (issue #155).
func TestDashboardAPI(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"admin@example.com"}, ViewerEmails: []string{"view@example.com"}})
	as := func(email string) string {
		e.idp.as(map[string]any{"sub": email, "email": email, "email_verified": true})
		_ = e.login(t).Body.Close()
		return e.csrf(t)
	}
	ctx := context.Background()
	if _, err := e.adm.dashboards().EnsureDefault(ctx); err != nil {
		t.Fatal(err)
	}
	r1, _ := e.st.CreateRepo(ctx, "gitlab", "g.example", "ifmists/fms", "enabled", "a@x")
	_, _ = e.st.CreateRepo(ctx, "gitlab", "g.example", "ifmists/hrms/api", "enabled", "a@x")
	_, _ = e.st.CreateRepo(ctx, "gitlab", "g.example", "ifmists/hrms/ui", "enabled", "a@x")
	csrf := as("admin@example.com")

	code, body := e.do(t, "POST", "/admin/api/dashboards", csrf, map[string]any{"slug": "fms", "title": "FMS", "access": "token", "repos": []int64{r1.ID}})
	if code != 201 || !bytes.Contains(body, []byte(`"hide_repos":true`)) || !bytes.Contains(body, []byte(`"repo_count":1`)) || !bytes.Contains(body, []byte(`"url":"/dashboards/fms"`)) {
		t.Fatalf("create (a token dashboard hides names by default): %d %s", code, body)
	}
	var fms struct{ ID int64 }
	_ = json.Unmarshal(body, &fms)
	code, body = e.do(t, "POST", "/admin/api/dashboards", csrf, map[string]any{"slug": "hrms", "title": "HRMS", "access": "signed_in",
		"patterns": []map[string]string{{"platform": "gitlab", "host": "g.example", "pattern": "ifmists/hrms"}}})
	if code != 201 || !bytes.Contains(body, []byte(`"repo_count":2`)) || !bytes.Contains(body, []byte(`"url":"/admin/dashboards/hrms"`)) || !bytes.Contains(body, []byte(`"hide_repos":false`)) {
		t.Fatalf("create hrms: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", "/admin/api/dashboards", csrf, map[string]any{"slug": "fms", "title": "Again", "access": "admins"}); code != 400 {
		t.Errorf("duplicate slug: %d", code)
	}
	code, body = e.do(t, "GET", "/admin/api/dashboards/match?pattern=gitlab|g.example|ifmists&repo="+fmt.Sprint(r1.ID), "", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"count":3`)) {
		t.Errorf("match: %d %s", code, body)
	}

	code, body = e.do(t, "POST", fmt.Sprintf("/admin/api/dashboards/%d/token", fms.ID), csrf, map[string]any{"days": 30})
	var rot struct {
		Token string
		Link  string
	}
	_ = json.Unmarshal(body, &rot)
	if code != 200 || rot.Token == "" || rot.Link != "/dashboards/fms?token="+rot.Token || !bytes.Contains(body, []byte(`"token_set":true`)) {
		t.Fatalf("rotate: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", fmt.Sprintf("/admin/api/dashboards/%d/token", fms.ID), csrf, map[string]any{"days": 12}); code != 400 {
		t.Errorf("odd validity: %d", code)
	}
	// An edit without a token keeps it.
	code, body = e.do(t, "PUT", fmt.Sprintf("/admin/api/dashboards/%d", fms.ID), csrf, map[string]any{"slug": "fms", "title": "FMS team", "access": "token", "repos": []int64{r1.ID}})
	if code != 200 || !bytes.Contains(body, []byte(`"token_set":true`)) {
		t.Errorf("edit: %d %s", code, body)
	}
	if d, _, _ := e.adm.dashboards().BySlug(ctx, "fms"); !dashboards.TokenOK(d, rot.Token, d.UpdatedAt) {
		t.Error("the token survives an edit")
	}

	// Settings still holds the old dashboard fields: a change there reaches the default dashboard.
	if code, _ := e.do(t, "PUT", "/admin/api/settings", csrf, map[string]any{"dashboard_enabled": true, "dashboard_title": "Bots"}); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	if d, _, _ := e.adm.dashboards().BySlug(ctx, dashboards.DefaultSlug); d.Title != "Bots" || !d.Enabled {
		t.Errorf("default after a Settings change: %+v", d)
	}
	_, body = e.do(t, "GET", "/admin/api/dashboards", "", nil)
	var list struct {
		Dashboards []struct {
			ID   int64
			Slug string
		}
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Dashboards) != 3 || list.Dashboards[0].Slug != dashboards.DefaultSlug {
		t.Errorf("admin list: %s", body)
	}
	if code, _ := e.do(t, "DELETE", fmt.Sprintf("/admin/api/dashboards/%d", list.Dashboards[0].ID), csrf, nil); code != 400 {
		t.Errorf("the default dashboard can't be deleted: %d", code)
	}

	// A viewer lists what they may open, with no settings.
	as("view@example.com")
	_, body = e.do(t, "GET", "/admin/api/dashboards", "", nil)
	if !bytes.Contains(body, []byte(`"slug":"hrms"`)) || bytes.Contains(body, []byte(`"slug":"fms"`)) || bytes.Contains(body, []byte("repo_count")) {
		t.Errorf("viewer list: %s", body)
	}

	// Without the metrics feature: no new dashboards.
	csrf = as("admin@example.com")
	e.adm.Lic = licensetest.Manager(t, "hosts")
	if code, _ := e.do(t, "POST", "/admin/api/dashboards", csrf, map[string]any{"slug": "x", "title": "X", "access": "admins"}); code != http.StatusPaymentRequired {
		t.Errorf("unlicensed: %d", code)
	}
	e.adm.Lic = licensetest.Full(t)
	if code, _ := e.do(t, "DELETE", fmt.Sprintf("/admin/api/dashboards/%d", fms.ID), csrf, nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	entries, _ := e.st.ListAudit(ctx, 100)
	actions := map[string]int{}
	for _, en := range entries {
		actions[en.Action]++
		if strings.Contains(string(en.Detail), rot.Token) || strings.Contains(string(en.Detail), dashboards.HashToken(rot.Token)) {
			t.Errorf("the audit log has the token: %s", en.Detail)
		}
	}
	if actions["dashboard.create"] < 2 || actions["dashboard.token.rotate"] != 1 || actions["dashboard.delete"] != 1 {
		t.Errorf("audit: %v", actions)
	}
}
