package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/version"
)

//go:embed admin.html
var adminHTML string

var adminTmpl = template.Must(template.New("admin").Parse(adminHTML))

// Register mounts the admin routes.
func (a *Admin) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", a.handlePage)
	// Each section has its own URL, so a reload stays on it. The page
	// itself picks the section from the path.
	mux.HandleFunc("GET /admin/{section}", func(w http.ResponseWriter, r *http.Request) {
		if !adminSections[r.PathValue("section")] {
			http.NotFound(w, r)
			return
		}
		a.handlePage(w, r)
	})
	mux.HandleFunc("GET /admin/login", a.handleLogin)
	mux.HandleFunc("GET /admin/callback", a.handleCallback)
	mux.HandleFunc("POST /admin/logout", a.handleLogout)
	mux.HandleFunc("GET /admin/api/me", a.requireAPI(a.apiMe))
	mux.HandleFunc("GET /admin/api/repos", a.requireAPI(a.apiListRepos))
	mux.HandleFunc("POST /admin/api/repos", a.requireAPI(a.apiCreateRepo))
	mux.HandleFunc("PUT /admin/api/repos/{id}", a.requireAPI(a.apiUpdateRepo))
	mux.HandleFunc("DELETE /admin/api/repos/{id}", a.requireAPI(a.apiDeleteRepo))
	mux.HandleFunc("GET /admin/api/rules", a.requireAPI(a.apiListRules))
	mux.HandleFunc("POST /admin/api/rules", a.requireAPI(a.apiCreateRule))
	mux.HandleFunc("PUT /admin/api/rules/{id}", a.requireAPI(a.apiUpdateRule))
	mux.HandleFunc("DELETE /admin/api/rules/{id}", a.requireAPI(a.apiDeleteRule))
	mux.HandleFunc("GET /admin/api/webhooks", a.requireAPI(a.apiWebhooks))
	mux.HandleFunc("GET /admin/api/hosts", a.requireAPI(a.apiListHosts))
	mux.HandleFunc("POST /admin/api/hosts", a.requireAPI(a.apiCreateHost))
	mux.HandleFunc("PUT /admin/api/hosts/{id}", a.requireAPI(a.apiUpdateHost))
	mux.HandleFunc("DELETE /admin/api/hosts/{id}", a.requireAPI(a.apiDeleteHost))
	mux.HandleFunc("POST /admin/api/hosts/{id}/test", a.requireAPI(a.apiTestHost))
	mux.HandleFunc("GET /admin/api/settings", a.requireAPI(a.apiGetSettings))
	mux.HandleFunc("PUT /admin/api/settings", a.requireAPI(a.apiPutSettings))
	mux.HandleFunc("GET /admin/api/audit", a.requireAPI(a.apiAudit))
}

// adminSections are the admin UI's pages (see the routes in admin.html).
var adminSections = map[string]bool{"repositories": true, "hosts": true, "rules": true, "webhooks": true, "settings": true, "audit": true}

func (a *Admin) handlePage(w http.ResponseWriter, r *http.Request) {
	s, ok := a.session(r)
	if !ok {
		http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := base64.RawStdEncoding.EncodeToString(b)
	var buf bytes.Buffer
	if err := adminTmpl.Execute(&buf, map[string]any{
		"Nonce": nonce, "CSRF": s.CSRF, "Email": s.Email, "Name": firstNonEmpty(s.Name, s.Email), "Version": version.Version,
	}); err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	noStore(w)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (a *Admin) apiMe(w http.ResponseWriter, r *http.Request, s *store.Session) {
	eff := a.settings().Get(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"email": s.Email, "name": s.Name, "expires": s.Expires, "profiles": a.profiles, "allowlist": eff.Allowlist, "require_reviewer": eff.RequireReviewer,
	})
}

// repoView is a repository plus its effective state.
type repoView struct {
	store.Repo
	Effective store.Access `json:"effective"`
}

func (a *Admin) view(ctx context.Context, repos []store.Repo) ([]repoView, error) {
	rules, err := a.store.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	eff := a.settings().Get(ctx)
	out := make([]repoView, 0, len(repos))
	for _, r := range repos {
		out = append(out, repoView{Repo: r, Effective: store.Resolve(r, rules, eff.Base())})
	}
	return out, nil
}

func (a *Admin) writeRepo(w http.ResponseWriter, r *http.Request, status int, repo *store.Repo) {
	v, err := a.view(r.Context(), []store.Repo{*repo})
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, status, v[0])
}

func (a *Admin) apiListRepos(w http.ResponseWriter, r *http.Request, s *store.Session) {
	repos, err := a.store.ListRepos(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	v, err := a.view(r.Context(), repos)
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var (
	pathRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+(/[A-Za-z0-9_.\-]+)+$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]+)?$`)
)

// validPath accepts owner/repo or group/sub/project, without "." or ".."
// segments.
func validPath(p string) bool {
	if !pathRe.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.Trim(seg, ".") == "" {
			return false
		}
	}
	return true
}

type createReq struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	Mode     string `json:"mode"`
}

// target validates platform and host, filling the default host.
func target(platform, host string) (string, string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if platform != "github" && platform != "gitlab" {
		return "", "", false
	}
	if host == "" {
		host = map[string]string{"github": "github.com", "gitlab": "gitlab.com"}[platform]
	}
	return platform, host, hostRe.MatchString(host)
}

func (a *Admin) apiCreateRepo(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	req.Path = strings.Trim(strings.TrimSpace(req.Path), "/")
	req.Host = strings.ToLower(strings.TrimSpace(req.Host))
	if req.Platform != "github" && req.Platform != "gitlab" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "platform must be github or gitlab"})
		return
	}
	if req.Host == "" {
		req.Host = map[string]string{"github": "github.com", "gitlab": "gitlab.com"}[req.Platform]
	}
	if !hostRe.MatchString(req.Host) || !validPath(req.Path) || (req.Platform == "github" && strings.Count(req.Path, "/") != 1) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "use owner/repo for GitHub or group/[subgroup/]project for GitLab"})
		return
	}
	if req.Mode == "" {
		req.Mode = store.ModeEnabled
	}
	if !store.ValidMode(req.Mode) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be inherit, enabled or disabled"})
		return
	}
	repo, err := a.store.CreateRepo(r.Context(), req.Platform, req.Host, req.Path, req.Mode, s.Email)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.writeRepo(w, r, http.StatusCreated, repo)
}

type updateReq struct {
	Mode     string           `json:"mode"`
	Settings config.Overrides `json:"settings"`
}

func (a *Admin) knownProfiles() map[string]bool {
	profiles := map[string]bool{}
	for _, p := range a.profiles {
		profiles[p] = true
	}
	return profiles
}

func (a *Admin) apiUpdateRepo(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	var req updateReq
	if !decode(w, r, &req) {
		return
	}
	if !store.ValidMode(req.Mode) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be inherit, enabled or disabled"})
		return
	}
	if err := req.Settings.Validate(a.knownProfiles()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	repo, err := a.store.UpdateRepo(r.Context(), id, req.Mode, req.Settings, s.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
		return
	}
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.logger.Info("admin updated repository", "by", s.Email, "repo", repo.Platform+":"+repo.Host+"/"+repo.Path, "mode", repo.Mode)
	a.writeRepo(w, r, http.StatusOK, repo)
}

func (a *Admin) apiDeleteRepo(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	if err := a.store.DeleteRepo(r.Context(), id, s.Email); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
		return
	} else if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) apiListRules(w http.ResponseWriter, r *http.Request, s *store.Session) {
	rules, err := a.store.ListRules(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

type ruleReq struct {
	Platform        string           `json:"platform"`
	Host            string           `json:"host"`
	Pattern         string           `json:"pattern"`
	Enabled         *bool            `json:"enabled"`
	RequireReviewer *bool            `json:"require_reviewer"`
	SkipBranchSync  *bool            `json:"skip_branch_sync"`
	ScoreLabels     *bool            `json:"score_labels"`
	CommitStatus    *bool            `json:"commit_status"`
	Settings        config.Overrides `json:"settings"`
}

func (a *Admin) apiCreateRule(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req ruleReq
	if !decode(w, r, &req) {
		return
	}
	platform, host, ok := target(req.Platform, req.Host)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "platform must be github or gitlab, with a valid host"})
		return
	}
	pattern, err := store.NormalizePattern(req.Pattern)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := req.Settings.Validate(a.knownProfiles()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rule, err := a.store.CreateRule(r.Context(), store.Rule{Platform: platform, Host: host, Pattern: pattern, Enabled: req.Enabled,
		RequireReviewer: req.RequireReviewer, SkipBranchSync: req.SkipBranchSync, ScoreLabels: req.ScoreLabels,
		CommitStatus: req.CommitStatus, Settings: req.Settings}, s.Email)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.logger.Info("admin saved rule", "by", s.Email, "rule", platform+":"+host+"/"+pattern)
	writeJSON(w, http.StatusCreated, rule)
}

type ruleUpdateReq struct {
	Enabled         *bool            `json:"enabled"`
	RequireReviewer *bool            `json:"require_reviewer"`
	SkipBranchSync  *bool            `json:"skip_branch_sync"`
	ScoreLabels     *bool            `json:"score_labels"`
	CommitStatus    *bool            `json:"commit_status"`
	Settings        config.Overrides `json:"settings"`
}

func (a *Admin) apiUpdateRule(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	var req ruleUpdateReq
	if !decode(w, r, &req) {
		return
	}
	if err := req.Settings.Validate(a.knownProfiles()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rule, err := a.store.UpdateRule(r.Context(), id, store.Rule{Enabled: req.Enabled, RequireReviewer: req.RequireReviewer,
		SkipBranchSync: req.SkipBranchSync, ScoreLabels: req.ScoreLabels, CommitStatus: req.CommitStatus, Settings: req.Settings}, s.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
		return
	}
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.logger.Info("admin updated rule", "by", s.Email, "rule", rule.Platform+":"+rule.Host+"/"+rule.Pattern)
	writeJSON(w, http.StatusOK, rule)
}

func (a *Admin) apiDeleteRule(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	if err := a.store.DeleteRule(r.Context(), id, s.Email); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
		return
	} else if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) apiGetSettings(w http.ResponseWriter, r *http.Request, s *store.Session) {
	writeJSON(w, http.StatusOK, a.settings().Get(r.Context()))
}

// settingsReq is the full set of settings; every save writes all of them.
type settingsReq struct {
	Allowlist          bool             `json:"allowlist"`
	RequireReviewer    bool             `json:"require_reviewer"`
	ReviewDefaults     config.Overrides `json:"review_defaults"`
	SkipBranchSync     bool             `json:"skip_branch_sync"`
	SyncSourceBranches []string         `json:"sync_source_branches"`
	ScoreLabels        bool             `json:"score_labels"`
	CommitStatus       bool             `json:"commit_status"`
	DashboardEnabled   bool             `json:"dashboard_enabled"`
	HideRepos          bool             `json:"hide_repos"`
	DashboardTitle     string           `json:"dashboard_title"`
}

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (a *Admin) apiPutSettings(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req settingsReq
	if !decode(w, r, &req) {
		return
	}
	st := settings.Stored{
		Allowlist: &req.Allowlist, RequireReviewer: &req.RequireReviewer, ReviewDefaults: req.ReviewDefaults,
		SkipBranchSync: &req.SkipBranchSync, SyncSourceBranches: cleanList(req.SyncSourceBranches),
		ScoreLabels: &req.ScoreLabels, CommitStatus: &req.CommitStatus,
		DashboardEnabled: &req.DashboardEnabled, HideRepos: &req.HideRepos, DashboardTitle: strings.TrimSpace(req.DashboardTitle),
	}
	if st.DashboardTitle == "" {
		st.DashboardTitle = "Code review"
	}
	if err := st.Validate(a.knownProfiles(), true); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	eff, err := a.settings().Put(r.Context(), st, s.Email)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.logger.Info("admin saved settings", "by", s.Email, "allowlist", eff.Allowlist, "require_reviewer", eff.RequireReviewer,
		"dashboard", eff.DashboardEnabled, "hide_repos", eff.HideRepos)
	writeJSON(w, http.StatusOK, eff)
}

// apiWebhooks lists webhook deliveries, newest first, with counts by
// outcome for the last 24 hours.
func (a *Admin) apiWebhooks(w http.ResponseWriter, r *http.Request, s *store.Session) {
	q := r.URL.Query()
	f := store.WebhookFilter{Platform: q.Get("platform"), Project: strings.TrimSpace(q.Get("project")), Outcome: q.Get("outcome"), Limit: 100}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		f.Limit = n
	}
	if id, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && id > 0 {
		f.BeforeID = id
	}
	events, err := a.store.ListWebhookEvents(r.Context(), f)
	if err != nil {
		a.serverError(w, err)
		return
	}
	counts, err := a.store.CountWebhookEvents(r.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "counts_24h": counts})
}

func (a *Admin) apiAudit(w http.ResponseWriter, r *http.Request, s *store.Session) {
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	entries, err := a.store.ListAudit(r.Context(), limit)
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func (a *Admin) serverError(w http.ResponseWriter, err error) {
	a.logger.Error("admin request failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// ---- code hosts ----

// hostView is a host as the admin UI sees it: settings, which secrets are
// set (never their values), and where to point the webhook.
type hostView struct {
	ID         int64          `json:"id"`
	Platform   string         `json:"platform"`
	Host       string         `json:"host"`
	BaseURL    string         `json:"base_url"`
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	Settings   hosts.Settings `json:"settings"`
	Secrets    hosts.Status   `json:"secrets"`
	WebhookURL string         `json:"webhook_url"`
	Events     []string       `json:"events"`
}

var hookEvents = map[string][]string{
	hosts.GitHub:    {"Pull requests", "Issue comments"},
	hosts.GitLab:    {"Merge request events", "Comments"},
	hosts.Bitbucket: {"Pull request: Created", "Updated", "Merged", "Declined", "Comment created"},
}

func (a *Admin) viewHost(h hosts.Host) hostView {
	return hostView{ID: h.ID, Platform: h.Platform, Host: h.Host, BaseURL: h.BaseURL, Name: h.Name, Enabled: h.Enabled,
		Settings: h.Settings, Secrets: h.SecretStatus(), WebhookURL: a.cfg.BaseURL + "/webhooks/" + h.Platform, Events: hookEvents[h.Platform]}
}

func (a *Admin) apiListHosts(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if a.Hosts == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "none", "hosts": []hostView{}})
		return
	}
	out := []hostView{}
	for _, h := range a.Hosts.All() {
		out = append(out, a.viewHost(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": a.Hosts.Mode(), "hosts": out, "key_env": secrets.EnvKey})
}

// hostReq creates or updates a host. Secret fields: absent (null) keeps
// the current value, "" or [] clears it, anything else replaces it.
type hostReq struct {
	Platform       string         `json:"platform"`
	BaseURL        string         `json:"base_url"`
	Name           string         `json:"name"`
	Enabled        *bool          `json:"enabled"`
	Settings       hosts.Settings `json:"settings"`
	Token          *string        `json:"token"`
	AppPrivateKey  *string        `json:"app_private_key"`
	APIToken       *string        `json:"api_token"`
	WebhookSecrets *[]string      `json:"webhook_secrets"`
	SigningTokens  *[]string      `json:"signing_tokens"`
}

func (req hostReq) apply(h *hosts.Host) {
	if req.BaseURL != "" {
		h.BaseURL = req.BaseURL
	}
	h.Name = strings.TrimSpace(req.Name)
	if req.Enabled != nil {
		h.Enabled = *req.Enabled
	}
	h.Settings = req.Settings
	if req.Token != nil {
		h.Token = strings.TrimSpace(*req.Token)
	}
	if req.AppPrivateKey != nil {
		h.AppPrivateKey = strings.TrimSpace(*req.AppPrivateKey)
	}
	if req.APIToken != nil {
		h.APIToken = strings.TrimSpace(*req.APIToken)
	}
	if req.WebhookSecrets != nil {
		h.WebhookSecrets = *req.WebhookSecrets
	}
	if req.SigningTokens != nil {
		h.SigningTokens = *req.SigningTokens
	}
}

func (a *Admin) saveHost(w http.ResponseWriter, r *http.Request, s *store.Session, h hosts.Host, status int) {
	saved, err := a.Hosts.Save(r.Context(), h, s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin saved host", "by", s.Email, "platform", saved.Platform, "host", saved.Host, "enabled", saved.Enabled)
	if fresh, ok := a.Hosts.Get(saved.ID); ok {
		saved = fresh
	}
	writeJSON(w, status, a.viewHost(saved))
}

func (a *Admin) apiCreateHost(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if a.Hosts == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "hosts are not available"})
		return
	}
	var req hostReq
	if !decode(w, r, &req) {
		return
	}
	h := hosts.Host{Platform: req.Platform, Enabled: true}
	req.apply(&h)
	if err := h.Normalize(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	for _, x := range a.Hosts.All() {
		if x.Platform == h.Platform && x.Host == h.Host {
			writeJSON(w, http.StatusConflict, map[string]string{"error": h.Platform + " host " + h.Host + " already exists; edit it instead"})
			return
		}
	}
	a.saveHost(w, r, s, h, http.StatusCreated)
}

func (a *Admin) hostByID(w http.ResponseWriter, r *http.Request) (hosts.Host, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || a.Hosts == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return hosts.Host{}, false
	}
	h, ok := a.Hosts.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not found"})
	}
	return h, ok
}

func (a *Admin) apiUpdateHost(w http.ResponseWriter, r *http.Request, s *store.Session) {
	h, ok := a.hostByID(w, r)
	if !ok {
		return
	}
	var req hostReq
	if !decode(w, r, &req) {
		return
	}
	if req.Platform != "" && req.Platform != h.Platform {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the platform of a host cannot change; add a new host"})
		return
	}
	oldHost := h.Host
	req.apply(&h)
	if err := h.Normalize(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if h.Host != oldHost {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the URL points at another host (" + h.Host + "); add it as a new host"})
		return
	}
	a.saveHost(w, r, s, h, http.StatusOK)
}

func (a *Admin) apiDeleteHost(w http.ResponseWriter, r *http.Request, s *store.Session) {
	h, ok := a.hostByID(w, r)
	if !ok {
		return
	}
	if err := a.Hosts.Delete(r.Context(), h.ID, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// apiTestHost checks a host's credentials by asking who the token belongs to.
func (a *Admin) apiTestHost(w http.ResponseWriter, r *http.Request, s *store.Session) {
	h, ok := a.hostByID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var c interface {
		CurrentUser(context.Context) (string, error)
	}
	switch h.Platform {
	case hosts.GitHub:
		if h.Token == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": h.AppID != 0, "message": "GitHub App: credentials are checked when a webhook arrives (an App has no user)"})
			return
		}
		c = vcs.NewGitHub(h.BaseURL, vcs.StaticToken(h.Token))
	case hosts.GitLab:
		c = vcs.NewGitLab(h.BaseURL, h.Token, h.SkipTLSVerify)
	case hosts.Bitbucket:
		c = vcs.NewBitbucket(h.BaseURL, vcs.BitbucketAuth{Token: h.Token, Email: h.Email, APIToken: h.APIToken})
	}
	user, err := c.CurrentUser(ctx)
	if err != nil {
		msg := err.Error()
		if h.Platform == hosts.Bitbucket && h.Token != "" {
			msg += " (access tokens have no user: set the reviewer name for this host)"
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": user, "message": "connected as @" + user})
}
