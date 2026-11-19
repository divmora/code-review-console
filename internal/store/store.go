// Package store persists admin settings, the repository allowlist, admin
// sessions, the audit log and job history in PostgreSQL.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/divmora/code-review-console/internal/config"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Repo is one repository known to the server.
type Repo struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	// Mode is inherit (follow rules and the allowlist default), enabled or
	// disabled.
	Mode      string           `json:"mode"`
	Settings  config.Overrides `json:"settings"`
	FirstSeen time.Time        `json:"first_seen"`
	LastSeen  *time.Time       `json:"last_seen,omitempty"`
	UpdatedAt time.Time        `json:"updated_at"`
	UpdatedBy string           `json:"updated_by"`
}

// Job is a finished background job (review, answer, help).
type Job struct {
	ID          int64
	RequestedBy string // admin who triggered it from the UI, if any
	// Result is the review result JSON (summary, findings), when stored.
	Result    json.RawMessage
	Kind      string
	Platform  string
	Host      string
	Repo      string
	Number    int
	URL       string
	Trigger   string
	State     string
	Enqueued  time.Time
	Started   time.Time
	Finished  time.Time
	Merged    int
	Error     string
	Note      string
	Comments  int
	Files     int
	TokensIn  int
	TokensOut int
	Model     string
}

// HostRow is a code host as stored: Config holds non-secret settings and
// Secrets the encrypted secrets (see internal/hosts). The store never sees
// secret values in clear.
type HostRow struct {
	ID        int64           `json:"id"`
	Platform  string          `json:"platform"`
	Host      string          `json:"host"`
	BaseURL   string          `json:"base_url"`
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config"`
	Secrets   string          `json:"-"`
	UpdatedAt time.Time       `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}

// WebhookEvent is one webhook delivery and what the console did with it.
// Project, Kind and Action come from the payload; for rejected deliveries
// they are unverified.
type WebhookEvent struct {
	ID         int64     `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
	Platform   string    `json:"platform"`
	Host       string    `json:"host"`
	Project    string    `json:"project"`
	Number     int       `json:"number,omitempty"`
	Event      string    `json:"event"`
	Kind       string    `json:"kind"`
	Action     string    `json:"action"`
	Sender     string    `json:"sender"`
	Delivery   string    `json:"delivery"`
	Auth       string    `json:"auth"`    // signature, token, failed
	Outcome    string    `json:"outcome"` // queued, ignored, duplicate, rejected, error
	Reason     string    `json:"reason"`
	Status     int       `json:"status"`
	DurationMS int       `json:"duration_ms"`
}

// Webhook outcomes.
const (
	HookQueued    = "queued"
	HookIgnored   = "ignored"
	HookDuplicate = "duplicate"
	HookRejected  = "rejected"
	HookError     = "error"
)

// WebhookFilter narrows ListWebhookEvents. Zero values match everything.
type WebhookFilter struct {
	Platform string
	Project  string // substring, case-insensitive
	Outcome  string
	BeforeID int64
	Limit    int
}

// Session is an admin login session.
type Session struct {
	Subject string
	Email   string
	Name    string
	CSRF    string
	Expires time.Time
}

// JobFilter narrows ListJobs. Zero values match everything.
type JobFilter struct {
	Platform string
	Repo     string // exact "owner/repo" or "group/project"
	State    string
	Kind     string
	BeforeID int64 // pagination: only jobs with id < BeforeID
	Limit    int
}

// ModelProfile is a model configuration managed in the UI. It never holds
// an API key, only the name of the env var that does (api_key_env).
type ModelProfile struct {
	Name      string              `json:"name"`
	Config    config.ModelProfile `json:"config"`
	UpdatedAt time.Time           `json:"updated_at"`
	UpdatedBy string              `json:"updated_by"`
}

// AuditEntry records an admin action.
type AuditEntry struct {
	ID     int64           `json:"id"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target"`
	Detail json.RawMessage `json:"detail"`
}

// Store is the persistence API used by the server.
type Store interface {
	// TouchRepo records that a repository sent a webhook and returns it.
	// New repositories start disabled.
	TouchRepo(ctx context.Context, platform, host, path string) (*Repo, error)
	FindRepo(ctx context.Context, platform, host, path string) (*Repo, error)
	ListRepos(ctx context.Context) ([]Repo, error)
	CreateRepo(ctx context.Context, platform, host, path, mode string, actor string) (*Repo, error)
	UpdateRepo(ctx context.Context, id int64, mode string, settings config.Overrides, actor string) (*Repo, error)
	DeleteRepo(ctx context.Context, id int64, actor string) error

	// Rules enable or disable repositories by group or pattern.
	ListRules(ctx context.Context) ([]Rule, error)
	CreateRule(ctx context.Context, r Rule, actor string) (*Rule, error)
	// UpdateRule replaces a rule's settings (not its scope).
	UpdateRule(ctx context.Context, id int64, in Rule, actor string) (*Rule, error)
	DeleteRule(ctx context.Context, id int64, actor string) error

	// Code hosts. PutHost creates or updates by (platform, host); audit
	// details never include secrets.
	ListHosts(ctx context.Context) ([]HostRow, error)
	PutHost(ctx context.Context, h HostRow, actor string) (*HostRow, error)
	DeleteHost(ctx context.Context, id int64, actor string) error

	// Webhook history.
	InsertWebhookEvents(ctx context.Context, events []WebhookEvent) error
	ListWebhookEvents(ctx context.Context, f WebhookFilter) ([]WebhookEvent, error)
	// CountWebhookEvents counts events by outcome since a time.
	CountWebhookEvents(ctx context.Context, since time.Time) (map[string]int, error)
	// PruneWebhookEvents deletes events older than before and keeps at most
	// keep rows.
	PruneWebhookEvents(ctx context.Context, before time.Time, keep int) (int64, error)

	InsertJob(ctx context.Context, j Job) (int64, error)
	RecentJobs(ctx context.Context, limit int) ([]Job, error)
	// ListJobs returns jobs newest first, without Result.
	ListJobs(ctx context.Context, f JobFilter) ([]Job, error)
	// GetJob returns one job including Result.
	GetJob(ctx context.Context, id int64) (*Job, error)

	ListModelProfiles(ctx context.Context) ([]ModelProfile, error)
	PutModelProfile(ctx context.Context, name string, cfg config.ModelProfile, actor string) error
	DeleteModelProfile(ctx context.Context, name string, actor string) error

	// GetSetting decodes a global setting into out; found is false if unset.
	GetSetting(ctx context.Context, key string, out any) (found bool, err error)
	PutSetting(ctx context.Context, key string, value any, actor string) error

	CreateSession(ctx context.Context, idHash []byte, s Session) error
	GetSession(ctx context.Context, idHash []byte) (*Session, error)
	DeleteSession(ctx context.Context, idHash []byte) error

	Audit(ctx context.Context, actor, action, target string, detail any) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)

	Close()
}

// Postgres implements Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// Open connects and applies migrations.
func Open(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close releases the pool.
func (p *Postgres) Close() { p.pool.Close() }

var migrations = []string{
	// 1: initial schema
	`CREATE TABLE repos (
		id          bigserial PRIMARY KEY,
		platform    text NOT NULL,
		host        text NOT NULL,
		path        text NOT NULL,
		enabled     boolean NOT NULL DEFAULT false,
		settings    jsonb NOT NULL DEFAULT '{}',
		first_seen  timestamptz NOT NULL DEFAULT now(),
		last_seen   timestamptz,
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT '',
		UNIQUE (platform, host, path)
	);
	CREATE TABLE jobs (
		id         bigserial PRIMARY KEY,
		kind       text NOT NULL,
		platform   text NOT NULL,
		host       text NOT NULL DEFAULT '',
		repo       text NOT NULL,
		number     integer NOT NULL,
		url        text NOT NULL DEFAULT '',
		trigger    text NOT NULL DEFAULT '',
		state      text NOT NULL,
		enqueued   timestamptz NOT NULL,
		started    timestamptz,
		finished   timestamptz NOT NULL,
		merged     integer NOT NULL DEFAULT 0,
		error      text NOT NULL DEFAULT '',
		note       text NOT NULL DEFAULT '',
		comments   integer NOT NULL DEFAULT 0,
		files      integer NOT NULL DEFAULT 0,
		tokens_in  integer NOT NULL DEFAULT 0,
		tokens_out integer NOT NULL DEFAULT 0,
		model      text NOT NULL DEFAULT ''
	);
	CREATE INDEX jobs_finished_idx ON jobs (finished DESC);
	CREATE TABLE sessions (
		id_hash  bytea PRIMARY KEY,
		subject  text NOT NULL,
		email    text NOT NULL,
		name     text NOT NULL DEFAULT '',
		csrf     text NOT NULL,
		created  timestamptz NOT NULL DEFAULT now(),
		expires  timestamptz NOT NULL
	);
	CREATE TABLE audit_log (
		id      bigserial PRIMARY KEY,
		at      timestamptz NOT NULL DEFAULT now(),
		actor   text NOT NULL,
		action  text NOT NULL,
		target  text NOT NULL,
		detail  jsonb NOT NULL DEFAULT '{}'
	);
	CREATE INDEX audit_log_at_idx ON audit_log (at DESC);`,
	// 2: review results, manual triggers, UI-managed models and settings
	`ALTER TABLE jobs ADD COLUMN result jsonb;
	ALTER TABLE jobs ADD COLUMN requested_by text NOT NULL DEFAULT '';
	CREATE INDEX jobs_repo_idx ON jobs (platform, repo, id DESC);
	CREATE TABLE model_profiles (
		name        text PRIMARY KEY,
		config      jsonb NOT NULL,
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT ''
	);
	CREATE TABLE settings (
		key         text PRIMARY KEY,
		value       jsonb NOT NULL,
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT ''
	);`,
	// 3: group and pattern rules; repositories can inherit from them
	`ALTER TABLE repos ADD COLUMN mode text NOT NULL DEFAULT 'inherit'
		CHECK (mode IN ('inherit', 'enabled', 'disabled'));
	UPDATE repos SET mode = CASE WHEN enabled THEN 'enabled' WHEN updated_by <> '' THEN 'disabled' ELSE 'inherit' END;
	ALTER TABLE repos DROP COLUMN enabled;
	CREATE TABLE repo_rules (
		id          bigserial PRIMARY KEY,
		platform    text NOT NULL,
		host        text NOT NULL,
		pattern     text NOT NULL,
		enabled     boolean NOT NULL,
		settings    jsonb NOT NULL DEFAULT '{}',
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT '',
		UNIQUE (platform, host, pattern)
	);`,
	// 4: webhook delivery history
	`CREATE TABLE webhook_events (
		id           bigserial PRIMARY KEY,
		received_at  timestamptz NOT NULL DEFAULT now(),
		platform     text NOT NULL,
		host         text NOT NULL DEFAULT '',
		project      text NOT NULL DEFAULT '',
		number       integer NOT NULL DEFAULT 0,
		event        text NOT NULL DEFAULT '',
		kind         text NOT NULL DEFAULT '',
		action       text NOT NULL DEFAULT '',
		sender       text NOT NULL DEFAULT '',
		delivery     text NOT NULL DEFAULT '',
		auth         text NOT NULL DEFAULT '',
		outcome      text NOT NULL,
		reason       text NOT NULL DEFAULT '',
		status       integer NOT NULL DEFAULT 0,
		duration_ms  integer NOT NULL DEFAULT 0
	);
	CREATE INDEX webhook_events_received_idx ON webhook_events (received_at DESC);
	CREATE INDEX webhook_events_outcome_idx ON webhook_events (outcome, id DESC);`,
	// 5: host-wide rules and per-scope settings; each is optional (inherit)
	`ALTER TABLE repo_rules ALTER COLUMN enabled DROP NOT NULL;
	ALTER TABLE repo_rules ADD COLUMN require_reviewer boolean;
	ALTER TABLE repo_rules ADD COLUMN skip_branch_sync boolean;
	ALTER TABLE repo_rules ADD COLUMN score_labels boolean;
	ALTER TABLE repo_rules ADD COLUMN commit_status boolean;`,
	// 6: code hosts managed in the admin panel; secrets are encrypted by the
	// console before they reach the database
	`CREATE TABLE hosts (
		id          bigserial PRIMARY KEY,
		platform    text NOT NULL,
		host        text NOT NULL,
		base_url    text NOT NULL,
		name        text NOT NULL DEFAULT '',
		enabled     boolean NOT NULL DEFAULT true,
		config      jsonb NOT NULL DEFAULT '{}',
		secrets     text NOT NULL DEFAULT '',
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT '',
		UNIQUE (platform, host)
	);`,
}

// migrate applies pending migrations under an advisory lock, so several
// server instances can start at once.
func (p *Postgres) migrate(ctx context.Context) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	const lockID = 7349210911 // arbitrary, fixed
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS codereview_schema (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema table: %w", err)
	}
	var current int
	if err := conn.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM codereview_schema`).Scan(&current); err != nil {
		return err
	}
	for i := current; i < len(migrations); i++ {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, migrations[i]); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO codereview_schema (version) VALUES ($1)`, i+1); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

const repoCols = `id, platform, host, path, mode, settings, first_seen, last_seen, updated_at, updated_by`

func scanRepo(row pgx.Row) (*Repo, error) {
	var r Repo
	var settings []byte
	err := row.Scan(&r.ID, &r.Platform, &r.Host, &r.Path, &r.Mode, &settings, &r.FirstSeen, &r.LastSeen, &r.UpdatedAt, &r.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &r.Settings); err != nil {
			return nil, fmt.Errorf("repo %d settings: %w", r.ID, err)
		}
	}
	return &r, nil
}

// RepoKey normalises identifiers. GitHub and GitLab treat repository
// paths case-insensitively, so everything is lower-cased to keep one row
// per repository.
func RepoKey(platform, host, path string) (string, string, string) {
	return strings.ToLower(platform), strings.ToLower(host), strings.ToLower(strings.Trim(path, "/"))
}

func (p *Postgres) TouchRepo(ctx context.Context, platform, host, path string) (*Repo, error) {
	platform, host, path = RepoKey(platform, host, path)
	row := p.pool.QueryRow(ctx, `
		INSERT INTO repos (platform, host, path, last_seen) VALUES ($1, $2, $3, now())
		ON CONFLICT (platform, host, path) DO UPDATE SET last_seen = now()
		RETURNING `+repoCols, platform, host, path)
	return scanRepo(row)
}

func (p *Postgres) FindRepo(ctx context.Context, platform, host, path string) (*Repo, error) {
	platform, host, path = RepoKey(platform, host, path)
	return scanRepo(p.pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos WHERE platform=$1 AND host=$2 AND path=$3`, platform, host, path))
}

func (p *Postgres) ListRepos(ctx context.Context) ([]Repo, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+repoCols+` FROM repos ORDER BY platform, host, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (p *Postgres) CreateRepo(ctx context.Context, platform, host, path, mode string, actor string) (*Repo, error) {
	if !ValidMode(mode) {
		return nil, fmt.Errorf("mode %q: want inherit, enabled or disabled", mode)
	}
	platform, host, path = RepoKey(platform, host, path)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	r, err := scanRepo(tx.QueryRow(ctx, `
		INSERT INTO repos (platform, host, path, mode, updated_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (platform, host, path) DO UPDATE SET mode = EXCLUDED.mode, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+repoCols, platform, host, path, mode, actor))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, actor, "repo.create", repoTarget(r), map[string]any{"mode": mode}); err != nil {
		return nil, err
	}
	return r, tx.Commit(ctx)
}

func (p *Postgres) UpdateRepo(ctx context.Context, id int64, mode string, settings config.Overrides, actor string) (*Repo, error) {
	if !ValidMode(mode) {
		return nil, fmt.Errorf("mode %q: want inherit, enabled or disabled", mode)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	before, err := scanRepo(tx.QueryRow(ctx, `SELECT `+repoCols+` FROM repos WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	js, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	after, err := scanRepo(tx.QueryRow(ctx, `
		UPDATE repos SET mode=$2, settings=$3, updated_at=now(), updated_by=$4 WHERE id=$1
		RETURNING `+repoCols, id, mode, js, actor))
	if err != nil {
		return nil, err
	}
	detail := map[string]any{
		"before": map[string]any{"mode": before.Mode, "settings": before.Settings},
		"after":  map[string]any{"mode": after.Mode, "settings": after.Settings},
	}
	if err := audit(ctx, tx, actor, "repo.update", repoTarget(after), detail); err != nil {
		return nil, err
	}
	return after, tx.Commit(ctx)
}

func (p *Postgres) DeleteRepo(ctx context.Context, id int64, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	r, err := scanRepo(tx.QueryRow(ctx, `DELETE FROM repos WHERE id=$1 RETURNING `+repoCols, id))
	if err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "repo.delete", repoTarget(r), map[string]any{"mode": r.Mode}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const ruleCols = `id, platform, host, pattern, enabled, require_reviewer, skip_branch_sync, score_labels, commit_status, settings, updated_at, updated_by`

func scanRule(row pgx.Row) (*Rule, error) {
	var r Rule
	var settings []byte
	err := row.Scan(&r.ID, &r.Platform, &r.Host, &r.Pattern, &r.Enabled, &r.RequireReviewer, &r.SkipBranchSync, &r.ScoreLabels, &r.CommitStatus, &settings, &r.UpdatedAt, &r.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &r.Settings); err != nil {
			return nil, fmt.Errorf("rule %d settings: %w", r.ID, err)
		}
	}
	return &r, nil
}

func ruleTarget(r *Rule) string { return r.Platform + ":" + r.Host + "/" + r.Pattern }

func ruleDetail(r *Rule) map[string]any {
	return map[string]any{"enabled": r.Enabled, "require_reviewer": r.RequireReviewer, "skip_branch_sync": r.SkipBranchSync,
		"score_labels": r.ScoreLabels, "commit_status": r.CommitStatus, "settings": r.Settings}
}

func (p *Postgres) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+ruleCols+` FROM repo_rules ORDER BY platform, host, pattern`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (p *Postgres) CreateRule(ctx context.Context, in Rule, actor string) (*Rule, error) {
	platform, host, _ := RepoKey(in.Platform, in.Host, "")
	pattern, err := NormalizePattern(in.Pattern)
	if err != nil {
		return nil, err
	}
	js, err := json.Marshal(in.Settings)
	if err != nil {
		return nil, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	r, err := scanRule(tx.QueryRow(ctx, `
		INSERT INTO repo_rules (platform, host, pattern, enabled, require_reviewer, skip_branch_sync, score_labels, commit_status, settings, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (platform, host, pattern) DO UPDATE SET enabled = EXCLUDED.enabled, require_reviewer = EXCLUDED.require_reviewer,
			skip_branch_sync = EXCLUDED.skip_branch_sync, score_labels = EXCLUDED.score_labels, commit_status = EXCLUDED.commit_status,
			settings = EXCLUDED.settings, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+ruleCols, platform, host, pattern, in.Enabled, in.RequireReviewer, in.SkipBranchSync, in.ScoreLabels, in.CommitStatus, js, actor))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, actor, "rule.create", ruleTarget(r), ruleDetail(r)); err != nil {
		return nil, err
	}
	return r, tx.Commit(ctx)
}

func (p *Postgres) UpdateRule(ctx context.Context, id int64, in Rule, actor string) (*Rule, error) {
	js, err := json.Marshal(in.Settings)
	if err != nil {
		return nil, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	before, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleCols+` FROM repo_rules WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	after, err := scanRule(tx.QueryRow(ctx, `
		UPDATE repo_rules SET enabled=$2, require_reviewer=$3, skip_branch_sync=$4, score_labels=$5, commit_status=$6, settings=$7,
			updated_at=now(), updated_by=$8 WHERE id=$1
		RETURNING `+ruleCols, id, in.Enabled, in.RequireReviewer, in.SkipBranchSync, in.ScoreLabels, in.CommitStatus, js, actor))
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"before": ruleDetail(before), "after": ruleDetail(after)}
	if err := audit(ctx, tx, actor, "rule.update", ruleTarget(after), detail); err != nil {
		return nil, err
	}
	return after, tx.Commit(ctx)
}

func (p *Postgres) DeleteRule(ctx context.Context, id int64, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	r, err := scanRule(tx.QueryRow(ctx, `DELETE FROM repo_rules WHERE id=$1 RETURNING `+ruleCols, id))
	if err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "rule.delete", ruleTarget(r), map[string]any{"enabled": r.Enabled}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const hostCols = `id, platform, host, base_url, name, enabled, config, secrets, updated_at, updated_by`

func scanHost(row pgx.Row) (*HostRow, error) {
	var h HostRow
	err := row.Scan(&h.ID, &h.Platform, &h.Host, &h.BaseURL, &h.Name, &h.Enabled, &h.Config, &h.Secrets, &h.UpdatedAt, &h.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &h, err
}

func hostAudit(h *HostRow) map[string]any {
	return map[string]any{"base_url": h.BaseURL, "name": h.Name, "enabled": h.Enabled, "config": h.Config, "secrets_set": h.Secrets != ""}
}

func (p *Postgres) ListHosts(ctx context.Context) ([]HostRow, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+hostCols+` FROM hosts ORDER BY platform, host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostRow{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

func (p *Postgres) PutHost(ctx context.Context, in HostRow, actor string) (*HostRow, error) {
	if len(in.Config) == 0 {
		in.Config = json.RawMessage("{}")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	h, err := scanHost(tx.QueryRow(ctx, `
		INSERT INTO hosts (platform, host, base_url, name, enabled, config, secrets, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (platform, host) DO UPDATE SET base_url = EXCLUDED.base_url, name = EXCLUDED.name, enabled = EXCLUDED.enabled,
			config = EXCLUDED.config, secrets = EXCLUDED.secrets, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+hostCols, strings.ToLower(in.Platform), strings.ToLower(in.Host), in.BaseURL, in.Name, in.Enabled, in.Config, in.Secrets, actor))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, actor, "host.put", h.Platform+":"+h.Host, hostAudit(h)); err != nil {
		return nil, err
	}
	return h, tx.Commit(ctx)
}

func (p *Postgres) DeleteHost(ctx context.Context, id int64, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	h, err := scanHost(tx.QueryRow(ctx, `DELETE FROM hosts WHERE id=$1 RETURNING `+hostCols, id))
	if err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "host.delete", h.Platform+":"+h.Host, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const hookCols = `id, received_at, platform, host, project, number, event, kind, action, sender, delivery, auth, outcome, reason, status, duration_ms`

func (p *Postgres) InsertWebhookEvents(ctx context.Context, events []WebhookEvent) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(events))
	for _, e := range events {
		if e.ReceivedAt.IsZero() {
			e.ReceivedAt = time.Now()
		}
		rows = append(rows, []any{e.ReceivedAt, e.Platform, e.Host, e.Project, e.Number, e.Event, e.Kind, e.Action,
			e.Sender, e.Delivery, e.Auth, e.Outcome, e.Reason, e.Status, e.DurationMS})
	}
	_, err := p.pool.CopyFrom(ctx, pgx.Identifier{"webhook_events"},
		[]string{"received_at", "platform", "host", "project", "number", "event", "kind", "action", "sender", "delivery", "auth", "outcome", "reason", "status", "duration_ms"},
		pgx.CopyFromRows(rows))
	return err
}

func (p *Postgres) ListWebhookEvents(ctx context.Context, f WebhookFilter) ([]WebhookEvent, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT ` + hookCols + ` FROM webhook_events WHERE true`
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.Platform != "" {
		add("platform = $%d", f.Platform)
	}
	if f.Outcome != "" {
		add("outcome = $%d", f.Outcome)
	}
	if f.Project != "" {
		add("project ILIKE $%d", "%"+likeEscape(f.Project)+"%")
	}
	if f.BeforeID > 0 {
		add("id < $%d", f.BeforeID)
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebhookEvent{}
	for rows.Next() {
		var e WebhookEvent
		if err := rows.Scan(&e.ID, &e.ReceivedAt, &e.Platform, &e.Host, &e.Project, &e.Number, &e.Event, &e.Kind, &e.Action,
			&e.Sender, &e.Delivery, &e.Auth, &e.Outcome, &e.Reason, &e.Status, &e.DurationMS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (p *Postgres) CountWebhookEvents(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT outcome, count(*) FROM webhook_events WHERE received_at >= $1 GROUP BY outcome`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

func (p *Postgres) PruneWebhookEvents(ctx context.Context, before time.Time, keep int) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM webhook_events WHERE received_at < $1
		OR id <= (SELECT id FROM webhook_events ORDER BY id DESC OFFSET $2 LIMIT 1)`, before, keep)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func repoTarget(r *Repo) string { return r.Platform + ":" + r.Host + "/" + r.Path }

func audit(ctx context.Context, tx pgx.Tx, actor, action, target string, detail any) error {
	js, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4)`, actor, action, target, js)
	return err
}

func (p *Postgres) Audit(ctx context.Context, actor, action, target string, detail any) error {
	js, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4)`, actor, action, target, js)
	return err
}

func (p *Postgres) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, at, actor, action, target, detail FROM audit_log ORDER BY at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (p *Postgres) InsertJob(ctx context.Context, j Job) (int64, error) {
	var result any
	if len(j.Result) > 0 {
		result = []byte(j.Result)
	}
	var id int64
	err := p.pool.QueryRow(ctx, `
		INSERT INTO jobs (kind, platform, host, repo, number, url, trigger, state, enqueued, started, finished,
			merged, error, note, comments, files, tokens_in, tokens_out, model, result, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id`,
		j.Kind, j.Platform, j.Host, j.Repo, j.Number, j.URL, j.Trigger, j.State, j.Enqueued, nullTime(j.Started), j.Finished,
		j.Merged, j.Error, j.Note, j.Comments, j.Files, j.TokensIn, j.TokensOut, j.Model, result, j.RequestedBy).Scan(&id)
	return id, err
}

const jobCols = `id, kind, platform, host, repo, number, url, trigger, state, enqueued, started, finished,
	merged, error, note, comments, files, tokens_in, tokens_out, model, requested_by`

func scanJob(row pgx.Row, withResult bool) (*Job, error) {
	var j Job
	var started *time.Time
	dest := []any{&j.ID, &j.Kind, &j.Platform, &j.Host, &j.Repo, &j.Number, &j.URL, &j.Trigger, &j.State, &j.Enqueued, &started, &j.Finished,
		&j.Merged, &j.Error, &j.Note, &j.Comments, &j.Files, &j.TokensIn, &j.TokensOut, &j.Model, &j.RequestedBy}
	var result []byte
	if withResult {
		dest = append(dest, &result)
	}
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if started != nil {
		j.Started = *started
	}
	if len(result) > 0 {
		j.Result = result
	}
	return &j, nil
}

func (p *Postgres) RecentJobs(ctx context.Context, limit int) ([]Job, error) {
	return p.ListJobs(ctx, JobFilter{Limit: limit})
}

func (p *Postgres) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Platform != "" {
		add("platform = $%d", f.Platform)
	}
	if f.Repo != "" {
		add("lower(repo) = lower($%d)", f.Repo)
	}
	if f.State != "" {
		add("state = $%d", f.State)
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.BeforeID > 0 {
		add("id < $%d", f.BeforeID)
	}
	q := `SELECT ` + jobCols + ` FROM jobs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (p *Postgres) GetJob(ctx context.Context, id int64) (*Job, error) {
	return scanJob(p.pool.QueryRow(ctx, `SELECT `+jobCols+`, result FROM jobs WHERE id = $1`, id), true)
}

func (p *Postgres) ListModelProfiles(ctx context.Context) ([]ModelProfile, error) {
	rows, err := p.pool.Query(ctx, `SELECT name, config, updated_at, updated_by FROM model_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelProfile{}
	for rows.Next() {
		var m ModelProfile
		var cfg []byte
		if err := rows.Scan(&m.Name, &cfg, &m.UpdatedAt, &m.UpdatedBy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cfg, &m.Config); err != nil {
			return nil, fmt.Errorf("model profile %s: %w", m.Name, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *Postgres) PutModelProfile(ctx context.Context, name string, cfg config.ModelProfile, actor string) error {
	cfg = cfg.WithoutKey() // never persist secrets
	js, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_profiles (name, config, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		name, js, actor); err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "model.put", name, cfg); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) DeleteModelProfile(ctx context.Context, name string, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `DELETE FROM model_profiles WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit(ctx, tx, actor, "model.delete", name, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) GetSetting(ctx context.Context, key string, out any) (bool, error) {
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

func (p *Postgres) PutSetting(ctx context.Context, key string, value any, actor string) error {
	js, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		key, js, actor); err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "setting.put", key, value); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) CreateSession(ctx context.Context, idHash []byte, s Session) error {
	// Opportunistic cleanup keeps the table small.
	if _, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE expires < now()`); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO sessions (id_hash, subject, email, name, csrf, expires) VALUES ($1,$2,$3,$4,$5,$6)`,
		idHash, s.Subject, s.Email, s.Name, s.CSRF, s.Expires)
	return err
}

func (p *Postgres) GetSession(ctx context.Context, idHash []byte) (*Session, error) {
	var s Session
	err := p.pool.QueryRow(ctx, `SELECT subject, email, name, csrf, expires FROM sessions WHERE id_hash=$1 AND expires > now()`, idHash).
		Scan(&s.Subject, &s.Email, &s.Name, &s.CSRF, &s.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

func (p *Postgres) DeleteSession(ctx context.Context, idHash []byte) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash=$1`, idHash)
	return err
}
