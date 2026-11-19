// Package settings holds console settings that admins change at runtime.
//
// The config file is the initial configuration: on first start (no saved
// settings yet) its values are copied into the database, and from then on
// the database is the source of truth, edited from the admin console.
// Later edits to the config file do not override saved values; Seed logs
// the differences. A setting added in a newer version, absent from the
// saved row, falls back to the config file until it is saved.
package settings

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
)

// Key is the settings row that holds Stored.
const Key = "console"

// cacheTTL bounds how stale another instance's view can be after a save.
const cacheTTL = 5 * time.Second

// Stored is what admins saved. Nil fields fall back to the config file.
type Stored struct {
	// Allowlist: true = repositories no rule matches are not reviewed.
	Allowlist *bool `json:"allowlist,omitempty"`
	// RequireReviewer: review only when the bot user is a reviewer.
	RequireReviewer *bool `json:"require_reviewer,omitempty"`
	// ReviewDefaults apply to every repository, under rules and repository
	// settings.
	ReviewDefaults config.Overrides `json:"review_defaults"`
	// SkipBranchSync skips automatic reviews when the source branch is the
	// default branch or matches SyncSourceBranches.
	SkipBranchSync     *bool    `json:"skip_branch_sync,omitempty"`
	SyncSourceBranches []string `json:"sync_source_branches,omitempty"`
	// ScoreLabels and CommitStatus control what a review posts.
	ScoreLabels  *bool `json:"score_labels,omitempty"`
	CommitStatus *bool `json:"commit_status,omitempty"`
	// Retention in days for review history and webhook deliveries.
	ReviewRetentionDays *int `json:"review_retention_days,omitempty"`
	// Learnings: capture team decisions from replies, and whether new ones
	// apply at once (else they wait for an admin).
	LearningsEnabled *bool `json:"learnings_enabled,omitempty"`
	// Daily token budgets (input + output, UTC day); 0 = no limit.
	DailyTokenBudget      *int64 `json:"daily_token_budget,omitempty"`
	RepoDailyTokenBudget  *int64 `json:"repo_daily_token_budget,omitempty"`
	LearningsAutoActivate *bool  `json:"learnings_auto_activate,omitempty"`
	WebhookRetentionDays  *int   `json:"webhook_retention_days,omitempty"`
	// Dashboard: the status page at /dashboard.
	DashboardEnabled *bool  `json:"dashboard_enabled,omitempty"`
	HideRepos        *bool  `json:"hide_repos,omitempty"`
	DashboardTitle   string `json:"dashboard_title,omitempty"`
}

// Effective is what the console uses now, with where each value came from
// ("database", or "config" when it was never saved).
type Effective struct {
	Allowlist             bool              `json:"allowlist"`
	RequireReviewer       bool              `json:"require_reviewer"`
	ReviewDefaults        config.Overrides  `json:"review_defaults"`
	SkipBranchSync        bool              `json:"skip_branch_sync"`
	SyncSourceBranches    []string          `json:"sync_source_branches"`
	ScoreLabels           bool              `json:"score_labels"`
	CommitStatus          bool              `json:"commit_status"`
	ReviewRetentionDays   int               `json:"review_retention_days"`
	LearningsEnabled      bool              `json:"learnings_enabled"`
	DailyTokenBudget      int64             `json:"daily_token_budget"`
	RepoDailyTokenBudget  int64             `json:"repo_daily_token_budget"`
	LearningsAutoActivate bool              `json:"learnings_auto_activate"`
	WebhookRetentionDays  int               `json:"webhook_retention_days"`
	DashboardEnabled      bool              `json:"dashboard_enabled"`
	HideRepos             bool              `json:"hide_repos"`
	DashboardTitle        string            `json:"dashboard_title"`
	Source                map[string]string `json:"source"`
}

// Service resolves settings. It is safe for concurrent use; a nil store
// means config-file values only.
type Service struct {
	cfg *config.Config
	st  store.Store

	mu     sync.Mutex
	cached *Effective
	at     time.Time
}

// New returns a Service.
func New(cfg *config.Config, st store.Store) *Service {
	return &Service{cfg: cfg, st: st}
}

// Defaults returns the config-file values, as Effective.
func (s *Service) Defaults() Effective {
	d := s.cfg.Server.Dashboard
	skip := s.cfg.Repos.SkipBranchSync == nil || *s.cfg.Repos.SkipBranchSync
	return Effective{
		Allowlist: s.cfg.Repos.Allowlist, RequireReviewer: s.cfg.Repos.RequireReviewer,
		SkipBranchSync: skip, SyncSourceBranches: append([]string{}, s.cfg.Repos.SyncSourceBranches...),
		ScoreLabels: s.cfg.Repos.ScoreLabels, CommitStatus: s.cfg.Repos.CommitStatus,
		ReviewRetentionDays: days(s.cfg.Server.ReviewRetention, 180),
		LearningsEnabled:    true, LearningsAutoActivate: true,
		WebhookRetentionDays: days(s.cfg.Server.WebhookRetention, 14),
		DashboardEnabled:     d.Enabled, HideRepos: d.HideRepos, DashboardTitle: d.Title,
		Source: map[string]string{},
	}
}

// Stored returns what admins saved (zero value when nothing).
func (s *Service) Stored(ctx context.Context) (Stored, error) {
	var st Stored
	if s.st == nil {
		return st, nil
	}
	_, err := s.st.GetSetting(ctx, Key, &st)
	return st, err
}

// Get returns the effective settings, cached briefly. On a database error
// it falls back to the last known values, or the config file.
func (s *Service) Get(ctx context.Context) Effective {
	if s.st == nil {
		return s.resolve(Stored{}) // config file only: nothing to cache
	}
	s.mu.Lock()
	if s.cached != nil && time.Since(s.at) < cacheTTL {
		e := *s.cached
		s.mu.Unlock()
		return e
	}
	s.mu.Unlock()
	stored, err := s.Stored(ctx)
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cached != nil {
			return *s.cached
		}
		return s.resolve(Stored{})
	}
	e := s.resolve(stored)
	s.mu.Lock()
	s.cached, s.at = &e, time.Now()
	s.mu.Unlock()
	return e
}

func (s *Service) resolve(st Stored) Effective {
	e := s.Defaults()
	pick := func(name string, dst *bool, v *bool) {
		e.Source[name] = "config"
		if v != nil {
			*dst, e.Source[name] = *v, "database"
		}
	}
	pick("allowlist", &e.Allowlist, st.Allowlist)
	pick("require_reviewer", &e.RequireReviewer, st.RequireReviewer)
	pick("dashboard_enabled", &e.DashboardEnabled, st.DashboardEnabled)
	pick("skip_branch_sync", &e.SkipBranchSync, st.SkipBranchSync)
	pick("score_labels", &e.ScoreLabels, st.ScoreLabels)
	pick("commit_status", &e.CommitStatus, st.CommitStatus)
	pick("learnings_enabled", &e.LearningsEnabled, st.LearningsEnabled)
	for _, b := range []struct {
		name string
		dst  *int64
		v    *int64
	}{{"daily_token_budget", &e.DailyTokenBudget, st.DailyTokenBudget}, {"repo_daily_token_budget", &e.RepoDailyTokenBudget, st.RepoDailyTokenBudget}} {
		e.Source[b.name] = "config"
		if b.v != nil {
			*b.dst, e.Source[b.name] = *b.v, "database"
		}
	}
	pick("learnings_auto_activate", &e.LearningsAutoActivate, st.LearningsAutoActivate)
	pickInt := func(name string, dst *int, v *int) {
		e.Source[name] = "config"
		if v != nil {
			*dst, e.Source[name] = *v, "database"
		}
	}
	pickInt("review_retention_days", &e.ReviewRetentionDays, st.ReviewRetentionDays)
	pickInt("webhook_retention_days", &e.WebhookRetentionDays, st.WebhookRetentionDays)
	e.Source["sync_source_branches"] = "config"
	if st.SyncSourceBranches != nil {
		e.SyncSourceBranches, e.Source["sync_source_branches"] = st.SyncSourceBranches, "database"
	}
	pick("hide_repos", &e.HideRepos, st.HideRepos)
	e.Source["dashboard_title"] = "config"
	if st.DashboardTitle != "" {
		e.DashboardTitle, e.Source["dashboard_title"] = st.DashboardTitle, "database"
	}
	e.ReviewDefaults = st.ReviewDefaults
	return e
}

func days(d time.Duration, def int) int {
	if d <= 0 {
		return def
	}
	return max(1, int(d.Hours()/24))
}

// ReviewRetention and WebhookRetention as durations.
func (e Effective) ReviewRetention() time.Duration {
	return time.Duration(max(1, e.ReviewRetentionDays)) * 24 * time.Hour
}

// WebhookRetention is how long webhook deliveries are kept.
func (e Effective) WebhookRetention() time.Duration {
	return time.Duration(max(1, e.WebhookRetentionDays)) * 24 * time.Hour
}

// Base is the global level for store.Resolve.
func (e Effective) Base() store.Base {
	return store.Base{Allowlist: e.Allowlist, RequireReviewer: e.RequireReviewer, SkipBranchSync: e.SkipBranchSync,
		ScoreLabels: e.ScoreLabels, CommitStatus: e.CommitStatus, ReviewDefaults: e.ReviewDefaults}
}

// BranchSync reports whether a merge/pull request from source to target is
// a branch sync to skip (when skip is on): the source is the default
// branch, or matches one of the patterns. It returns the reason.
func BranchSync(skip bool, patterns []string, source, target, defaultBranch string) (bool, string) {
	if !skip || source == "" || source == target {
		return false, ""
	}
	if defaultBranch != "" && source == defaultBranch {
		return true, fmt.Sprintf("branch sync from the default branch (%s into %s)", source, target)
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, source); ok {
			return true, fmt.Sprintf("branch sync from %s into %s (matches %q)", source, target, p)
		}
	}
	return false, ""
}

// SeedActor is the audit actor for the initial copy of the config file.
const SeedActor = "config file (initial)"

// Seed copies the config file into the database when nothing is saved yet,
// and otherwise reports config values that differ from the saved ones
// (the saved ones win). It returns those differences.
func (s *Service) Seed(ctx context.Context) (seeded bool, differs []string, err error) {
	if s.st == nil {
		return false, nil, nil
	}
	var stored Stored
	found, err := s.st.GetSetting(ctx, Key, &stored)
	if err != nil {
		return false, nil, err
	}
	d := s.Defaults()
	if !found {
		init := Stored{Allowlist: &d.Allowlist, RequireReviewer: &d.RequireReviewer,
			SkipBranchSync: &d.SkipBranchSync, SyncSourceBranches: d.SyncSourceBranches,
			ScoreLabels: &d.ScoreLabels, CommitStatus: &d.CommitStatus,
			ReviewRetentionDays: &d.ReviewRetentionDays, WebhookRetentionDays: &d.WebhookRetentionDays,
			DashboardEnabled: &d.DashboardEnabled, HideRepos: &d.HideRepos, DashboardTitle: d.DashboardTitle}
		_, err := s.Put(ctx, init, SeedActor)
		return err == nil, nil, err
	}
	cmp := func(name string, file bool, saved *bool) {
		if saved != nil && *saved != file {
			differs = append(differs, fmt.Sprintf("%s: config file %v, database %v", name, file, *saved))
		}
	}
	cmp("repos.allowlist", d.Allowlist, stored.Allowlist)
	cmp("repos.require_reviewer", d.RequireReviewer, stored.RequireReviewer)
	cmp("server.dashboard.enabled", d.DashboardEnabled, stored.DashboardEnabled)
	cmp("server.dashboard.hide_repos", d.HideRepos, stored.HideRepos)
	if stored.DashboardTitle != "" && stored.DashboardTitle != d.DashboardTitle {
		differs = append(differs, fmt.Sprintf("server.dashboard.title: config file %q, database %q", d.DashboardTitle, stored.DashboardTitle))
	}
	return false, differs, nil
}

// Validate checks values before they are saved.
func (st Stored) Validate(profiles map[string]bool, haveStore bool) error {
	if st.Allowlist != nil && *st.Allowlist && !haveStore {
		return errors.New("the allowlist needs a database")
	}
	if len(st.DashboardTitle) > 120 {
		return errors.New("dashboard title is limited to 120 characters")
	}
	if strings.ContainsAny(st.DashboardTitle, "<>") {
		return errors.New("dashboard title cannot contain < or >")
	}
	for name, v := range map[string]*int{"review retention": st.ReviewRetentionDays, "webhook retention": st.WebhookRetentionDays} {
		if v != nil && (*v < 1 || *v > 3650) {
			return fmt.Errorf("%s must be between 1 and 3650 days", name)
		}
	}
	for _, v := range []*int64{st.DailyTokenBudget, st.RepoDailyTokenBudget} {
		if v != nil && *v < 0 {
			return errors.New("token budgets cannot be negative (0 means no limit)")
		}
	}
	if len(st.SyncSourceBranches) > 50 {
		return errors.New("at most 50 sync source branches")
	}
	for _, b := range st.SyncSourceBranches {
		if b == "" || len(b) > 200 || strings.ContainsAny(b, " \t\n") {
			return fmt.Errorf("sync source branch %q: use a branch name or glob (develop, release/*)", b)
		}
		if _, err := path.Match(b, ""); err != nil {
			return fmt.Errorf("sync source branch %q: %w", b, err)
		}
	}
	if err := st.ReviewDefaults.Validate(profiles); err != nil {
		return fmt.Errorf("review defaults: %w", err)
	}
	return nil
}

// Put saves settings (audited by the store) and refreshes the cache.
func (s *Service) Put(ctx context.Context, st Stored, actor string) (Effective, error) {
	if s.st == nil {
		return Effective{}, errors.New("settings need a database")
	}
	if err := s.st.PutSetting(ctx, Key, st, actor); err != nil {
		return Effective{}, err
	}
	e := s.resolve(st)
	s.mu.Lock()
	s.cached, s.at = &e, time.Now()
	s.mu.Unlock()
	return e, nil
}
