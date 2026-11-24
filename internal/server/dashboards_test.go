package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/dashboards"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestDashboardsScoped is issue #155's acceptance: each dashboard shows
// only its repositories, opens only for its access, and a token opens only
// its own dashboard.
func TestDashboardsScoped(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "s"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	st := storetest.New()
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ctx := context.Background()
	host := "127.0.0.1:1"
	repo := func(path string) store.DashboardRepo {
		r, err := st.CreateRepo(ctx, "gitlab", host, path, "enabled", "a@x")
		if err != nil {
			t.Fatal(err)
		}
		return store.DashboardRepo{ID: r.ID}
	}
	fms, common, oss := repo("ifmists/fms"), repo("ifmists/common-lib"), repo("oss/tool")
	_ = repo("ifmists/hrms/hrms-ui")

	save := func(d dashboards.Dashboard) dashboards.Dashboard {
		t.Helper()
		d.Enabled = true
		out, err := s.dashboards.Save(ctx, d, "a@x")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	save(dashboards.Dashboard{Slug: "hrms", Title: "HRMS", Access: dashboards.SignedIn,
		Patterns: []store.DashboardPattern{{Platform: "gitlab", Host: host, Pattern: "ifmists/hrms"}}})
	fmsD := save(dashboards.Dashboard{Slug: "fms", Title: "FMS", Access: dashboards.Token, Repos: []store.DashboardRepo{fms, common}})
	other := save(dashboards.Dashboard{Slug: "other", Title: "Other", Access: dashboards.Token, Repos: []store.DashboardRepo{oss}})
	save(dashboards.Dashboard{Slug: "open-source", Title: "Open source", Access: dashboards.Public, Repos: []store.DashboardRepo{oss}})
	fmsTok, _, _ := s.dashboards.RotateToken(ctx, fmsD.ID, 0, "a@x")
	otherTok, _, _ := s.dashboards.RotateToken(ctx, other.ID, 0, "a@x")

	// Finished reviews on every repository, one waiting, and a repository
	// the console hasn't seen before in ifmists/hrms.
	s.queue.mu.Lock()
	for i, p := range []string{"ifmists/fms", "ifmists/common-lib", "oss/tool", "ifmists/hrms/hrms-ui", "ifmists/hrms/brand-new", "secret/payroll"} {
		s.queue.history = append(s.queue.history, Record{ID: int64(i + 1), Meta: Meta{Kind: "review", Platform: "gitlab", Host: host, Repo: p, Number: i + 1},
			State: Done, Started: time.Now().Add(-time.Minute), Finished: time.Now(), Report: Report{Comments: 1}})
	}
	s.queue.pending["secret"] = &pending{rec: &Record{Meta: Meta{Kind: "review", Platform: "gitlab", Host: host, Repo: "secret/payroll"}, State: Pending}}
	s.queue.mu.Unlock()

	role := ""
	s.roleFor = func(*http.Request) string { return role }
	h := s.Handler()
	get := func(path, token, as string) (int, statusResponse, string) {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		role = as
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var resp statusResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp, w.Body.String()
	}
	repos := func(r statusResponse) string {
		var out []string
		for _, x := range r.Recent {
			out = append(out, x.Repo)
		}
		return strings.Join(out, ",")
	}

	code, resp, body := get("/api/dashboards/fms/status", fmsTok, "")
	if code != 200 || repos(resp) != "ifmists/common-lib,ifmists/fms" || resp.Totals.Done != 2 || resp.Totals.Comments != 2 || resp.Title != "FMS" {
		t.Errorf("fms: %d %s", code, body)
	}
	if strings.Contains(body, "payroll") || strings.Contains(body, "oss/tool") || len(resp.Pending) != 0 || resp.WaitingOverall != 1 {
		t.Errorf("fms leaks other repositories, or misses the overall queue: %s", body)
	}
	if code, _, _ := get("/api/dashboards/other/status", fmsTok, ""); code != 401 {
		t.Errorf("fms's token opens another dashboard: %d", code)
	}
	if code, resp, _ := get("/api/dashboards/other/status", otherTok, ""); code != 200 || repos(resp) != "oss/tool" {
		t.Errorf("other: %d %s", code, repos(resp))
	}
	if code, _, _ := get("/api/dashboards/hrms/status", fmsTok, ""); code != 401 {
		t.Errorf("hrms is for signed-in users: %d", code)
	}
	code, resp, body = get("/api/dashboards/open-source/status", "", "")
	if code != 200 || repos(resp) != "oss/tool" || resp.WaitingOverall != 0 || strings.Contains(body, "payroll") {
		t.Errorf("open source (public): %d %s", code, body)
	}
	// Signed in (any role) on /admin: hrms, with the new repository via its pattern.
	code, resp, body = get("/admin/api/dashboards/hrms/status", "", "viewer")
	if code != 200 || repos(resp) != "ifmists/hrms/brand-new,ifmists/hrms/hrms-ui" {
		t.Errorf("hrms for a viewer: %d %s", code, body)
	}
	if code, _, _ := get("/admin/api/dashboards/hrms/status", "", ""); code != 401 {
		t.Errorf("hrms signed out: %d", code)
	}
	// The default dashboard (off by default, as before) keeps every repository.
	def, _, _ := s.dashboards.BySlug(ctx, dashboards.DefaultSlug)
	save(def)
	if code, resp, _ := get("/admin/api/status", "", "admin"); code != 200 || len(resp.Recent) != 6 || resp.WaitingOverall != 0 || len(resp.Pending) != 1 {
		t.Errorf("default: %d %d recent", code, len(resp.Recent))
	}
	// Pages: a token page, a public page, a signed-in page redirect, an unknown one.
	if code, _, _ := get("/dashboards/fms?token="+fmsTok, "", ""); code != 200 {
		t.Errorf("fms page: %d", code)
	}
	req := httptest.NewRequest("GET", "/dashboards/hrms", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/admin/dashboards/hrms" {
		t.Errorf("hrms page redirects to sign in: %d %s", w.Code, w.Header().Get("Location"))
	}
	if code, _, _ := get("/dashboards/nope", "", "admin"); code != 404 {
		t.Errorf("unknown dashboard: %d", code)
	}
	// Without the metrics license feature, only the default dashboard is served.
	s.settings.Lic = licensetest.Manager(t, "hosts")
	if code, _, _ := get("/api/dashboards/open-source/status", "", ""); code != 404 {
		t.Errorf("unlicensed extra dashboard: %d", code)
	}
}

// TestDefaultDashboardKeepsOldToken: on upgrade, /dashboard?token=<old>
// keeps working (issue #155).
func TestDefaultDashboardKeepsOldToken(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "s3cret", "t", "http://127.0.0.1:1"
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Dashboard = config.Dashboard{Enabled: true, Token: "old-token", Title: "Bots"}
	st := storetest.New()
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	if list, _ := st.ListDashboards(context.Background()); len(list) != 1 || list[0].Slug != dashboards.DefaultSlug || list[0].Access != dashboards.Token {
		t.Fatalf("the upgrade stores the default dashboard: %+v", list)
	}
	req := httptest.NewRequest("GET", "/api/status?token=old-token", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"Bots"`) {
		t.Errorf("old token: %d %s", w.Code, w.Body)
	}
}
