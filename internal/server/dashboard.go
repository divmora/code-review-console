package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"strings"

	"github.com/divmora/code-review-console/internal/version"
)

//go:embed dashboard.html
var dashboardHTML string

var dashboardTmpl = template.Must(template.New("dashboard").Parse(dashboardHTML))

// dashboardAuthorized checks the optional dashboard token.
func (s *Server) dashboardAuthorized(r *http.Request) bool {
	want := s.cfg.Server.Dashboard.Token
	if want == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
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

func (s *Server) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	eff := s.settings.Get(r.Context())
	if !eff.DashboardEnabled {
		http.NotFound(w, r)
		return
	}
	if !s.dashboardAuthorized(r) {
		securityHeaders(w, "")
		http.Error(w, "dashboard token required: open /dashboard?token=...", http.StatusUnauthorized)
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := base64.RawStdEncoding.EncodeToString(b)
	var buf bytes.Buffer
	err := dashboardTmpl.Execute(&buf, map[string]any{
		"Title":   eff.DashboardTitle,
		"Version": version.Version,
		"Nonce":   nonce,
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w, "")
	eff := s.settings.Get(r.Context())
	if !eff.DashboardEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the status dashboard is off"})
		return
	}
	if !s.dashboardAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "dashboard token required"})
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
		Workers: s.cfg.Server.Workers, BotName: firstNonEmpty(s.cfg.BotName, "codereview"), Debounced: s.cfg.Server.Debounce.String(),
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
