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
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
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
	// glDeliveries dedupes signed GitLab webhooks by webhook-id;
	// glEvents dedupes the same event arriving through several hooks
	// (system, group and project hooks on one instance).
	glDeliveries deliveries
	glEvents     deliveries
	// hooks saves webhook delivery history (nil without a store).
	hooks *hookWriter
	// settings are the runtime settings (config defaults + admin changes).
	settings *settings.Service
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
}

// New validates config and builds a server.
func New(cfg *config.Config, exec executor.Executor, logger *slog.Logger, deps Deps) (*Server, error) {
	s := &Server{cfg: cfg, exec: exec, logger: logger, selfLogins: map[string]string{}, ghApps: map[int64]*vcs.GitHubApp{},
		store: deps.Store, admin: deps.Admin, settings: deps.Settings, hosts: deps.Hosts}
	if s.settings == nil {
		s.settings = settings.New(cfg, deps.Store)
	}
	if cfg.Repos.Allowlist && deps.Store == nil {
		return nil, errors.New("repos.allowlist needs database.url (the allowlist is managed in the admin panel)")
	}
	if s.hosts == nil {
		s.hosts = hosts.New(cfg, deps.Store, nil, logger)
		if _, err := s.hosts.Init(context.Background()); err != nil {
			return nil, err
		}
	}
	for _, h := range s.hosts.Enabled("") {
		if err := h.Validate(); err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Host, err)
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
	s.queue = NewQueue(cfg.Server.Workers, cfg.Server.QueueSize, cfg.Server.JobTimeout, cfg.Server.Dashboard.History, logger)
	if s.store != nil {
		s.queue.OnFinish = s.persistJob
		s.loadHistory()
		s.hooks = newHookWriter(s.store, cfg.Server.WebhookRetention, logger)
	}
	return s, nil
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
			"reviewer", firstNonEmpty(h.Reviewer, login), "mention", "@"+firstNonEmpty(h.BotName, s.cfg.BotName, login))
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
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/github", s.recordHooks("github", s.handleGitHub))
	mux.HandleFunc("POST /webhooks/gitlab", s.recordHooks("gitlab", s.handleGitLab))
	mux.HandleFunc("POST /webhooks/bitbucket", s.recordHooks("bitbucket", s.handleBitbucket))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		p, run := s.queue.Stats()
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.Version, "pending": p, "running": run})
	})
	// The dashboard can be switched on and off from the admin console, so
	// its routes always exist and check the setting per request.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !s.settings.Get(r.Context()).DashboardEnabled {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})
	mux.HandleFunc("GET /dashboard", s.handleDashboardPage)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	if s.admin != nil {
		s.admin.Register(mux)
	}
	return mux
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
	eff := s.settings.Get(ctx)
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
	acc := store.Resolve(*repo, rules, base)
	if !acc.Enabled {
		s.logger.Info("repository not enabled", "repo", ref.String(), "source", acc.Source)
	}
	return acc
}

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
	jobs, err := s.store.RecentJobs(ctx, s.cfg.Server.Dashboard.History)
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
