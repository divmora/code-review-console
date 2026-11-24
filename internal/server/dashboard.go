package server

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/dashboards"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/version"
)

//go:embed dashboard.html
var dashboardHTML string

var dashboardTmpl = template.Must(template.New("dashboard").Parse(dashboardHTML))

// requestToken is the dashboard token sent with r (?token= or Bearer).
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func securityHeaders(w http.ResponseWriter, nonce string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer") // the token may be in the URL
	csp := "default-src 'none'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if nonce != "" {
		csp += "; script-src 'nonce-" + nonce + "'; style-src 'nonce-" + nonce + "'"
	}
	h.Set("Content-Security-Policy", csp)
}

// dashboard returns the dashboard with slug, or false (404).
func (s *Server) dashboard(r *http.Request, slug string) (dashboards.Dashboard, bool) {
	d, ok, err := s.dashboards.BySlug(r.Context(), slug)
	if err != nil {
		s.logger.Error("dashboards unavailable", "error", err)
		return d, false
	}
	// Dashboards besides the default one are licensed (feature metrics).
	if ok && d.Slug != dashboards.DefaultSlug && !s.settings.Lic.Has(license.Metrics) {
		return d, false
	}
	return d, ok && d.Enabled
}

// slugOf is the dashboard a route serves: /dashboards/{slug}, or the
// default dashboard for the old /dashboard routes.
func slugOf(r *http.Request) string {
	if v := r.PathValue("slug"); v != "" {
		return v
	}
	return dashboards.DefaultSlug
}

// dashboardPaths are a dashboard's page and status URLs, public and admin.
func dashboardPaths(slug string) (page, status, adminPage, adminStatus string) {
	if slug == dashboards.DefaultSlug {
		return "/dashboard", "/api/status", "/admin/dashboard", "/admin/api/status"
	}
	return "/dashboards/" + slug, "/api/dashboards/" + slug + "/status", "/admin/dashboards/" + slug, "/admin/api/dashboards/" + slug + "/status"
}

// handleDashboardPage serves a dashboard to anyone with its token, or to
// anyone when it is public. One opened by signing in is sent to its /admin
// page, where the session cookie applies.
func (s *Server) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.dashboard(r, slugOf(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	page, status, adminPage, _ := dashboardPaths(d.Slug)
	if dashboards.NeedsSignIn(d) {
		http.Redirect(w, r, adminPage, http.StatusFound)
		return
	}
	if tok := requestToken(r); !dashboards.Opens(d, dashboards.Viewer{Token: tok}) {
		msg := `This dashboard needs its token: open <code>` + template.HTMLEscapeString(page) + `?token=…</code>.`
		if dashboards.TokenExpiredFor(d, tok, time.Now()) {
			msg = `This dashboard link has expired. Ask an admin for a new one.`
		}
		securityHeaders(w, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Status dashboard</title>` +
			`<p>` + msg + ` Admins can <a href="` + template.HTMLEscapeString(adminPage) + `">sign in</a> instead.</p>`))
		return
	}
	s.renderDashboard(w, r, d, status)
}

// handleAdminDashboardPage serves a dashboard to signed-in users its
// access lets in (admins see every dashboard).
func (s *Server) handleAdminDashboardPage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.dashboard(r, slugOf(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _, adminPage, adminStatus := dashboardPaths(d.Slug)
	role := s.role(r)
	if role == "" {
		http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(adminPage), http.StatusFound)
		return
	}
	if !dashboards.Opens(d, dashboards.Viewer{Role: role}) {
		securityHeaders(w, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Status dashboard</title><p>This dashboard is for admins.</p>`))
		return
	}
	s.renderDashboard(w, r, d, adminStatus)
}

// role is the signed-in user's role on an /admin route ("" otherwise).
func (s *Server) role(r *http.Request) string {
	if s.roleFor != nil { // tests
		return s.roleFor(r)
	}
	if s.admin == nil {
		return ""
	}
	return s.admin.Role(r)
}

func (s *Server) renderDashboard(w http.ResponseWriter, r *http.Request, d dashboards.Dashboard, statusURL string) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := base64.RawStdEncoding.EncodeToString(b)
	var buf bytes.Buffer
	err := dashboardTmpl.Execute(&buf, map[string]any{
		"Title":      d.Title,
		"Version":    version.Version,
		"Nonce":      nonce,
		"Persistent": s.store != nil,
		"StatusURL":  statusURL,
		// Each dashboard keeps its own token in the tab (one dashboard's
		// token never goes to another).
		"TokenKey": "crb-token:" + d.Slug,
	})
	if err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	securityHeaders(w, nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// statusResponse is what /api/status returns.
type statusResponse struct {
	Title    string `json:"title"`
	Version  string `json:"version"`
	Redacted bool   `json:"redacted"`
	Workers  int    `json:"workers"`
	// BotName is the mention handle when every code host uses the same
	// one, else empty; Mentions has each host's.
	BotName   string    `json:"bot_name"`
	Mentions  []mention `json:"mentions"`
	Debounced string    `json:"debounce"`
	// WaitingOverall is how many jobs wait in the whole queue, when the
	// dashboard shows only some repositories (never on a public one).
	WaitingOverall int `json:"waiting_overall,omitempty"`
	Snapshot
}

// mention is the handle that starts chat commands on one code host
// (@<handle> review). Host is empty when repository names are hidden.
type mention struct {
	Platform string `json:"platform,omitempty"`
	Host     string `json:"host,omitempty"`
	Handle   string `json:"handle"`
}

// mentions are the handles commands are matched against, per enabled
// code host in scope (see Server.mention): the host's bot_name, else the
// default bot name, else the token user the console read at startup. A
// host's credentials (issue #136) can add handles of their own.
func (s *Server) mentions(hide bool, scope dashboards.Scope) (all []mention, common string) {
	seen := map[string]bool{}
	listed := map[string]bool{}
	for _, h := range s.hosts.Enabled("") {
		if !scope.HasHost(h.Platform, h.Host) {
			continue
		}
		handles := []string{s.mention(h)}
		for _, c := range s.hosts.ActiveCredentials(h) {
			handles = append(handles, s.mention(h.WithCredential(c)))
		}
		for _, handle := range handles {
			m := mention{Platform: string(h.Platform), Host: h.Host, Handle: handle}
			if hide {
				if seen[handle] {
					continue
				}
				m.Platform, m.Host = "", ""
			}
			if listed[m.Platform+"|"+m.Host+"|"+handle] {
				continue
			}
			listed[m.Platform+"|"+m.Host+"|"+handle] = true
			seen[handle] = true
			all = append(all, m)
		}
	}
	if len(seen) == 1 {
		common = all[0].Handle
	}
	return all, common
}

// handleStatus is a dashboard's status (/api/status for the default one,
// /api/dashboards/{slug}/status): for anyone with its token, or anyone
// when it is public.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, "")
	d, ok := s.dashboard(r, slugOf(r))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such dashboard, or it is off"})
		return
	}
	_, _, adminPage, _ := dashboardPaths(d.Slug)
	if dashboards.NeedsSignIn(d) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this dashboard is for signed-in users: open " + adminPage})
		return
	}
	if tok := requestToken(r); !dashboards.Opens(d, dashboards.Viewer{Token: tok}) {
		msg := "dashboard token required"
		if dashboards.TokenExpiredFor(d, tok, time.Now()) {
			msg = "this dashboard link has expired: ask an admin for a new one"
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": msg})
		return
	}
	s.writeStatus(w, r, d)
}

// handleAdminStatus is a dashboard's status for signed-in users its access
// lets in.
func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, "")
	d, ok := s.dashboard(r, slugOf(r))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such dashboard, or it is off"})
		return
	}
	role := s.role(r)
	if role == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to the admin panel"})
		return
	}
	if !dashboards.Opens(d, dashboards.Viewer{Role: role}) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this dashboard is for admins"})
		return
	}
	s.writeStatus(w, r, d)
}

// writeStatus writes d's status: the queue filtered to d's repositories on
// the server, so repositories outside it never appear, not even as counts.
func (s *Server) writeStatus(w http.ResponseWriter, r *http.Request, d dashboards.Dashboard) {
	tn := s.tuning.Get(r.Context())
	scope := dashboards.ScopeOf(d)
	all := s.queue.Snapshot()
	snap := all.Filter(func(rec Record) bool { return scope.Covers(rec.Platform, rec.Host, rec.Repo) })
	if d.HideRepos {
		redact(snap.Running)
		redact(snap.Pending)
		redact(snap.Recent)
	}
	ms, common := s.mentions(d.HideRepos, scope)
	resp := statusResponse{
		Title: d.Title, Version: version.Version, Redacted: d.HideRepos,
		Workers: tn.Workers, BotName: common, Mentions: ms, Debounced: tn.DebounceDuration().String(),
		Snapshot: snap,
	}
	if !scope.All() && d.Access != dashboards.Public {
		resp.WaitingOverall = len(all.Pending)
	}
	writeJSON(w, http.StatusOK, resp)
}

// redact removes anything that identifies a repository or leaks details.
func redact(rs []Record) {
	for i := range rs {
		r := &rs[i]
		r.Repo = "private repository"
		r.URL = ""
		r.Number = 0
		r.Scores = nil // a repository's scores are as private as its name
		if r.Error != "" {
			r.Error = "review failed"
		}
		if r.State == Skipped {
			r.Note = "skipped by repository rules"
		}
	}
}
