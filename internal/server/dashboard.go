package server

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"

	"github.com/divmora/code-review-console/internal/tuning"
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

// signedIn reports whether r comes from a signed-in admin. The session
// cookie is scoped to /admin, so this only holds on /admin routes.
func (s *Server) signedIn(r *http.Request) bool {
	return s.admin != nil && s.admin.SignedIn(r)
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

// handleDashboardPage serves /dashboard to anyone with the token, or to
// anyone when the dashboard is public. When it is for admins only, it
// sends them to /admin/dashboard, where their session cookie applies.
func (s *Server) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if !s.settings.Runtime(r.Context()).DashboardEnabled {
		http.NotFound(w, r)
		return
	}
	t := s.tuning.Get(r.Context())
	if t.Access() == tuning.AccessAdmins {
		http.Redirect(w, r, "/admin/dashboard", http.StatusFound)
		return
	}
	if !t.DashboardOpen(requestToken(r), false) {
		securityHeaders(w, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Status dashboard</title>` +
			`<p>This dashboard needs its token: open <code>/dashboard?token=…</code>. Admins can <a href="/admin/dashboard">sign in</a> instead.</p>`))
		return
	}
	s.renderDashboard(w, r, "/api/status")
}

// handleAdminDashboardPage serves the dashboard to signed-in admins,
// whatever the access setting.
func (s *Server) handleAdminDashboardPage(w http.ResponseWriter, r *http.Request) {
	if !s.settings.Runtime(r.Context()).DashboardEnabled {
		http.NotFound(w, r)
		return
	}
	if !s.signedIn(r) {
		http.Redirect(w, r, "/admin/login?next=/admin/dashboard", http.StatusFound)
		return
	}
	s.renderDashboard(w, r, "/admin/api/status")
}

func (s *Server) renderDashboard(w http.ResponseWriter, r *http.Request, statusURL string) {
	eff := s.settings.Runtime(r.Context())
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := base64.RawStdEncoding.EncodeToString(b)
	var buf bytes.Buffer
	err := dashboardTmpl.Execute(&buf, map[string]any{
		"Title":      eff.DashboardTitle,
		"Version":    version.Version,
		"Nonce":      nonce,
		"Persistent": s.store != nil,
		"StatusURL":  statusURL,
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
	Title     string `json:"title"`
	Version   string `json:"version"`
	Redacted  bool   `json:"redacted"`
	Workers   int    `json:"workers"`
	BotName   string `json:"bot_name"`
	Debounced string `json:"debounce"`
	Snapshot
}

// handleStatus is /api/status: for anyone with the token, or anyone when
// the dashboard is public.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, "")
	tn := s.tuning.Get(r.Context())
	if tn.Access() == tuning.AccessAdmins {
		if s.settings.Runtime(r.Context()).DashboardEnabled {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the dashboard is for signed-in admins: open /admin/dashboard"})
			return
		}
	} else if !tn.DashboardOpen(requestToken(r), false) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "dashboard token required"})
		return
	}
	s.writeStatus(w, r, tn)
}

// handleAdminStatus is /admin/api/status, for signed-in admins.
func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, "")
	if !s.signedIn(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to the admin panel"})
		return
	}
	s.writeStatus(w, r, s.tuning.Get(r.Context()))
}

func (s *Server) writeStatus(w http.ResponseWriter, r *http.Request, tn tuning.Settings) {
	eff := s.settings.Runtime(r.Context())
	if !eff.DashboardEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the status dashboard is off"})
		return
	}
	snap := s.queue.Snapshot()
	if eff.HideRepos {
		redact(snap.Running)
		redact(snap.Pending)
		redact(snap.Recent)
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Title: eff.DashboardTitle, Version: version.Version, Redacted: eff.HideRepos,
		Workers: tn.Workers, BotName: firstNonEmpty(tn.BotName, "codereview"), Debounced: tn.DebounceDuration().String(),
		Snapshot: snap,
	})
}

// redact removes anything that identifies a repository or leaks details.
func redact(rs []Record) {
	for i := range rs {
		r := &rs[i]
		r.Repo = "private repository"
		r.URL = ""
		r.Number = 0
		if r.Error != "" {
			r.Error = "review failed"
		}
		if r.State == Skipped {
			r.Note = "skipped by repository rules"
		}
	}
}
