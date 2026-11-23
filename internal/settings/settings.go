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
	"github.com/divmora/code-review-console/internal/ee/license"
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
	// Approve has the bot approve merge requests whose review passes:
	// no finding at or above ApproveBlock ("none": whatever it found)
	// and, with ApproveMinScore above 0, every score at least that high.
	// ApproveWithdraw removes the bot's approval when a later review
	// doesn't pass. Rules can turn Approve on or off.
	Approve         *bool  `json:"approve,omitempty"`
	ApproveBlock    string `json:"approve_block,omitempty"`
	ApproveMinScore *int   `json:"approve_min_score,omitempty"`
	ApproveWithdraw *bool  `json:"approve_withdraw,omitempty"`
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
	// CommandAccess says who may run chat commands that spend tokens
	// (review, full review, questions): "write" (authors who can push,
	// the default) or "anyone" who can comment.
	CommandAccess string `json:"command_access,omitempty"`
	// SessionIdleMinutes signs an admin session out after this long
	// without a request from the person (background refreshes don't
	// count); 0 = only session_ttl applies.
	SessionIdleMinutes *int `json:"session_idle_minutes,omitempty"`
}

// DefaultApproveBlock is the severity that blocks the bot's approval until
// one is saved: a major or critical finding.
const DefaultApproveBlock = "major"

// DefaultSessionIdleMinutes is the idle timeout until one is saved.
const DefaultSessionIdleMinutes = 60

// Command access values.
const (
	CommandAccessWrite  = "write"
	CommandAccessAnyone = "anyone"
)

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
	Approve               bool              `json:"approve"`
	ApproveBlock          string            `json:"approve_block"`
	ApproveMinScore       int               `json:"approve_min_score"`
	ApproveWithdraw       bool              `json:"approve_withdraw"`
	ReviewRetentionDays   int               `json:"review_retention_days"`
	LearningsEnabled      bool              `json:"learnings_enabled"`
	DailyTokenBudget      int64             `json:"daily_token_budget"`
	RepoDailyTokenBudget  int64             `json:"repo_daily_token_budget"`
	LearningsAutoActivate bool              `json:"learnings_auto_activate"`
	WebhookRetentionDays  int               `json:"webhook_retention_days"`
	DashboardEnabled      bool              `json:"dashboard_enabled"`
	HideRepos             bool              `json:"hide_repos"`
	DashboardTitle        string            `json:"dashboard_title"`
	CommandAccess         string            `json:"command_access"`
	SessionIdleMinutes    int               `json:"session_idle_minutes"`
	Source                map[string]string `json:"source"`
}

// Service resolves settings. It is safe for concurrent use; a nil store
// means config-file values only.
type Service struct {
	cfg *config.Config
	st  store.Store
	// Lic switches paid settings off in Runtime when it lacks their
	// feature (nil = the free core).
	Lic *license.Manager

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
		ApproveBlock: DefaultApproveBlock, ApproveWithdraw: true,
		ReviewRetentionDays: days(s.cfg.Server.ReviewRetention, 180),
		LearningsEnabled:    true, LearningsAutoActivate: false,
		WebhookRetentionDays: days(s.cfg.Server.WebhookRetention, 14),
		DashboardEnabled:     d.Enabled, HideRepos: d.HideRepos, DashboardTitle: d.Title,
		CommandAccess: CommandAccessWrite, SessionIdleMinutes: DefaultSessionIdleMinutes,
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
	pick("approve", &e.Approve, st.Approve)
	pick("approve_withdraw", &e.ApproveWithdraw, st.ApproveWithdraw)
	e.Source["approve_block"] = "config"
	if st.ApproveBlock != "" {
		e.ApproveBlock, e.Source["approve_block"] = st.ApproveBlock, "database"
	}
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
	pickInt("approve_min_score", &e.ApproveMinScore, st.ApproveMinScore)
	pickInt("review_retention_days", &e.ReviewRetentionDays, st.ReviewRetentionDays)
	pickInt("webhook_retention_days", &e.WebhookRetentionDays, st.WebhookRetentionDays)
	e.Source["sync_source_branches"] = "config"
	if st.SyncSourceBranches != nil {
		e.SyncSourceBranches, e.Source["sync_source_branches"] = st.SyncSourceBranches, "database"
	}
	pick("hide_repos", &e.HideRepos, st.HideRepos)
	pickInt("session_idle_minutes", &e.SessionIdleMinutes, st.SessionIdleMinutes)
	e.Source["command_access"] = "config"
	if st.CommandAccess != "" {
		e.CommandAccess, e.Source["command_access"] = st.CommandAccess, "database"
	}
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

// Runtime returns the settings the console acts on: Get, with the paid
// settings the license does not cover switched off. Get (what admins saved)
// is what the admin panel shows and edits, so a license that lapses and is
// renewed keeps them.
func (s *Service) Runtime(ctx context.Context) Effective {
	return s.Get(ctx).Licensed(s.Lic)
}

// Licensed returns e with the paid settings lic does not cover switched off.
func (e Effective) Licensed(lic *license.Manager) Effective {
	if !lic.Has(license.Learnings) {
		e.LearningsEnabled = false
	}
	if !lic.Has(license.Budgets) {
		e.DailyTokenBudget, e.RepoDailyTokenBudget = 0, 0
	}
	if !lic.Has(license.BranchSync) {
		e.SkipBranchSync, e.SyncSourceBranches = false, nil
	}
	if !lic.Has(license.Metrics) && e.ReviewRetentionDays > license.FreeRetentionDays {
		e.ReviewRetentionDays = license.FreeRetentionDays
	}
	return e
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
		ScoreLabels: e.ScoreLabels, CommitStatus: e.CommitStatus, Approve: e.Approve, ReviewDefaults: e.ReviewDefaults}
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
	switch st.CommandAccess {
	case "", CommandAccessWrite, CommandAccessAnyone:
	default:
		return fmt.Errorf("command_access %q: want write or anyone", st.CommandAccess)
	}
	if len(st.DashboardTitle) > 120 {
		return errors.New("dashboard title is limited to 120 characters")
	}
	if strings.ContainsAny(st.DashboardTitle, "<>") {
		return errors.New("dashboard title cannot contain < or >")
	}
	if v := st.SessionIdleMinutes; v != nil && *v != 0 && (*v < 5 || *v > 1440) {
		return errors.New("sign out after inactivity: between 5 and 1440 minutes, or 0 for never")
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
	switch st.ApproveBlock {
	case "", "none", "critical", "major", "minor", "nitpick":
	default:
		return fmt.Errorf("approve_block %q: want critical, major, minor, nitpick or none", st.ApproveBlock)
	}
	if v := st.ApproveMinScore; v != nil && (*v < 0 || *v > 100) {
		return errors.New("approve_min_score: want 0 to 100 (0 ignores the scores)")
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
