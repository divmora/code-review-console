package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/expiry"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/version"
)

// ui is the admin single-page app, built from web/ by `make web`.
//
//go:embed all:ui
var uiFS embed.FS

var (
	uiIndex  = mustRead("ui/index.html")
	uiAssets = func() http.Handler {
		sub, err := fs.Sub(uiFS, "ui")
		if err != nil {
			panic(err)
		}
		return http.StripPrefix("/admin/", http.FileServerFS(filesOnly{sub}))
	}()
)

// filesOnly serves files and never lists a directory: opening one fails,
// so the file server answers 404 instead of an index of the bundle.
type filesOnly struct{ fs.FS }

func (f filesOnly) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := file.Stat(); err != nil || st.IsDir() {
		_ = file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}

func mustRead(name string) []byte {
	b, err := uiFS.ReadFile(name)
	if err != nil {
		panic("admin UI not built: " + err.Error() + " (run make web)")
	}
	return b
}

// uiCSP allows only the app's own files: no inline scripts or styles.
const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// pageCSP is uiCSP for the app page, plus a per-request nonce for the one
// <style> tag Radix adds to lock page scroll under dialogs (the page reads
// it from <meta name="csp-nonce">). Inline scripts stay forbidden.
func pageCSP(nonce string) string {
	return strings.Replace(uiCSP, "style-src 'self'", "style-src 'self' 'nonce-"+nonce+"'", 1)
}

// nonceMarker is the placeholder in the built index.html.
var nonceMarker = []byte("__CSP_NONCE__")

// Register mounts the admin routes.
func (a *Admin) Register(mux *http.ServeMux) {
	a.mux = mux // approved changes run through the same routes
	mux.HandleFunc("GET /admin", a.handlePage)
	// /admin/ and /admin/<section>/ (bookmarks, proxies and SSO return URLs
	// add the slash) go to the URL without it.
	mux.HandleFunc("GET /admin/{$}", a.dropSlash)
	for sec := range adminSections {
		mux.HandleFunc("GET /admin/"+sec+"/{$}", a.dropSlash)
	}
	// Each section has its own URL, so a reload stays on it. The page
	// itself picks the section from the path; for an unknown one it shows
	// "page not found" with the sidebar (status 404).
	mux.HandleFunc("GET /admin/{section}", func(w http.ResponseWriter, r *http.Request) {
		if !adminSections[r.PathValue("section")] {
			a.handlePageStatus(w, r, http.StatusNotFound)
			return
		}
		a.handlePage(w, r)
	})
	// Sign-out is POST only (CSRF); a typed or bookmarked GET /admin/logout
	// shows a page with a Sign out button.
	mux.HandleFunc("GET /admin/logout", a.handlePage)
	// Change requests were called Approvals before 0.14 (issue #115).
	mux.HandleFunc("GET /admin/approvals", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/change-requests", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /admin/assets/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", uiCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/admin/assets/index-") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed names
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		uiAssets.ServeHTTP(w, r)
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
	mux.HandleFunc("GET /admin/api/reviews", a.requireAPI(a.apiListReviews))
	mux.HandleFunc("POST /admin/api/reviews", a.requireAPI(a.apiReviewNow))
	mux.HandleFunc("GET /admin/api/reviews/{id}", a.requireAPI(a.apiGetReview))
	mux.HandleFunc("POST /admin/api/reviews/{id}/rerun", a.requireAPI(a.apiRerun))
	mux.HandleFunc("GET /admin/api/metrics", a.requireAPI(a.apiMetrics))
	mux.HandleFunc("GET /admin/api/repos/scores", a.requireAPI(a.apiRepoScores))
	mux.HandleFunc("GET /admin/api/impact", a.requireAPI(a.apiImpact))
	mux.HandleFunc("GET /admin/api/impact/export", a.requireAPI(a.apiImpactExport))
	mux.HandleFunc("GET /admin/api/learnings", a.requireAPI(a.apiListLearnings))
	mux.HandleFunc("POST /admin/api/learnings", a.requireAPI(a.apiCreateLearning))
	mux.HandleFunc("PUT /admin/api/learnings/{id}", a.requireAPI(a.apiUpdateLearning))
	mux.HandleFunc("DELETE /admin/api/learnings/{id}", a.requireAPI(a.apiDeleteLearning))
	mux.HandleFunc("GET /admin/api/hosts", a.requireAPI(a.apiListHosts))
	mux.HandleFunc("POST /admin/api/hosts", a.requireAPI(a.apiCreateHost))
	mux.HandleFunc("PUT /admin/api/hosts/{id}", a.requireAPI(a.apiUpdateHost))
	mux.HandleFunc("DELETE /admin/api/hosts/{id}", a.requireAPI(a.apiDeleteHost))
	mux.HandleFunc("POST /admin/api/hosts/{id}/test", a.requireAPI(a.apiTestHost))
	mux.HandleFunc("POST /admin/api/hosts/{id}/webhooks", a.requireAPI(a.apiCreateWebhook))
	mux.HandleFunc("PUT /admin/api/webhooks/{wid}", a.requireAPI(a.apiUpdateWebhook))
	mux.HandleFunc("DELETE /admin/api/webhooks/{wid}", a.requireAPI(a.apiDeleteWebhook))
	mux.HandleFunc("GET /admin/api/settings", a.requireAPI(a.apiGetSettings))
	mux.HandleFunc("PUT /admin/api/settings", a.requireAPI(a.apiPutSettings))
	mux.HandleFunc("GET /admin/api/tuning", a.requireAPI(a.apiGetTuning))
	mux.HandleFunc("PUT /admin/api/tuning", a.requireAPI(a.apiPutTuning))
	mux.HandleFunc("GET /admin/api/audit", a.requireAPI(a.apiAudit))
	mux.HandleFunc("GET /admin/api/audit/export", a.requireAPI(a.apiAuditExport))
	mux.HandleFunc("GET /admin/api/setup", a.requireAPI(a.apiSetup))
	mux.HandleFunc("GET /admin/api/models", a.requireAPI(a.apiListModels))
	mux.HandleFunc("PUT /admin/api/models/{name}", a.requireAPI(a.apiPutModel))
	mux.HandleFunc("DELETE /admin/api/models/{name}", a.requireAPI(a.apiDeleteModel))
	mux.HandleFunc("PUT /admin/api/model-choice", a.requireAPI(a.apiPutModelChoice))
	mux.HandleFunc("GET /admin/api/sessions", a.requireAPI(a.apiListSessions))
	mux.HandleFunc("DELETE /admin/api/sessions/{id}", a.requireAPI(a.apiDeleteSession))
	mux.HandleFunc("POST /admin/api/sessions/end-others", a.requireAPI(a.apiEndOtherSessions))
	mux.HandleFunc("GET /admin/api/executor", a.requireAPI(a.apiGetExecutor))
	mux.HandleFunc("PUT /admin/api/executor", a.requireAPI(a.apiPutExecutor))
	mux.HandleFunc("GET /admin/api/license", a.requireAPI(a.apiGetLicense))
	mux.HandleFunc("PUT /admin/api/license", a.requireAPI(a.apiPutLicense))
	mux.HandleFunc("GET /admin/api/approvals", a.requireAPI(a.apiListApprovals))
	mux.HandleFunc("POST /admin/api/approvals/{id}/approve", a.requireAPI(a.apiApprove))
	mux.HandleFunc("POST /admin/api/approvals/{id}/reject", a.requireAPI(a.apiReject))
	mux.HandleFunc("POST /admin/api/approvals/{id}/cancel", a.requireAPI(a.apiCancel))
}

// adminSections are the admin UI's pages (see the routes in web/src/App.tsx).
var adminSections = map[string]bool{"setup": true, "repositories": true, "reviews": true, "impact": true, "learnings": true, "hosts": true, "rules": true, "webhooks": true, "settings": true, "audit": true, "license": true, "models": true, "executor": true, "change-requests": true}

// dropSlash redirects to the same URL without the trailing slash, keeping
// the query string.
func (a *Admin) dropSlash(w http.ResponseWriter, r *http.Request) {
	u := *r.URL
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/admin"
	}
	http.Redirect(w, r, u.RequestURI(), http.StatusPermanentRedirect)
}

func (a *Admin) handlePage(w http.ResponseWriter, r *http.Request) {
	a.handlePageStatus(w, r, http.StatusOK)
}

func (a *Admin) handlePageStatus(w http.ResponseWriter, r *http.Request, status int) {
	if _, ok := a.session(r); !ok {
		http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
		return
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "nonce", http.StatusInternalServerError)
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(b)
	noStore(w)
	w.Header().Set("Content-Security-Policy", pageCSP(nonce))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.Replace(uiIndex, nonceMarker, []byte(nonce), 1))
}

func (a *Admin) apiMe(w http.ResponseWriter, r *http.Request, s *store.Session) {
	eff := a.settings().Get(r.Context())
	// Setup progress drives the sidebar badge and the first-run redirect.
	setup := map[string]any{"complete": true}
	if st, err := a.setup(r.Context()); err == nil {
		setup = map[string]any{"complete": st.Complete, "done": st.Done, "total": st.Total}
	} else {
		a.logger.Warn("setup status unavailable", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email": s.Email, "name": firstNonEmpty(s.Name, s.Email), "expires": s.Expires, "csrf": s.CSRF, "version": version.Version, "license": a.Lic.Status(), "free_retention_days": license.FreeRetentionDays, "profiles": a.profileNames(r), "allowlist": eff.Allowlist, "require_reviewer": eff.RequireReviewer,
		"setup": setup, "reviews_paused": a.reviewsPaused(), "role": s.Role, "pending_approvals": a.pendingFor(r.Context(), s), "expiry_warnings": expiry.All(a.Lic.Status(), a.Hosts, time.Now()),
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
	eff := a.settings().Runtime(ctx)
	if !a.Lic.Has(license.Rules) {
		rules = nil // group and pattern rules are a paid feature
	}
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

func (a *Admin) knownProfiles(ctx context.Context) map[string]bool {
	profiles := map[string]bool{}
	names, err := a.models().Names(ctx)
	if err != nil {
		a.logger.Warn("model profiles", "error", err)
	}
	for _, p := range names {
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
	if err := req.Settings.Validate(a.knownProfiles(r.Context())); err != nil {
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
	Approve         *bool            `json:"approve"`
	Settings        config.Overrides `json:"settings"`
}

func (a *Admin) apiCreateRule(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Rules) {
		return
	}
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
	if err := req.Settings.Validate(a.knownProfiles(r.Context())); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rule, err := a.store.CreateRule(r.Context(), store.Rule{Platform: platform, Host: host, Pattern: pattern, Enabled: req.Enabled,
		RequireReviewer: req.RequireReviewer, SkipBranchSync: req.SkipBranchSync, ScoreLabels: req.ScoreLabels,
		CommitStatus: req.CommitStatus, Approve: req.Approve, Settings: req.Settings}, s.Email)
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
	Approve         *bool            `json:"approve"`
	Settings        config.Overrides `json:"settings"`
}

func (a *Admin) apiUpdateRule(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Rules) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	var req ruleUpdateReq
	if !decode(w, r, &req) {
		return
	}
	if err := req.Settings.Validate(a.knownProfiles(r.Context())); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rule, err := a.store.UpdateRule(r.Context(), id, store.Rule{Enabled: req.Enabled, RequireReviewer: req.RequireReviewer,
		SkipBranchSync: req.SkipBranchSync, ScoreLabels: req.ScoreLabels, CommitStatus: req.CommitStatus, Approve: req.Approve, Settings: req.Settings}, s.Email)
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
	Allowlist             bool             `json:"allowlist"`
	RequireReviewer       bool             `json:"require_reviewer"`
	ReviewDefaults        config.Overrides `json:"review_defaults"`
	SkipBranchSync        bool             `json:"skip_branch_sync"`
	SyncSourceBranches    []string         `json:"sync_source_branches"`
	ScoreLabels           bool             `json:"score_labels"`
	CommitStatus          bool             `json:"commit_status"`
	Approve               bool             `json:"approve"`
	ApproveBlock          string           `json:"approve_block"`
	ApproveMinScore       int              `json:"approve_min_score"`
	ApproveWithdraw       *bool            `json:"approve_withdraw"` // nil: on
	ReviewRetentionDays   int              `json:"review_retention_days"`
	LearningsEnabled      bool             `json:"learnings_enabled"`
	DailyTokenBudget      int64            `json:"daily_token_budget"`
	RepoDailyTokenBudget  int64            `json:"repo_daily_token_budget"`
	LearningsAutoActivate bool             `json:"learnings_auto_activate"`
	WebhookRetentionDays  int              `json:"webhook_retention_days"`
	DashboardEnabled      bool             `json:"dashboard_enabled"`
	HideRepos             bool             `json:"hide_repos"`
	DashboardTitle        string           `json:"dashboard_title"`
	CommandAccess         string           `json:"command_access"`
	SessionIdleMinutes    *int             `json:"session_idle_minutes"`
}

func intOr(v, def int) *int {
	if v == 0 {
		v = def
	}
	return &v
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
		Approve: &req.Approve, ApproveBlock: strings.ToLower(strings.TrimSpace(req.ApproveBlock)), ApproveMinScore: &req.ApproveMinScore,
		ApproveWithdraw:  req.ApproveWithdraw,
		LearningsEnabled: &req.LearningsEnabled, LearningsAutoActivate: &req.LearningsAutoActivate,
		DailyTokenBudget: &req.DailyTokenBudget, RepoDailyTokenBudget: &req.RepoDailyTokenBudget,
		ReviewRetentionDays: intOr(req.ReviewRetentionDays, 180), WebhookRetentionDays: intOr(req.WebhookRetentionDays, 14),
		DashboardEnabled: &req.DashboardEnabled, HideRepos: &req.HideRepos, DashboardTitle: strings.TrimSpace(req.DashboardTitle),
		CommandAccess: req.CommandAccess, SessionIdleMinutes: req.SessionIdleMinutes,
	}
	if st.SessionIdleMinutes == nil {
		idle := settings.DefaultSessionIdleMinutes
		st.SessionIdleMinutes = &idle
	}
	if st.DashboardTitle == "" {
		st.DashboardTitle = "Code review"
	}
	if err := st.Validate(a.knownProfiles(r.Context()), true); err != nil {
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

// auditFilter reads the audit log filters from the query string: actor,
// action (host.* for a prefix), target, q (search), from and to (dates or
// RFC 3339 times; a date "to" includes that day), before (an entry ID, to
// page back) and limit.
func auditFilter(r *http.Request) store.AuditFilter {
	q := r.URL.Query()
	f := store.AuditFilter{Actor: strings.TrimSpace(q.Get("actor")), Action: strings.TrimSpace(q.Get("action")),
		Target: strings.TrimSpace(q.Get("target")), Query: strings.TrimSpace(q.Get("q")), From: parseDay(q.Get("from")), Limit: 100}
	if to := parseDay(q.Get("to")); !to.IsZero() {
		if len(q.Get("to")) == 10 {
			to = to.Add(24 * time.Hour)
		}
		f.To = to
	}
	if id, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && id > 0 {
		f.BeforeID = id
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 1000 {
		f.Limit = n
	}
	return f
}

// apiAudit is one page of the audit log, newest first; next is the
// "before" for the following page (0 at the end).
func (a *Admin) apiAudit(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Audit) {
		return
	}
	f := auditFilter(r)
	entries, err := a.store.SearchAudit(r.Context(), f)
	if err != nil {
		a.serverError(w, err)
		return
	}
	var next int64
	if len(entries) == f.Limit {
		next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "next": next})
}

// maxAuditExport bounds one export.
const maxAuditExport = 100000

// apiAuditExport downloads the filtered audit log as CSV or JSON Lines,
// newest first, up to maxAuditExport entries.
func (a *Admin) apiAuditExport(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Audit) {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "jsonl" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format: want csv or jsonl"})
		return
	}
	f := auditFilter(r)
	f.Limit, f.BeforeID = 1000, 0
	name := "audit-" + time.Now().UTC().Format("20060102-150405") + "." + format
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/x-ndjson")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	if format == "csv" {
		_ = cw.Write([]string{"id", "at", "actor", "action", "target", "ip", "user_agent", "detail"})
	}
	enc := json.NewEncoder(w)
	written := 0
	for written < maxAuditExport {
		page, err := a.store.SearchAudit(r.Context(), f)
		if err != nil {
			a.logger.Error("audit export failed", "error", err)
			return // headers are sent: the download ends short
		}
		for _, e := range page {
			if format == "csv" {
				_ = cw.Write([]string{strconv.FormatInt(e.ID, 10), e.At.UTC().Format(time.RFC3339), csvSafe(e.Actor), csvSafe(e.Action), csvSafe(e.Target), e.IP, csvSafe(e.UserAgent), csvSafe(string(e.Detail))})
			} else {
				_ = enc.Encode(e)
			}
		}
		written += len(page)
		if len(page) < f.Limit {
			break
		}
		f.BeforeID = page[len(page)-1].ID
	}
	cw.Flush()
	_ = a.store.Audit(r.Context(), s.Email, "audit.export", "admin", map[string]any{"format": format, "entries": written,
		"filter": map[string]any{"actor": f.Actor, "action": f.Action, "target": f.Target, "q": f.Query}})
}

// csvSafe keeps a spreadsheet from running a value as a formula.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
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
	ID       int64          `json:"id"`
	Platform string         `json:"platform"`
	Host     string         `json:"host"`
	BaseURL  string         `json:"base_url"`
	Name     string         `json:"name"`
	Enabled  bool           `json:"enabled"`
	Settings hosts.Settings `json:"settings"`
	Secrets  hosts.Status   `json:"secrets"`
	// Webhooks have their own URL and secrets (added in the admin panel).
	Webhooks []webhookView `json:"webhooks"`
	Events   []string      `json:"events"`
	// CommentWarning is set when verified deliveries arrive but no comment
	// events do, so @bot commands and learnings would fail silently.
	CommentWarning string `json:"comment_warning,omitempty"`
	// Licensed is false for a host the license does not cover (a second
	// host on a platform without the hosts feature): it is not used.
	Licensed bool `json:"licensed"`
	// TokenExpiry is what the code host says about the token's expiry
	// (nil until it has been checked).
	TokenExpiry *hosts.TokenExpiry `json:"token_expiry,omitempty"`
	// Check is the last Test result (nil until Test runs).
	Check *hosts.Check `json:"check,omitempty"`
}

var hookEvents = map[string][]string{
	hosts.GitHub:    {"Pull requests", "Issue comments", "Pull request review comments"},
	hosts.GitLab:    {"Merge request events", "Comments"},
	hosts.Bitbucket: {"Pull request: Created", "Updated", "Merged", "Declined", "Comment created"},
}

func (a *Admin) viewHost(h hosts.Host, stats map[string]store.HostHookStats) hostView {
	v := hostView{ID: h.ID, Platform: h.Platform, Host: h.Host, BaseURL: h.BaseURL, Name: h.Name, Enabled: h.Enabled,
		Settings: h.Settings, Secrets: h.SecretStatus(), Webhooks: []webhookView{},
		Events: hookEvents[h.Platform], Licensed: !h.Enabled || a.Hosts.Active(h)}
	for _, e := range a.Hosts.Endpoints(h.ID) {
		v.Webhooks = append(v.Webhooks, a.viewWebhook(h.Platform, e))
	}
	if h.Enabled {
		v.CommentWarning = commentWarning(h, stats[h.Platform+":"+h.Host])
	}
	if e, ok := a.Hosts.TokenExpiryOf(h.ID); ok {
		v.TokenExpiry = &e
	}
	if c, ok := a.Hosts.CheckOf(h.ID); ok {
		v.Check = &c
	}
	return v
}

// hookStats returns verified-delivery stats by "platform:host" (empty on
// error: the warnings are a hint, not a reason to fail the page).
func (a *Admin) hookStats(ctx context.Context) map[string]store.HostHookStats {
	out := map[string]store.HostHookStats{}
	list, err := a.store.HookStats(ctx)
	if err != nil {
		a.logger.Warn("webhook stats unavailable", "error", err)
		return out
	}
	for _, s := range list {
		out[s.Platform+":"+s.Host] = s
	}
	return out
}

// commentWarning explains why comment events are missing for a host that
// does receive webhooks: a GitLab system hook never sends them, and other
// hooks need the comment events switched on.
func commentWarning(h hosts.Host, s store.HostHookStats) string {
	if s.Verified == 0 || s.Comments > 0 {
		return ""
	}
	bot := "@" + h.BotName
	if h.BotName == "" {
		bot = "@bot"
	}
	effect := bot + " commands and learnings from replies won't work"
	switch h.Platform {
	case hosts.GitLab:
		if s.SystemHook == s.Verified {
			return "Only system hook deliveries so far. GitLab system hooks don't send comments, so " + effect +
				". Add a group or project webhook with Comments (Manage webhooks gives it its own URL)."
		}
		return "No comment events received yet, so " + effect + ". Turn on Comments on the group or project webhook."
	case hosts.GitHub:
		return "No comment events received yet, so " + effect + ". Subscribe the webhook or GitHub App to Issue comment and Pull request review comment."
	case hosts.Bitbucket:
		return "No comment events received yet, so " + effect + ". Turn on the Comment created trigger on the webhook."
	}
	return ""
}

// webhookView is a webhook endpoint as the admin UI sees it: its URL and
// which secrets are set, never their values.
type webhookView struct {
	ID        string               `json:"id"`
	HostID    int64                `json:"host_id"`
	Name      string               `json:"name"`
	Enabled   bool                 `json:"enabled"`
	URL       string               `json:"url"`
	Secrets   hosts.EndpointStatus `json:"secrets"`
	CreatedAt time.Time            `json:"created_at"`
	CreatedBy string               `json:"created_by"`
	// AcceptSecretToken and Accepts: what this webhook takes ("signature",
	// "signature or secret token", "secret token").
	AcceptSecretToken bool   `json:"accept_secret_token"`
	Accepts           string `json:"accepts"`
}

func (a *Admin) viewWebhook(platform string, e hosts.Endpoint) webhookView {
	v := webhookView{ID: e.ID, HostID: e.HostID, Name: e.Name, Enabled: e.Enabled, URL: a.cfg.BaseURL + e.Path(platform),
		Secrets: e.SecretStatus(), CreatedAt: e.CreatedAt, CreatedBy: e.CreatedBy, AcceptSecretToken: e.AcceptSecretToken, Accepts: "secret token"}
	switch {
	case platform != hosts.GitLab:
		v.Accepts = "signature" // GitHub and Bitbucket sign with the secret
	case e.SignedOnly():
		v.Accepts = "signature"
	case len(e.SigningTokens) > 0:
		v.Accepts = "signature or secret token"
	}
	if h, ok := a.Hosts.Get(e.HostID); ok && h.Platform == hosts.GitLab && h.RequireSignature {
		v.Accepts = "signature" // the host rejects unsigned deliveries
	}
	return v
}

// apiCreateWebhook adds a webhook to a host: a new UUID URL and a generated
// secret, returned once.
func (a *Admin) apiCreateWebhook(w http.ResponseWriter, r *http.Request, s *store.Session) {
	h, ok := a.hostByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	e, secret, err := a.Hosts.AddEndpoint(r.Context(), h.ID, req.Name, s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin added webhook", "by", s.Email, "host", h.Host, "webhook", e.ID, "name", e.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": a.viewWebhook(h.Platform, e), "secret": secret})
}

func (a *Admin) webhookByID(w http.ResponseWriter, r *http.Request) (hosts.Endpoint, hosts.Host, bool) {
	if a.Hosts == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook not found"})
		return hosts.Endpoint{}, hosts.Host{}, false
	}
	e, ok := a.Hosts.Endpoint(r.PathValue("wid"))
	var h hosts.Host
	if ok {
		h, ok = a.Hosts.Get(e.HostID)
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook not found"})
	}
	return e, h, ok
}

// apiUpdateWebhook renames, enables or disables a webhook, sets its GitLab
// signing tokens (absent keeps them, [] clears them), or replaces its
// secret with a new one (returned once).
func (a *Admin) apiUpdateWebhook(w http.ResponseWriter, r *http.Request, s *store.Session) {
	e, h, ok := a.webhookByID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		// AcceptSecretToken: with a signing token, also accept unsigned
		// deliveries with the secret token (for migrating hooks).
		AcceptSecretToken *bool     `json:"accept_secret_token"`
		SigningTokens     *[]string `json:"signing_tokens"`
		RegenerateSecret  bool      `json:"regenerate_secret"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil {
		e.Name = *req.Name
	}
	if req.Enabled != nil {
		e.Enabled = *req.Enabled
	}
	if req.AcceptSecretToken != nil {
		e.AcceptSecretToken = *req.AcceptSecretToken
	}
	if req.SigningTokens != nil {
		e.SigningTokens = *req.SigningTokens
	}
	secret := ""
	if req.RegenerateSecret {
		var err error
		if secret, err = hosts.GenerateSecret(); err != nil {
			a.serverError(w, err)
			return
		}
		e.WebhookSecrets = []string{secret}
	}
	saved, err := a.Hosts.SaveEndpoint(r.Context(), e, s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin updated webhook", "by", s.Email, "host", h.Host, "webhook", e.ID, "new_secret", secret != "")
	out := map[string]any{"webhook": a.viewWebhook(h.Platform, saved)}
	if secret != "" {
		out["secret"] = secret
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Admin) apiDeleteWebhook(w http.ResponseWriter, r *http.Request, s *store.Session) {
	e, h, ok := a.webhookByID(w, r)
	if !ok {
		return
	}
	if err := a.Hosts.DeleteEndpoint(r.Context(), e.ID, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin deleted webhook", "by", s.Email, "host", h.Host, "webhook", e.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) apiListHosts(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if a.Hosts == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "none", "hosts": []hostView{}})
		return
	}
	out := []hostView{}
	stats := a.hookStats(r.Context())
	for _, h := range a.Hosts.All() {
		out = append(out, a.viewHost(h, stats))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": a.Hosts.Mode(), "hosts": out, "key_env": secrets.EnvKey})
}

// hostReq creates or updates a host. Secret fields: absent (null) keeps
// the current value, "" or [] clears it, anything else replaces it.
type hostReq struct {
	Platform      string         `json:"platform"`
	BaseURL       string         `json:"base_url"`
	Name          string         `json:"name"`
	Enabled       *bool          `json:"enabled"`
	Settings      hosts.Settings `json:"settings"`
	Token         *string        `json:"token"`
	AppPrivateKey *string        `json:"app_private_key"`
	APIToken      *string        `json:"api_token"`
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
	if saved.Enabled {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		a.Hosts.CheckTokenExpiry(ctx, saved)
		cancel()
	}
	writeJSON(w, status, a.viewHost(saved, a.hookStats(r.Context())))
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
	if err := a.Hosts.CheckLicense(h); err != nil {
		writeLicenseError(w, err)
		return
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
	oldHost, oldBot, oldReviewer := h.Host, h.BotName, h.Reviewer
	req.apply(&h)
	if (h.BotName != oldBot || h.Reviewer != oldReviewer) && (h.BotName != "" || h.Reviewer != "") && !a.require(w, license.Hosts) {
		return
	}
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
			c := hosts.Check{OK: h.AppID != 0, Summary: "GitHub App", Message: "GitHub App: credentials are checked when a webhook arrives (an App has no user)", At: time.Now()}
			a.Hosts.RecordCheck(h.ID, c)
			writeJSON(w, http.StatusOK, c)
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
		res := hosts.Check{OK: false, Summary: hosts.Summarize(err), Message: msg, At: time.Now()}
		a.Hosts.RecordCheck(h.ID, res)
		writeJSON(w, http.StatusOK, res)
		return
	}
	msg := "connected as @" + user
	e := a.Hosts.CheckTokenExpiry(ctx, h)
	switch {
	case e.Known && e.Expires.IsZero():
		msg += "; the token never expires"
	case e.Known:
		msg += "; the token expires on " + e.Expires.UTC().Format("2 Jan 2006")
		if d := expiry.DaysLeft(e.Expires, time.Now()); d <= 14 {
			msg += " (" + strconv.Itoa(d) + " days left: replace it soon)"
		}
	}
	res := hosts.Check{OK: true, Summary: "OK as @" + user, Message: msg, At: time.Now()}
	a.Hosts.RecordCheck(h.ID, res)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": user, "summary": res.Summary, "message": msg, "at": res.At, "token_expiry": e})
}

// ---- reviews and metrics ----

func parseDay(v string) time.Time {
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

// apiListReviews is the full review history with filters.
func (a *Admin) apiListReviews(w http.ResponseWriter, r *http.Request, s *store.Session) {
	q := r.URL.Query()
	f := store.JobFilter{Platform: q.Get("platform"), Host: q.Get("host"), RepoLike: strings.TrimSpace(q.Get("repo")),
		State: q.Get("state"), Kind: q.Get("kind"), Trigger: q.Get("trigger"), From: parseDay(q.Get("from")), Limit: 100}
	if to := parseDay(q.Get("to")); !to.IsZero() {
		if len(q.Get("to")) == 10 {
			to = to.Add(24 * time.Hour) // a date means "through that day"
		}
		f.To = to
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		f.Limit = n
	}
	if id, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && id > 0 {
		f.BeforeID = id
	}
	// score_below=60&score_kind=security: reviews scoring below 60 on
	// security (score_kind empty or "any": on any of the three).
	if n, err := strconv.Atoi(q.Get("score_below")); err == nil && n > 0 && n <= 101 {
		f.ScoreBelow, f.ScoreKind = n, q.Get("score_kind")
	}
	// severity=critical, category=security: reviews with such a finding
	// (severity=any: with any finding), as counted on the Impact page.
	f.Severity, f.Category = strings.TrimSpace(q.Get("severity")), strings.TrimSpace(q.Get("category"))
	f.Approval = q.Get("approval") // approved, withdrawn, ...
	jobs, err := a.store.ListJobs(r.Context(), f)
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": jobs, "can_review": a.Reviewer != nil})
}

func (a *Admin) apiGetReview(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	j, err := a.store.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "review not found"})
		return
	}
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

type reviewNowReq struct {
	URL  string `json:"url"`
	Full bool   `json:"full"`
}

func (a *Admin) queueReview(w http.ResponseWriter, r *http.Request, s *store.Session, ref vcs.Ref, full bool) {
	if a.Reviewer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reviews cannot be started from this panel"})
		return
	}
	if err := a.Reviewer.ReviewNow(r.Context(), ref, full, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_ = a.store.Audit(r.Context(), s.Email, "review.request", ref.String(), map[string]any{"full": full})
	a.logger.Info("admin requested a review", "by", s.Email, "pr", ref.String(), "full", full)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "pr": ref.String()})
}

// apiReviewNow reviews a pull/merge request by URL.
func (a *Admin) apiReviewNow(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req reviewNowReq
	if !decode(w, r, &req) {
		return
	}
	ref, err := vcs.ParseURL(req.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "paste a pull request or merge request URL: " + err.Error()})
		return
	}
	a.queueReview(w, r, s, ref, req.Full)
}

// apiRerun reviews the pull/merge request of a past review again.
func (a *Admin) apiRerun(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	var req reviewNowReq
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	j, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "review not found"})
		return
	}
	a.queueReview(w, r, s, vcs.Ref{Platform: j.Platform, Host: j.Host, Repo: j.Repo, Number: j.Number}, req.Full)
}

// apiMetrics returns rollup buckets for charts. Defaults: daily, last 30 days.
func (a *Admin) apiMetrics(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Metrics) {
		return
	}
	q := r.URL.Query()
	g := q.Get("granularity")
	span := map[string]time.Duration{store.Hourly: 48 * time.Hour, store.Daily: 30 * 24 * time.Hour, store.Monthly: 365 * 24 * time.Hour}
	if _, ok := span[g]; !ok {
		g = store.Daily
	}
	to := time.Now().UTC()
	from := to.Add(-span[g])
	if t := parseDay(q.Get("from")); !t.IsZero() {
		from = t
	}
	if t := parseDay(q.Get("to")); !t.IsZero() {
		to = t.Add(24 * time.Hour)
	}
	points, err := a.store.Metrics(r.Context(), store.MetricsFilter{Granularity: g, From: from, To: to,
		Platform: q.Get("platform"), Host: q.Get("host"), Repo: q.Get("repo")})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	today, _ := a.store.TokensUsed(r.Context(), time.Now(), "", "", "")
	writeJSON(w, http.StatusOK, map[string]any{"granularity": g, "from": from, "to": to, "points": points,
		"today_tokens": today, "daily_token_budget": a.settings().Get(r.Context()).DailyTokenBudget})
}

// apiRepoScores is each repository's latest review scores and a daily
// trend (30 days by default, ?days= up to 3650).
func (a *Admin) apiRepoScores(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Metrics) {
		return
	}
	days := 30
	if n, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && n > 0 && n <= 3650 {
		days = n
	}
	now := time.Now().UTC()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	scores, err := a.store.RepoScores(r.Context(), since)
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "days": days, "repos": scores})
}

// ---- learnings ----

func (a *Admin) apiListLearnings(w http.ResponseWriter, r *http.Request, s *store.Session) {
	ls, err := a.store.ListLearnings(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"learnings": ls})
}

type learningReq struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Scope    string `json:"scope"`
	Text     string `json:"text"`
	Path     string `json:"path"`
	Status   string `json:"status"`
}

func (req *learningReq) check() error {
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > 600 {
		return errors.New("a learning needs text of at most 600 characters")
	}
	scope, err := store.NormalizePattern(req.Scope)
	if err != nil {
		return err
	}
	req.Scope = scope
	req.Path = strings.TrimSpace(req.Path)
	if req.Path != "" {
		if _, err := path.Match(req.Path, ""); err != nil {
			return fmt.Errorf("path: %w", err)
		}
	}
	switch req.Status {
	case "":
		req.Status = store.LearningActive
	case store.LearningActive, store.LearningPending, store.LearningDisabled:
	default:
		return errors.New("status must be active, pending or disabled")
	}
	return nil
}

func (a *Admin) apiCreateLearning(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Learnings) {
		return
	}
	var req learningReq
	if !decode(w, r, &req) {
		return
	}
	platform, host, ok := target(req.Platform, req.Host)
	if !ok && req.Platform == "bitbucket" {
		platform, host, ok = "bitbucket", firstNonEmpty(strings.ToLower(strings.TrimSpace(req.Host)), "bitbucket.org"), true
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "platform must be github, gitlab or bitbucket, with a valid host"})
		return
	}
	if err := req.check(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	l, err := a.store.CreateLearning(r.Context(), store.Learning{Platform: platform, Host: host, Scope: req.Scope, Text: req.Text,
		Path: req.Path, Status: req.Status, Source: "manual", SourceAuthor: s.Email}, s.Email)
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (a *Admin) apiUpdateLearning(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if !a.require(w, license.Learnings) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	var req learningReq
	if !decode(w, r, &req) {
		return
	}
	if err := req.check(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	l, err := a.store.UpdateLearning(r.Context(), store.Learning{ID: id, Scope: req.Scope, Text: req.Text, Path: req.Path, Status: req.Status}, s.Email)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "learning not found"})
		return
	}
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a *Admin) apiDeleteLearning(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	if err := a.store.DeleteLearning(r.Context(), id, s.Email); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "learning not found"})
		return
	} else if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
