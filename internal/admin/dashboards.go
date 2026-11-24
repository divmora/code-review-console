package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/dashboards"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/store"
)

func (a *Admin) dashboards() *dashboards.Service {
	if a.Dashboards == nil {
		a.Dashboards = dashboards.New(a.store, a.legacyDashboard)
	}
	return a.Dashboards
}

// legacyDashboard is the old single dashboard's settings (Settings and the
// queue settings).
func (a *Admin) legacyDashboard(ctx context.Context) dashboards.Legacy {
	return dashboards.LegacyFrom(a.settings().Runtime(ctx), a.tuning().Get(ctx))
}

// syncDefaultDashboard applies a change to the old dashboard settings
// (still on the Settings page) to the default dashboard.
func (a *Admin) syncDefaultDashboard(ctx context.Context, before dashboards.Legacy, actor string) {
	if err := a.dashboards().SyncLegacy(ctx, before, a.legacyDashboard(ctx), actor); err != nil {
		a.logger.Error("could not apply the dashboard settings to the default dashboard", "error", err)
	}
}

// dashboardView is a dashboard as the admin panel sees it: never its token.
type dashboardView struct {
	ID           int64                    `json:"id"`
	Slug         string                   `json:"slug"`
	Title        string                   `json:"title"`
	Description  string                   `json:"description"`
	Access       string                   `json:"access"`
	HideRepos    bool                     `json:"hide_repos"`
	AllRepos     bool                     `json:"all_repos"`
	Enabled      bool                     `json:"enabled"`
	TokenSet     bool                     `json:"token_set"`
	TokenExpires *time.Time               `json:"token_expires,omitempty"`
	TokenExpired bool                     `json:"token_expired,omitempty"`
	Repos        []store.DashboardRepo    `json:"repos"`
	Patterns     []store.DashboardPattern `json:"patterns"`
	// RepoCount is how many repositories the console knows are on it.
	RepoCount int `json:"repo_count"`
	// URL is where it opens: /dashboards/<slug>, or under /admin for one
	// opened by signing in.
	URL       string    `json:"url"`
	Default   bool      `json:"default"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	CreatedBy string    `json:"created_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

// dashboardURL is where d opens.
func dashboardURL(d dashboards.Dashboard) string {
	def := d.Slug == dashboards.DefaultSlug
	switch {
	case dashboards.NeedsSignIn(d) && def:
		return "/admin/dashboard"
	case dashboards.NeedsSignIn(d):
		return "/admin/dashboards/" + d.Slug
	case def:
		return "/dashboard"
	}
	return "/dashboards/" + d.Slug
}

func countCovered(d dashboards.Dashboard, repos []store.Repo) int {
	scope := dashboards.ScopeOf(d)
	n := 0
	for _, r := range repos {
		if scope.Covers(r.Platform, r.Host, r.Path) {
			n++
		}
	}
	return n
}

func viewDashboard(d dashboards.Dashboard, repos []store.Repo) dashboardView {
	v := dashboardView{ID: d.ID, Slug: d.Slug, Title: d.Title, Description: d.Description, Access: d.Access, HideRepos: d.HideRepos,
		AllRepos: d.AllRepos, Enabled: d.Enabled, TokenSet: d.TokenHash != "", Repos: d.Repos, Patterns: d.Patterns,
		RepoCount: countCovered(d, repos), URL: dashboardURL(d), Default: d.Slug == dashboards.DefaultSlug,
		CreatedAt: d.CreatedAt, CreatedBy: d.CreatedBy, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy}
	if !d.TokenExpires.IsZero() {
		t := d.TokenExpires
		v.TokenExpires = &t
		v.TokenExpired = d.TokenHash != "" && !time.Now().Before(t)
	}
	if v.Repos == nil {
		v.Repos = []store.DashboardRepo{}
	}
	if v.Patterns == nil {
		v.Patterns = []store.DashboardPattern{}
	}
	return v
}

// apiListDashboards lists the dashboards: every one, with its settings,
// for an admin; for operators and viewers only those they may open, with
// their title and link.
func (a *Admin) apiListDashboards(w http.ResponseWriter, r *http.Request, s *store.Session) {
	list, err := a.dashboards().List(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	if s.Role != "admin" {
		out := []map[string]string{}
		for _, d := range list {
			if (d.Slug == dashboards.DefaultSlug || a.Lic.Has(license.Metrics)) && dashboards.Opens(d, dashboards.Viewer{Role: s.Role}) {
				out = append(out, map[string]string{"slug": d.Slug, "title": d.Title, "description": d.Description, "url": dashboardURL(d)})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"dashboards": out})
		return
	}
	repos, err := a.store.ListRepos(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	out := []dashboardView{}
	for _, d := range list {
		out = append(out, viewDashboard(d, repos))
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": out})
}

type dashboardReq struct {
	Slug        string                   `json:"slug"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Access      string                   `json:"access"`
	HideRepos   *bool                    `json:"hide_repos"`
	AllRepos    bool                     `json:"all_repos"`
	Enabled     *bool                    `json:"enabled"`
	Repos       []int64                  `json:"repos"`
	Patterns    []store.DashboardPattern `json:"patterns"`
}

func (req dashboardReq) apply(d *dashboards.Dashboard) {
	d.Slug, d.Title, d.Description, d.Access, d.AllRepos = req.Slug, req.Title, req.Description, req.Access, req.AllRepos
	if req.HideRepos != nil {
		d.HideRepos = *req.HideRepos
	}
	if req.Enabled != nil {
		d.Enabled = *req.Enabled
	}
	d.Repos = []store.DashboardRepo{}
	for _, id := range req.Repos {
		d.Repos = append(d.Repos, store.DashboardRepo{ID: id})
	}
	d.Patterns = req.Patterns
}

// apiCreateDashboard adds a dashboard. A public or token dashboard hides
// repository names unless the request shows them on purpose.
func (a *Admin) apiCreateDashboard(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req dashboardReq
	if !decode(w, r, &req) {
		return
	}
	if !a.extraDashboardAllowed(w) {
		return
	}
	d := dashboards.Dashboard{Enabled: true}
	if req.HideRepos == nil && (req.Access == dashboards.Public || req.Access == dashboards.Token) {
		d.HideRepos = true
	}
	req.apply(&d)
	a.saveDashboard(w, r, s, d, http.StatusCreated)
}

func (a *Admin) dashboardByID(w http.ResponseWriter, r *http.Request) (dashboards.Dashboard, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dashboard not found"})
		return dashboards.Dashboard{}, false
	}
	d, ok, err := a.dashboards().ByID(r.Context(), id)
	if err != nil {
		a.serverError(w, err)
		return d, false
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dashboard not found"})
	}
	return d, ok
}

func (a *Admin) apiUpdateDashboard(w http.ResponseWriter, r *http.Request, s *store.Session) {
	d, ok := a.dashboardByID(w, r)
	if !ok {
		return
	}
	var req dashboardReq
	if !decode(w, r, &req) {
		return
	}
	if d.Slug != dashboards.DefaultSlug && !a.extraDashboardAllowed(w) {
		return
	}
	req.apply(&d)
	a.saveDashboard(w, r, s, d, http.StatusOK)
}

func (a *Admin) saveDashboard(w http.ResponseWriter, r *http.Request, s *store.Session, d dashboards.Dashboard, status int) {
	saved, err := a.dashboards().Save(r.Context(), d, s.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dashboard not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin saved dashboard", "by", s.Email, "dashboard", saved.Slug, "access", saved.Access)
	repos, _ := a.store.ListRepos(r.Context())
	writeJSON(w, status, viewDashboard(saved, repos))
}

func (a *Admin) apiDeleteDashboard(w http.ResponseWriter, r *http.Request, s *store.Session) {
	d, ok := a.dashboardByID(w, r)
	if !ok {
		return
	}
	if err := a.dashboards().Delete(r.Context(), d.ID, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin deleted dashboard", "by", s.Email, "dashboard", d.Slug)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// apiRotateDashboardToken gives a token dashboard a new token, valid for
// days (0: never expires). The token is shown once.
func (a *Admin) apiRotateDashboardToken(w http.ResponseWriter, r *http.Request, s *store.Session) {
	d, ok := a.dashboardByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch req.Days {
	case 0, 7, 30, 90, 365:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "days: 7, 30, 90, 365, or 0 for a token that never expires"})
		return
	}
	tok, saved, err := a.dashboards().RotateToken(r.Context(), d.ID, time.Duration(req.Days)*24*time.Hour, s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin rotated a dashboard token", "by", s.Email, "dashboard", saved.Slug, "days", req.Days)
	repos, _ := a.store.ListRepos(r.Context())
	v := viewDashboard(saved, repos)
	writeJSON(w, http.StatusOK, map[string]any{"dashboard": v, "token": tok, "link": v.URL + "?token=" + tok})
}

// apiMatchDashboard counts the known repositories a dashboard being edited
// would show: ?all=1, repo=<id> (repeated) and pattern=<platform>|<host>|<pattern>
// (repeated).
func (a *Admin) apiMatchDashboard(w http.ResponseWriter, r *http.Request, s *store.Session) {
	q := r.URL.Query()
	d := dashboards.Dashboard{AllRepos: q.Get("all") == "1"}
	for _, v := range q["repo"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			d.Repos = append(d.Repos, store.DashboardRepo{ID: id})
		}
	}
	for _, v := range q["pattern"] {
		parts := strings.SplitN(v, "|", 3)
		if len(parts) == 3 {
			d.Patterns = append(d.Patterns, store.DashboardPattern{Platform: parts[0], Host: parts[1], Pattern: parts[2]})
		}
	}
	d.Slug, d.Title, d.Access = "preview", "preview", dashboards.Admins
	if err := dashboards.Normalize(&d); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	repos, err := a.store.ListRepos(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	// Attached repositories are known by ID: fill in their names to match.
	byID := map[int64]store.Repo{}
	for _, x := range repos {
		byID[x.ID] = x
	}
	for i, x := range d.Repos {
		if rr, ok := byID[x.ID]; ok {
			d.Repos[i] = store.DashboardRepo{ID: rr.ID, Platform: rr.Platform, Host: rr.Host, Path: rr.Path}
		}
	}
	scope := dashboards.ScopeOf(d)
	names := []string{}
	for _, x := range repos {
		if scope.Covers(x.Platform, x.Host, x.Path) {
			names = append(names, x.Host+"/"+x.Path)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(names), "repos": names})
}

// extraDashboardAllowed reports whether dashboards besides the default one
// are licensed (feature metrics), writing 402 when not.
func (a *Admin) extraDashboardAllowed(w http.ResponseWriter) bool {
	return a.require(w, license.Metrics)
}
