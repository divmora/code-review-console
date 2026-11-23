// Package server receives GitHub and GitLab webhooks and runs reviews and
// chat replies in the background.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/admin"
	"github.com/divmora/code-review-console/internal/clientip"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/expiry"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/models"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/tuning"
	"github.com/divmora/code-review-console/internal/version"
)

// Server handles webhooks.
type Server struct {
	cfg    *config.Config
	exec   executor.Executor
	logger *slog.Logger
	queue  *Queue
	// hosts is the code host registry (admin panel or config file).
	hosts *hosts.Registry
	// idMu guards ghApps and selfLogins, rebuilt when hosts change.
	idMu   sync.RWMutex
	ghApps map[int64]*vcs.GitHubApp // by host ID
	// identities of the bot per web host, to ignore its own comments.
	selfLogins map[string]string
	store      store.Store  // optional: allowlist, overrides, history
	admin      *admin.Admin // optional: admin panel
	// replays is the in-memory fallback for webhook replay keys
	// (see firstDelivery).
	replays deliveries
	// cmdGate limits who runs chat commands and how often.
	cmdGate commandGate
	// hooks saves webhook delivery history (nil without a store).
	hooks *hookWriter
	// settings are the runtime settings (config defaults + admin changes).
	settings *settings.Service
	models   *models.Service
	tuning   *tuning.Service
	// stopPrune ends the review-history cleanup loop.
	stopPrune chan struct{}
	stopOnce  sync.Once
}

// hourlyMetricsKept is how long hourly rollups are kept: only the "last 48
// hours" chart reads them, and daily and monthly rollups are updated
// directly by each job (not derived from hourly rows), so a finished day
// needs no hourly detail. Daily and monthly rollups are kept for good.
const hourlyMetricsKept = 3 * 24 * time.Hour

// pruneLoop deletes review history older than the retention setting, once
// at start and then hourly.
func (s *Server) pruneLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		ret := s.settings.Runtime(ctx).ReviewRetention()
		if n, err := s.store.PruneJobs(ctx, time.Now().Add(-ret), time.Now().Add(-hourlyMetricsKept)); err != nil {
			s.logger.Warn("could not prune review history", "error", err)
		} else if n > 0 {
			s.logger.Info("pruned review history", "deleted", n, "retention", ret.String())
		}
		cancel()
		select {
		case <-s.stopPrune:
			return
		case <-t.C:
		}
	}
}

// Deps are optional components built by the caller.
type Deps struct {
	Store store.Store
	Admin *admin.Admin
	// Settings is shared with the admin panel; nil builds one.
	Settings *settings.Service
	// Hosts is shared with the admin panel; nil builds one from the
	// config file (and the store, without encryption).
	Hosts *hosts.Registry
	// License, when Settings is nil, goes to the settings built here;
	// otherwise the license is Settings.Lic. nil = the free core.
	License *license.Manager
	// Models, when nil, is built here from the config file and Store.
	Models *models.Service
	// Tuning (queue, review engine, bot name, dashboard token) likewise.
	Tuning *tuning.Service
}

// New validates config and builds a server.
func New(cfg *config.Config, exec executor.Executor, logger *slog.Logger, deps Deps) (*Server, error) {
	s := &Server{cfg: cfg, exec: exec, logger: logger, selfLogins: map[string]string{}, ghApps: map[int64]*vcs.GitHubApp{},
		store: deps.Store, admin: deps.Admin, settings: deps.Settings, hosts: deps.Hosts, models: deps.Models}
	if s.models == nil {
		s.models = models.New(cfg, deps.Store)
	}
	if s.tuning = deps.Tuning; s.tuning == nil {
		s.tuning = tuning.New(cfg, deps.Store)
	}
	if s.settings == nil {
		s.settings = settings.New(cfg, deps.Store)
		s.settings.Lic = deps.License
	}
	if cfg.Repos.Allowlist && deps.Store == nil {
		return nil, errors.New("repos.allowlist needs database.url (the allowlist is managed in the admin panel)")
	}
	if s.hosts == nil {
		s.hosts = hosts.New(cfg, deps.Store, nil, logger)
		s.hosts.Lic = s.settings.Lic
		if _, err := s.hosts.Init(context.Background()); err != nil {
			return nil, err
		}
	}
	for _, h := range s.hosts.Enabled("") {
		if err := h.Validate(); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Host, err)
		}
		if s.hosts.Mode() != "database" {
			if err := h.ValidateShared(); err != nil {
				return nil, fmt.Errorf("host %s: %w", h.Host, err)
			}
		}
	}
	if len(s.hosts.Enabled("")) == 0 && (deps.Store == nil || s.hosts.Mode() != "database") {
		return nil, errors.New("nothing to serve: configure github, gitlab and/or bitbucket hosts (or set " + secrets.EnvKey + " and add them in the admin panel)")
	}
	// Models can also be managed in the admin UI, so a missing model is only
	// fatal without a database.
	if cfg.Model == "" && len(cfg.Models) != 1 && deps.Store == nil {
		return nil, errors.New("model config: set model: (a profile name or provider:model), or one model profile")
	}
	if err := s.refreshHosts(); err != nil {
		return nil, err
	}
	s.hosts.OnChange = func() {
		if err := s.refreshHosts(); err != nil {
			s.logger.Error("could not apply host changes", "error", err)
		}
	}
	t := s.tuning.Get(context.Background())
	s.queue = NewQueue(t.Workers, t.QueueSize, t.JobTimeoutDuration(), t.History, logger)
	s.tuning.OnChange = s.applyTuning
	if s.store != nil {
		s.queue.OnFinish = s.persistJob
		s.loadHistory()
		s.hooks = newHookWriter(s.store, func() time.Duration { return s.settings.Runtime(context.Background()).WebhookRetention() }, logger)
		s.stopPrune = make(chan struct{})
		go s.pruneLoop()
		go s.tuningLoop()
		go s.expiryLoop()
	}
	return s, nil
}

// applyTuning resizes the queue to the saved settings.
func (s *Server) applyTuning(t tuning.Settings) {
	if t.Workers != s.queue.Workers() {
		s.logger.Info("reviews at once changed", "workers", t.Workers)
	}
	s.queue.Resize(t.Workers, t.QueueSize, t.JobTimeoutDuration(), t.History)
}

// expiryLoop checks the code host tokens' expiry at start and once a day,
// and logs a warning for the license and each token that expires within
// two weeks (or has expired).
func (s *Server) expiryLoop() {
	// The first check waits a minute, so starting up (and tests) makes no
	// calls to the code hosts.
	select {
	case <-s.stopPrune:
		return
	case <-time.After(time.Minute):
	}
	tk := time.NewTicker(24 * time.Hour)
	defer tk.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		s.hosts.RefreshTokenExpiry(ctx)
		cancel()
		for _, w := range expiry.All(s.lic().Status(), s.hosts, time.Now()) {
			s.logger.Warn(w.Message, "kind", w.Kind, "name", w.Name, "expires", w.Expires.UTC().Format(time.RFC3339), "days_left", w.Days)
		}
		select {
		case <-s.stopPrune:
			return
		case <-tk.C:
		}
	}
}

// tuningLoop picks up settings saved on another instance.
func (s *Server) tuningLoop() {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-s.stopPrune:
			return
		case <-tk.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.applyTuning(s.tuning.Get(ctx))
			cancel()
		}
	}
}

// refreshHosts rebuilds GitHub App clients and learns each host's token
// user (the bot's identity), after startup and whenever hosts change.
// Identity lookups that fail are logged, not fatal.
func (s *Server) refreshHosts() error {
	apps := map[int64]*vcs.GitHubApp{}
	logins := map[string]string{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, h := range s.hosts.Enabled("") {
		switch h.Platform {
		case hosts.GitHub:
			if h.AppID != 0 {
				key, err := h.AppKey()
				if err != nil {
					return fmt.Errorf("github %s: %w", h.Host, err)
				}
				a, err := vcs.NewGitHubApp(h.AppID, key, h.BaseURL)
				if err != nil {
					return fmt.Errorf("github %s: %w", h.Host, err)
				}
				apps[h.ID] = a
				continue // the App's identity is its slug; set bot_name
			}
		}
		c := s.apiClient(h)
		if c == nil {
			continue
		}
		login, err := c.CurrentUser(ctx)
		if err != nil || login == "" {
			s.logger.Warn("could not read the token user", "platform", h.Platform, "host", h.Host, "error", err)
			continue
		}
		logins[h.Host] = login
		s.logger.Info(h.Platform+" identity", "host", h.Host, "token_user", login,
			"reviewer", firstNonEmpty(h.Reviewer, login), "mention", "@"+firstNonEmpty(h.BotName, s.tuning.Get(ctx).BotName, login))
	}
	s.idMu.Lock()
	s.ghApps, s.selfLogins = apps, logins
	s.idMu.Unlock()
	return nil
}

// userClient is the part of a code host client used for identity.
type userClient interface {
	CurrentUser(ctx context.Context) (string, error)
}

// apiClient returns a token client for a host (nil for GitHub Apps).
func (s *Server) apiClient(h hosts.Host) userClient {
	switch h.Platform {
	case hosts.GitHub:
		if h.Token == "" {
			return nil
		}
		return vcs.NewGitHub(h.BaseURL, vcs.StaticToken(h.Token))
	case hosts.GitLab:
		return vcs.NewGitLab(h.BaseURL, h.Token, h.SkipTLSVerify)
	case hosts.Bitbucket:
		return vcs.NewBitbucket(h.BaseURL, vcs.BitbucketAuth{Token: h.Token, Email: h.Email, APIToken: h.APIToken})
	}
	return nil
}

// self returns the bot's login on a web host ("" when unknown).
func (s *Server) self(host string) string {
	s.idMu.RLock()
	defer s.idMu.RUnlock()
	return s.selfLogins[host]
}

func (s *Server) ghApp(hostID int64) *vcs.GitHubApp {
	s.idMu.RLock()
	defer s.idMu.RUnlock()
	return s.ghApps[hostID]
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	// Every request carries its client (IP, user agent) for the audit log,
	// sessions and webhook history. The config was validated at load.
	resolver, _ := clientip.NewResolver(s.cfg.Server.TrustedProxies)
	mux := http.NewServeMux()
	// The shared URL per platform matches the host from the delivery; a
	// webhook added in the admin panel has its own URL (/<platform>/<id>)
	// and secrets.
	for platform, h := range map[string]http.HandlerFunc{"github": s.handleGitHub, "gitlab": s.handleGitLab, "bitbucket": s.handleBitbucket} {
		mux.HandleFunc("POST /webhooks/"+platform, s.recordHooks(platform, h))
		mux.HandleFunc("POST /webhooks/"+platform+"/{id}", s.recordHooks(platform, h))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		p, run := s.queue.Stats()
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.Version, "pending": p, "running": run})
	})
	// The dashboard can be switched on and off from the admin console, so
	// its routes always exist and check the setting per request.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !s.settings.Runtime(r.Context()).DashboardEnabled {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})
	mux.HandleFunc("GET /dashboard", s.handleDashboardPage)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	if s.admin != nil {
		// Admins-only dashboard: under /admin, where the session cookie applies.
		mux.HandleFunc("GET /admin/dashboard", s.handleAdminDashboardPage)
		mux.HandleFunc("GET /admin/api/status", s.handleAdminStatus)
	}
	if s.admin != nil {
		s.admin.Register(mux)
	}
	return resolver.Middleware(mux)
}

// ListenAndServe runs until ctx is cancelled, then drains jobs.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Server.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errc := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.cfg.Server.Addr, "version", version.Version)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.logger.Info("shutting down; waiting for running reviews")
	shCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = srv.Shutdown(shCtx)
	s.queue.Shutdown(shCtx)
	s.Close()
	return nil
}

// overrides are the admin settings passed to a job (nil without a store,
// so the CLI uses the repository's own config only).
func (s *Server) overrides(acc store.Access) *config.Overrides {
	if s.store == nil {
		return nil
	}
	o := acc.Settings
	return &o
}

// Close flushes the webhook history. ListenAndServe calls it on shutdown.
func (s *Server) Close() {
	if s.hooks != nil {
		s.hooks.close()
	}
	if s.stopPrune != nil {
		s.stopOnce.Do(func() { close(s.stopPrune) })
	}
}

// seen records a repository that sent a verified webhook, whatever the
// event, so admins can enable it later. New repositories start as inherit.
func (s *Server) seen(ctx context.Context, platform, host, path string) {
	if s.store == nil || path == "" {
		return
	}
	if _, err := s.store.TouchRepo(ctx, platform, host, path); err != nil {
		s.logger.Warn("could not record repository", "repo", platform+":"+host+"/"+path, "error", err)
	}
}

// meta builds the dashboard description of a job.
func meta(ref vcs.Ref, kind, trigger string) Meta {
	return Meta{Kind: kind, Platform: ref.Platform, Host: ref.Host, Repo: ref.Repo, Number: ref.Number, URL: webURL(ref), Trigger: trigger}
}

// gate decides whether a repository is reviewed and returns its admin
// settings: the repository's own mode wins, then the most specific group or
// pattern rule, then the allowlist default. It records unknown
// repositories so admins can see them. With the allowlist on, database
// errors fail closed.
func (s *Server) gate(ctx context.Context, ref vcs.Ref) (bool, *config.Overrides) {
	acc := s.access(ctx, ref)
	if !acc.Enabled {
		return false, nil
	}
	if s.store == nil {
		return true, nil
	}
	return true, &acc.Settings
}

// access resolves a repository's effective state across levels: global
// settings, host-wide rule, group/pattern rules, repository. Without a
// database every repository is reviewed with the global settings; on a
// database error the allowlist decides (fail closed when it is on).
func (s *Server) access(ctx context.Context, ref vcs.Ref) store.Access {
	eff := s.settings.Runtime(ctx)
	base := eff.Base()
	if s.store == nil {
		return store.Resolve(store.Repo{Platform: ref.Platform, Host: ref.Host, Path: ref.Repo, Mode: store.ModeInherit}, nil, base)
	}
	fallback := store.Access{Enabled: !eff.Allowlist, Source: "database error", RequireReviewer: eff.RequireReviewer, SkipBranchSync: eff.SkipBranchSync}
	repo, err := s.store.TouchRepo(ctx, ref.Platform, ref.Host, ref.Repo)
	if err != nil {
		s.logger.Error("repository lookup failed", "repo", ref.String(), "error", err)
		return fallback
	}
	rules, err := s.store.ListRules(ctx)
	if err != nil {
		s.logger.Error("rule lookup failed", "repo", ref.String(), "error", err)
		return fallback
	}
	if !s.lic().Has(license.Rules) {
		rules = nil // group and pattern rules are a paid feature
	}
	acc := store.Resolve(*repo, rules, base)
	if !acc.Enabled {
		s.logger.Info("repository not enabled", "repo", ref.String(), "source", acc.Source)
	}
	return acc
}

// lic is the installed license (nil = the free core).
func (s *Server) lic() *license.Manager { return s.settings.Lic }

func (s *Server) persistJob(r Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.store.InsertJob(ctx, store.Job{
		Kind: r.Kind, Platform: r.Platform, Host: r.Host, Repo: r.Repo, Number: r.Number, URL: r.URL, Trigger: r.Trigger,
		State: string(r.State), Enqueued: r.Enqueued, Started: r.Started, Finished: r.Finished, Merged: r.Merged,
		Error: r.Error, Note: r.Note, Comments: r.Comments, Files: r.Files, TokensIn: r.TokensIn, TokensOut: r.TokensOut, Model: r.Model,
		Result: r.Result, RequestedBy: r.RequestedBy,
	})
	if err != nil {
		s.logger.Warn("could not save job history", "error", err)
	}
}

func (s *Server) loadHistory() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobs, err := s.store.RecentJobs(ctx, s.tuning.Get(ctx).History)
	if err != nil {
		s.logger.Warn("could not load job history", "error", err)
		return
	}
	recs := make([]Record, 0, len(jobs))
	for i := len(jobs) - 1; i >= 0; i-- { // oldest first
		j := jobs[i]
		recs = append(recs, Record{
			Meta:  Meta{Kind: j.Kind, Platform: j.Platform, Host: j.Host, Repo: j.Repo, Number: j.Number, URL: j.URL, Trigger: j.Trigger},
			State: State(j.State), Enqueued: j.Enqueued, Started: j.Started, Finished: j.Finished, Merged: j.Merged, Error: j.Error,
			Report: Report{Note: j.Note, Comments: j.Comments, Files: j.Files, TokensIn: j.TokensIn, TokensOut: j.TokensOut, Model: j.Model},
		})
	}
	s.queue.Seed(recs)
}

func webURL(ref vcs.Ref) string {
	switch ref.Platform {
	case vcs.GitHub:
		return fmt.Sprintf("https://%s/%s/pull/%d", ref.Host, ref.Repo, ref.Number)
	case vcs.GitLab:
		return fmt.Sprintf("https://%s/%s/-/merge_requests/%d", ref.Host, ref.Repo, ref.Number)
	case vcs.Bitbucket:
		return fmt.Sprintf("https://%s/%s/pull-requests/%d", ref.Host, ref.Repo, ref.Number)
	}
	return ""
}

// ---- chat commands ----

type command struct {
	kind     string // review, full, help, ask
	question string
	// author and sourceRef identify a "remember" command's comment.
	author, sourceRef string
	// authorID is the author's account ID where the host needs it to look
	// up their access (Bitbucket); "" elsewhere.
	authorID string
}

// parseCommand finds "@bot ..." in a comment. ok is false when the bot is
// not mentioned.
func parseCommand(bot, body string) (command, bool) {
	re := regexp.MustCompile(`(?i)(^|[^\w@` + "`" + `])@` + regexp.QuoteMeta(bot) + `(?:\[bot\]|\b)`)
	loc := re.FindStringIndex(body)
	if loc == nil {
		return command{}, false
	}
	rest := strings.TrimSpace(body[loc[1]:])
	first := strings.ToLower(strings.TrimRight(rest, ".!? \n"))
	switch first {
	case "review", "incremental review":
		return command{kind: "review"}, true
	case "full review", "full-review", "review all", "re-review":
		return command{kind: "full"}, true
	case "help", "":
		return command{kind: "help"}, true
	}
	for _, verb := range []string{"remember", "learn", "note"} {
		if low := strings.ToLower(rest); low == verb || strings.HasPrefix(low, verb+" ") || strings.HasPrefix(low, verb+":") {
			return command{kind: "remember", question: strings.TrimLeft(strings.TrimSpace(rest[len(verb):]), ": ")}, true
		}
	}
	q := strings.TrimSpace(body[:loc[0]] + " " + rest)
	return command{kind: "ask", question: q}, true
}

// isBotOutput detects the bot's own comments so it never replies to itself.
func isBotOutput(body string) bool {
	return strings.Contains(body, vcs.SummaryMarker) || strings.Contains(body, "<!-- codereview:")
}

func helpText(bot string) string {
	return fmt.Sprintf(`**Code review bot commands**

| Command | What it does |
|---|---|
| `+"`@%[1]s review`"+` | Review commits pushed since the last review |
| `+"`@%[1]s full review`"+` | Re-review every file from scratch |
| `+"`@%[1]s help`"+` | Show this message |
| `+"`@%[1]s remember <rule>`"+` | Save a team learning for future reviews of this repository |
| `+"`@%[1]s <question>`"+` | Ask anything about this pull request |

Configure reviews with a `+"`.codereview.yaml`"+` file on the default branch (profile, path filters, instructions, auto review).`, bot)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func accepted(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "job": what})
}

func ignored(w http.ResponseWriter, why string) {
	slog.Info("webhook ignored", "reason", why)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": why})
}

func submitErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrQueueFull) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
