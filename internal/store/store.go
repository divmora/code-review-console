// Package store persists admin settings, the repository allowlist, admin
// sessions, the audit log and job history in PostgreSQL.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/divmora/code-review-console/internal/clientip"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/divmora/code-review-console/internal/config"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is returned when a record already exists.
var ErrDuplicate = errors.New("already exists")

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
	ID          int64  `json:"id"`
	RequestedBy string `json:"requested_by"` // admin who triggered it from the UI, if any
	// Result is the review result JSON (summary, findings), when stored.
	Result    json.RawMessage `json:"result,omitempty"`
	Kind      string          `json:"kind"`
	Platform  string          `json:"platform"`
	Host      string          `json:"host"`
	Repo      string          `json:"repo"`
	Number    int             `json:"number"`
	URL       string          `json:"url"`
	Trigger   string          `json:"trigger"`
	State     string          `json:"state"`
	Enqueued  time.Time       `json:"enqueued"`
	Started   time.Time       `json:"started"`
	Finished  time.Time       `json:"finished"`
	Merged    int             `json:"merged"`
	Error     string          `json:"error"`
	Note      string          `json:"note"`
	Comments  int             `json:"comments"`
	Files     int             `json:"files"`
	TokensIn  int             `json:"tokens_in"`
	TokensOut int             `json:"tokens_out"`
	Model     string          `json:"model"`
	// Scores are the review's scores (nil for other jobs, skipped
	// reviews and CLIs that don't score).
	Scores *Scores `json:"scores,omitempty"`
	// Approval is what the review did with the bot's approval on the code
	// host (approved, already_approved, withdrawn, not_approved,
	// unchanged, failed), or "" when approvals are off. InsertJob reads it
	// from Result when it is empty.
	Approval string `json:"approval,omitempty"`
}

// Scores rate a review's change from 1 (poor) to 100 (excellent), as the
// CLI reports them.
type Scores struct {
	Quality         int `json:"quality"`
	Security        int `json:"security"`
	Maintainability int `json:"maintainability"`
}

// ScoreKinds are the score names, in display order.
var ScoreKinds = []string{"quality", "security", "maintainability"}

// Get returns one score by kind (0 for an unknown kind).
func (s Scores) Get(kind string) int {
	switch kind {
	case "quality":
		return s.Quality
	case "security":
		return s.Security
	case "maintainability":
		return s.Maintainability
	}
	return 0
}

// Valid reports whether every score is in 1..100.
func (s Scores) Valid() bool {
	for _, k := range ScoreKinds {
		if v := s.Get(k); v < 1 || v > 100 {
			return false
		}
	}
	return true
}

// RepoScore is a repository's latest scored review and its daily score
// trend.
type RepoScore struct {
	Platform string    `json:"platform"`
	Host     string    `json:"host"`
	Repo     string    `json:"repo"`
	Number   int       `json:"number"`
	At       time.Time `json:"at"`
	Latest   Scores    `json:"latest"`
	// Trend is one point per day with scored reviews, oldest first.
	Trend []MetricPoint `json:"trend"`
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
	// SecretsChanged names the secrets this save changes, for the audit
	// log (never stored with the host; see hosts.SecretChanges).
	SecretsChanged []string `json:"-"`
}

// Dashboard is a status dashboard (issue #155): its own title, access and
// repositories. A repository is on it when attached (Repos), when it
// matches one of its Patterns, or when AllRepos is set. TokenHash is the
// SHA-256 (hex) of its token, for Access "token"; the token itself is
// never stored.
type Dashboard struct {
	ID           int64              `json:"id"`
	Slug         string             `json:"slug"`
	Title        string             `json:"title"`
	Description  string             `json:"description"`
	Access       string             `json:"access"`
	HideRepos    bool               `json:"hide_repos"`
	AllRepos     bool               `json:"all_repos"`
	Enabled      bool               `json:"enabled"`
	TokenHash    string             `json:"-"`
	TokenExpires time.Time          `json:"token_expires,omitzero"`
	Repos        []DashboardRepo    `json:"repos"`
	Patterns     []DashboardPattern `json:"patterns"`
	CreatedAt    time.Time          `json:"created_at"`
	CreatedBy    string             `json:"created_by"`
	UpdatedAt    time.Time          `json:"updated_at"`
	UpdatedBy    string             `json:"updated_by"`
	// TokenChange is "set", "rotated" or "removed" when this save changes
	// the token (audit only; never the token or its hash).
	TokenChange string `json:"-"`
}

// DashboardRepo is a repository attached to a dashboard (Platform, Host
// and Path are filled in on reads).
type DashboardRepo struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Path     string `json:"path"`
}

// DashboardPattern adds the repositories a pattern covers on a host, as a
// rule does (group with subgroups, or * within one level).
type DashboardPattern struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Pattern  string `json:"pattern"`
}

// HostCredential is an extra credential of a code host, used instead of
// the host's own for the repositories its group or pattern covers (issue
// #136): a GitLab group access token per group, say, so one token never
// reaches every group. Config holds its non-secret settings (bot name,
// reviewer, Bitbucket email); Secrets its token, encrypted by the console
// (see internal/hosts).
type HostCredential struct {
	ID      int64           `json:"id"`
	HostID  int64           `json:"host_id"`
	Name    string          `json:"name"`
	Pattern string          `json:"pattern"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config"`
	Secrets string          `json:"-"`
	// SecretsChanged names the secrets this save changes (audit only).
	SecretsChanged []string  `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      string    `json:"created_by"`
	UpdatedAt      time.Time `json:"updated_at"`
	UpdatedBy      string    `json:"updated_by"`
}

// WebhookEndpoint is a webhook URL of a host, /webhooks/<platform>/<id>,
// created in the admin panel. ID is a random UUID. Secrets holds the
// endpoint's own webhook secrets and signing tokens, encrypted by the
// console (see internal/hosts); deliveries on its URL are verified against
// them only.
type WebhookEndpoint struct {
	ID      string `json:"id"`
	HostID  int64  `json:"host_id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Secrets string `json:"-"`
	// AcceptSecretToken lets a webhook with a signing token still take
	// unsigned deliveries with its secret token (while migrating).
	AcceptSecretToken bool `json:"accept_secret_token"`
	// SecretsChanged names the secrets this save changes (audit only).
	SecretsChanged []string  `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      string    `json:"created_by"`
	UpdatedAt      time.Time `json:"updated_at"`
	UpdatedBy      string    `json:"updated_by"`
}

// Learning statuses.
const (
	LearningActive   = "active"
	LearningPending  = "pending"
	LearningDisabled = "disabled"
)

// Learning is a team decision captured from a review discussion (or added
// by an admin). Scope is a rule pattern on the host: "" for the whole
// host, a group or owner, a pattern with *, or a repository path.
type Learning struct {
	ID           int64  `json:"id"`
	Platform     string `json:"platform"`
	Host         string `json:"host"`
	Scope        string `json:"scope"`
	Text         string `json:"text"`
	Path         string `json:"path"`
	Status       string `json:"status"`
	Source       string `json:"source"` // reply, command, manual
	SourceURL    string `json:"source_url"`
	SourceAuthor string `json:"source_author"`
	// SourceRole is the author's access on the repository when the
	// learning was captured ("developer", "write", "guest", "unknown"...).
	SourceRole string `json:"source_role"`
	// SourceRef identifies the comment it came from, so a redelivered
	// webhook does not create it twice.
	SourceRef string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// AsRule gives the learning's scope the rule matching semantics.
func (l Learning) AsRule() Rule {
	return Rule{Platform: l.Platform, Host: l.Host, Pattern: l.Scope}
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
	// Endpoint is the webhook endpoint ID the delivery came in on; empty for
	// the shared /webhooks/<platform> URL.
	Endpoint   string `json:"endpoint"`
	Auth       string `json:"auth"`    // signature, token, failed
	Outcome    string `json:"outcome"` // queued, ignored, duplicate, rejected, error
	Reason     string `json:"reason"`
	Status     int    `json:"status"`
	DurationMS int    `json:"duration_ms"`
	// SourceIP is the address the delivery came from (see clientip).
	SourceIP string `json:"source_ip"`
}

// HostHookStats summarises a host's verified webhook deliveries in the
// history (see PruneWebhookEvents for how long it is kept).
type HostHookStats struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	// Verified counts deliveries whose signature or token matched.
	Verified int `json:"verified"`
	// Comments counts verified comment events (GitLab notes, GitHub issue
	// and review comments, Bitbucket comment_created).
	Comments int `json:"comments"`
	// SystemHook counts verified deliveries from a GitLab system hook,
	// which never sends comments.
	SystemHook int `json:"system_hook"`
}

// EndpointHookStats is what one webhook URL received: its latest delivery
// in the history, and deliveries since a time by result (issue #131).
type EndpointHookStats struct {
	Endpoint string `json:"endpoint"`
	// Last is the newest delivery on the URL (nil if none is kept).
	Last *WebhookEvent `json:"last,omitempty"`
	// Accepted counts deliveries since the time asked for that passed
	// authentication and were handled (queued, ignored or duplicate);
	// Rejected those refused (bad secret or signature, malformed) or
	// that failed (outcome error).
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

// CommentKinds are the webhook kinds that carry a comment.
var CommentKinds = []string{"note", "issue_comment", "pull_request_review_comment", "pullrequest:comment_created"}

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
	Endpoint string // the webhook URL's ID
	BeforeID int64
	Limit    int
}

// Session is an admin login session.
type Session struct {
	// ID identifies the session for listing and revoking (never the
	// cookie value or its hash).
	ID      int64
	Subject string
	Email   string
	Name    string
	// Groups and Verified are the identity provider's claims at sign-in,
	// so every request can be checked against the current sign-in rules.
	Groups   []string
	Verified bool
	CSRF     string
	Created  time.Time
	Expires  time.Time
	// IP and UserAgent are where the session signed in from; IP follows
	// the latest request. LastSeen is the latest request (updated at most
	// once a minute; zero before the first).
	IP        string
	UserAgent string
	LastSeen  time.Time
	// Role is the signed-in person's role (admin, operator, viewer),
	// worked out from the sign-in rules on every request; not stored.
	Role string
}

// JobFilter narrows ListJobs. Zero values match everything.
type JobFilter struct {
	Platform string
	Host     string
	Repo     string // exact "owner/repo" or "group/project"
	RepoLike string // substring, case-insensitive
	State    string
	Kind     string
	Trigger  string
	From, To time.Time // finished within [From, To)
	BeforeID int64     // pagination: only jobs with id < BeforeID
	Limit    int
	// ScoreKind and ScoreBelow keep scored reviews whose ScoreKind score
	// is below ScoreBelow ("any" means any of the three).
	ScoreKind  string
	ScoreBelow int
	// Severity and Category keep reviews with at least one finding (not a
	// question) of that severity and category, as Impact counts them.
	// Severity "any" means any finding.
	Severity string
	Category string
	// Approval keeps reviews with this approval outcome ("approved", ...).
	Approval string
}

// Metric granularities.
const (
	Hourly  = "hour"
	Daily   = "day"
	Monthly = "month"
)

// MetricsFilter selects rollup rows. Empty Platform/Host/Repo sum over all.
type MetricsFilter struct {
	Granularity string
	From, To    time.Time
	Platform    string
	Host        string
	Repo        string
}

// MetricPoint is one bucket of finished jobs. Rollups are kept per bucket,
// host and repository and updated as each job finishes, so charts never
// scan the jobs table.
type MetricPoint struct {
	Bucket     time.Time `json:"bucket"`
	Jobs       int       `json:"jobs"`
	Done       int       `json:"done"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Comments   int       `json:"comments"`
	Files      int       `json:"files"`
	TokensIn   int64     `json:"tokens_in"`
	TokensOut  int64     `json:"tokens_out"`
	DurationMS int64     `json:"duration_ms"`
	// Scored counts reviews with scores; the sums divided by it are the
	// average scores.
	Scored             int   `json:"scored"`
	QualitySum         int64 `json:"quality_sum"`
	SecuritySum        int64 `json:"security_sum"`
	MaintainabilitySum int64 `json:"maintainability_sum"`
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
// QueuedJob is a waiting (or running) job's saved copy, so it survives a
// restart. Owner is the replica that has it queued.
type QueuedJob struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	Owner     string          `json:"owner"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	ReadyAt   time.Time       `json:"ready_at"`
	CreatedAt time.Time       `json:"created_at"`
	StartedAt time.Time       `json:"started_at,omitzero"`
}

// Change request statuses.
const (
	ChangePending   = "pending"
	ChangeApplying  = "applying" // claimed by the approving admin, being replayed
	ChangeApplied   = "applied"
	ChangeFailed    = "failed"
	ChangeRejected  = "rejected"
	ChangeCancelled = "cancelled"
	ChangeExpired   = "expired"
)

// ChangeRequest is a change an operator asked for that waits for an
// admin. Body is the request body, encrypted with the console's secret
// key (it can hold tokens); Preview is the same with secret values
// masked, for display.
type ChangeRequest struct {
	ID           int64           `json:"id"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Summary      string          `json:"summary"`
	Preview      json.RawMessage `json:"preview"`
	Body         string          `json:"-"`
	RequestedBy  string          `json:"requested_by"`
	RequestedAt  time.Time       `json:"requested_at"`
	Status       string          `json:"status"`
	DecidedBy    string          `json:"decided_by,omitempty"`
	DecidedAt    time.Time       `json:"decided_at,omitzero"`
	ResultStatus int             `json:"result_status,omitempty"`
	// Result is what an applied or failed request answered, or why it was
	// rejected (never the response body, which can hold a new secret).
	Result string `json:"result,omitempty"`
}

// AuditFilter narrows SearchAudit. Zero values match everything.
type AuditFilter struct {
	Actor  string // substring, case-insensitive
	Action string // exact, or a prefix when it ends in "*" or "." (host.*)
	Target string // substring, case-insensitive
	// Query is a case-insensitive substring of the target or the details.
	Query    string
	From, To time.Time // at within [From, To)
	// BeforeID pages back: only entries older than this one.
	BeforeID int64
	Limit    int // default 100, at most 1000
}

// Prefix reports whether the action filter is a prefix, and returns it.
func (f AuditFilter) Prefix() (string, bool) {
	if p, ok := strings.CutSuffix(f.Action, "*"); ok {
		return p, true
	}
	if strings.HasSuffix(f.Action, ".") {
		return f.Action, true
	}
	return f.Action, false
}

type AuditEntry struct {
	ID     int64           `json:"id"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target"`
	Detail json.RawMessage `json:"detail"`
	// IP and UserAgent are where the request came from ("" for changes
	// the console made itself, such as the copy from the config file).
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
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

	// Webhook endpoints. PutWebhookEndpoint creates or updates by ID;
	// deleting a host deletes its endpoints. Audit details never include
	// secrets.
	ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error)
	PutWebhookEndpoint(ctx context.Context, e WebhookEndpoint, actor string) (*WebhookEndpoint, error)
	DeleteWebhookEndpoint(ctx context.Context, id string, actor string) error
	// Host credentials. PutHostCredential creates (ID 0) or updates by ID;
	// a host's pattern is unique (ErrDuplicate); deleting a host deletes
	// its credentials. Audit details never include secrets.
	ListHostCredentials(ctx context.Context) ([]HostCredential, error)
	PutHostCredential(ctx context.Context, c HostCredential, actor string) (*HostCredential, error)
	DeleteHostCredential(ctx context.Context, id int64, actor string) error
	// Status dashboards. PutDashboard creates (ID 0) or updates by ID and
	// replaces its repositories and patterns; slugs are unique
	// (ErrDuplicate). Deleting a repository detaches it everywhere.
	ListDashboards(ctx context.Context) ([]Dashboard, error)
	PutDashboard(ctx context.Context, d Dashboard, actor string) (*Dashboard, error)
	DeleteDashboard(ctx context.Context, id int64, actor string) error
	// HostsVersion changes whenever a host, webhook endpoint or credential is written
	// or deleted, by any process, so a running console can tell that its
	// copy is stale (issue #101).
	HostsVersion(ctx context.Context) (int64, error)

	// Learnings. CreateLearning ignores a duplicate SourceRef (returns
	// ErrDuplicate).
	ListLearnings(ctx context.Context) ([]Learning, error)
	CreateLearning(ctx context.Context, l Learning, actor string) (*Learning, error)
	UpdateLearning(ctx context.Context, l Learning, actor string) (*Learning, error)
	DeleteLearning(ctx context.Context, id int64, actor string) error

	// Webhook history.
	InsertWebhookEvents(ctx context.Context, events []WebhookEvent) error
	ListWebhookEvents(ctx context.Context, f WebhookFilter) ([]WebhookEvent, error)
	// CountWebhookEvents counts events by outcome since a time.
	CountWebhookEvents(ctx context.Context, since time.Time) (map[string]int, error)
	// HookStats summarises verified deliveries per host.
	HookStats(ctx context.Context) ([]HostHookStats, error)
	// EndpointHookStats summarises deliveries per webhook URL, counting
	// those received since since.
	EndpointHookStats(ctx context.Context, since time.Time) ([]EndpointHookStats, error)
	// PruneWebhookEvents deletes events older than before and keeps at most
	// keep rows.
	PruneWebhookEvents(ctx context.Context, before time.Time, keep int) (int64, error)
	// MarkSeen records a webhook replay key (a signed delivery ID or a hash
	// of a verified body). first is false when the key was recorded within
	// ttl, so the delivery was already processed by this or another
	// console. Shared by all replicas and kept across restarts.
	MarkSeen(ctx context.Context, key string, ttl time.Duration) (first bool, err error)
	// PruneSeen deletes replay keys recorded before before.
	PruneSeen(ctx context.Context, before time.Time) (int64, error)

	// InsertJob saves a finished job and adds it to the metric rollups.
	InsertJob(ctx context.Context, j Job) (int64, error)
	// TokensUsed sums input and output tokens of jobs finished in the UTC
	// day containing at (from the daily rollup); empty repo sums all.
	TokensUsed(ctx context.Context, at time.Time, platform, host, repo string) (int64, error)
	// Metrics returns rollup buckets, oldest first.
	Metrics(ctx context.Context, f MetricsFilter) ([]MetricPoint, error)
	// PruneJobs deletes jobs finished before a time, and hourly rollups
	// older than hourlyBefore (daily and monthly rollups are kept).
	PruneJobs(ctx context.Context, before, hourlyBefore time.Time) (int64, error)
	RecentJobs(ctx context.Context, limit int) ([]Job, error)
	// Impact summarises what the console did in a period, from rollups
	// (see impact.go).
	Impact(ctx context.Context, f ImpactFilter) (*Impact, error)
	// RepoScores returns each repository's latest scored review, with its
	// daily score trend since since.
	RepoScores(ctx context.Context, since time.Time) ([]RepoScore, error)
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
	// PutSettingAudited stores value but records detail in the audit log
	// (for values that must not be copied there, like a license key).
	PutSettingAudited(ctx context.Context, key string, value, detail any, actor string) error

	CreateSession(ctx context.Context, idHash []byte, s Session) error
	GetSession(ctx context.Context, idHash []byte) (*Session, error)
	// TouchSession records a request on the session: its time and IP
	// (at most once a minute per session, to keep writes down).
	TouchSession(ctx context.Context, idHash []byte, ip string) error
	DeleteSession(ctx context.Context, idHash []byte) error
	// ListSessions returns the sessions that haven't expired, newest first.
	ListSessions(ctx context.Context) ([]Session, error)
	// DeleteSessionByID ends one session (ErrNotFound if there is none).
	DeleteSessionByID(ctx context.Context, id int64) (*Session, error)
	// DeleteOtherSessions ends every session except keep's; it returns how many.
	DeleteOtherSessions(ctx context.Context, keep []byte) (int, error)

	Audit(ctx context.Context, actor, action, target string, detail any) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
	// SearchAudit returns entries matching f, newest first.
	SearchAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error)
	// Saved copies of queued jobs (survive restarts; see QueuedJob).
	PutQueued(ctx context.Context, q QueuedJob) error
	// UpdateQueued changes a saved job's payload and ready time, if it
	// still exists (it never inserts).
	UpdateQueued(ctx context.Context, id string, payload json.RawMessage, readyAt time.Time) error
	StartQueued(ctx context.Context, id string) error
	DeleteQueued(ctx context.Context, id string) error
	// ClaimQueued gives owner the saved jobs of replicas not seen for
	// stale (their heartbeat stopped), once: each job goes to one replica.
	ClaimQueued(ctx context.Context, owner string, stale time.Duration) ([]QueuedJob, error)
	// HeartbeatOwner records that owner is alive; RemoveOwner that it
	// stopped (its jobs can be claimed at once).
	HeartbeatOwner(ctx context.Context, owner string) error
	RemoveOwner(ctx context.Context, owner string) error
	// Change requests (operators' changes waiting for an admin).
	CreateChangeRequest(ctx context.Context, c ChangeRequest) (*ChangeRequest, error)
	GetChangeRequest(ctx context.Context, id int64) (*ChangeRequest, error)
	// ListChangeRequests returns the newest first; status "" lists all.
	ListChangeRequests(ctx context.Context, status string, limit int) ([]ChangeRequest, error)
	// DecideChangeRequest moves a pending request to status, once: it
	// returns ErrNotFound when the request isn't pending any more.
	DecideChangeRequest(ctx context.Context, id int64, status, by string, resultStatus int, result string) (*ChangeRequest, error)
	// ClaimChangeRequest moves a pending request to applying for by, once,
	// keeping its body: only the claimer replays it (issue #126). It
	// returns ErrNotFound when the request isn't pending any more.
	ClaimChangeRequest(ctx context.Context, id int64, by string) (*ChangeRequest, error)
	// FinishChangeRequest records how an applying request ended (applied
	// or failed), drops its body and audits it; ErrNotFound when it isn't
	// applying.
	FinishChangeRequest(ctx context.Context, id int64, status, by string, resultStatus int, result string) (*ChangeRequest, error)

	Close()
}

// Postgres implements Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// Open connects and applies migrations.
func Open(ctx context.Context, url string) (*Postgres, error) {
	p, err := Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := p.migrate(ctx); err != nil {
		p.pool.Close()
		return nil, err
	}
	return p, nil
}

// Connect connects without applying migrations, for commands that only
// read (check-config), so running a newer binary never changes the schema
// under the console that is serving.
func Connect(ctx context.Context, url string) (*Postgres, error) {
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
	return &Postgres{pool: pool}, nil
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
	// 7: metric rollups (hour, day, month) and indexes for review history
	`CREATE TABLE job_metrics (
		granularity  text NOT NULL,
		bucket       timestamptz NOT NULL,
		platform     text NOT NULL,
		host         text NOT NULL,
		repo         text NOT NULL,
		jobs         integer NOT NULL DEFAULT 0,
		done         integer NOT NULL DEFAULT 0,
		skipped      integer NOT NULL DEFAULT 0,
		failed       integer NOT NULL DEFAULT 0,
		comments     integer NOT NULL DEFAULT 0,
		files        integer NOT NULL DEFAULT 0,
		tokens_in    bigint NOT NULL DEFAULT 0,
		tokens_out   bigint NOT NULL DEFAULT 0,
		duration_ms  bigint NOT NULL DEFAULT 0,
		PRIMARY KEY (granularity, bucket, platform, host, repo)
	);
	CREATE INDEX job_metrics_bucket_idx ON job_metrics (granularity, bucket);
	INSERT INTO job_metrics (granularity, bucket, platform, host, repo, jobs, done, skipped, failed, comments, files, tokens_in, tokens_out, duration_ms)
	SELECT g, date_trunc(g, finished AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', platform, host, lower(repo), count(*),
		count(*) FILTER (WHERE state = 'done'), count(*) FILTER (WHERE state = 'skipped'), count(*) FILTER (WHERE state = 'failed'),
		sum(comments), sum(files), sum(tokens_in), sum(tokens_out),
		coalesce(sum(extract(epoch FROM (finished - started)) * 1000) FILTER (WHERE started IS NOT NULL), 0)::bigint
	FROM jobs, unnest(ARRAY['hour', 'day', 'month']) AS g
	GROUP BY 1, 2, 3, 4, 5;
	CREATE INDEX jobs_host_idx ON jobs (platform, host, id DESC);
	CREATE INDEX jobs_state_idx ON jobs (state, id DESC);`,
	// 8: team learnings
	`CREATE TABLE learnings (
		id             bigserial PRIMARY KEY,
		platform       text NOT NULL,
		host           text NOT NULL,
		scope          text NOT NULL DEFAULT '',
		text           text NOT NULL,
		path           text NOT NULL DEFAULT '',
		status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'disabled')),
		source         text NOT NULL DEFAULT 'manual',
		source_url     text NOT NULL DEFAULT '',
		source_author  text NOT NULL DEFAULT '',
		source_ref     text NOT NULL DEFAULT '',
		created_at     timestamptz NOT NULL DEFAULT now(),
		updated_at     timestamptz NOT NULL DEFAULT now(),
		updated_by     text NOT NULL DEFAULT ''
	);
	CREATE INDEX learnings_scope_idx ON learnings (platform, host, status);
	CREATE UNIQUE INDEX learnings_source_ref_idx ON learnings (source_ref) WHERE source_ref <> '';`,
	// 9: webhook endpoints (one URL per webhook, each with its own secrets)
	`CREATE TABLE webhook_endpoints (
		id          uuid PRIMARY KEY,
		host_id     bigint NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
		name        text NOT NULL DEFAULT '',
		enabled     boolean NOT NULL DEFAULT true,
		secrets     text NOT NULL DEFAULT '',
		created_at  timestamptz NOT NULL DEFAULT now(),
		created_by  text NOT NULL DEFAULT '',
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT ''
	);
	CREATE INDEX webhook_endpoints_host_idx ON webhook_endpoints (host_id);
	ALTER TABLE webhook_events ADD COLUMN endpoint text NOT NULL DEFAULT '';`,
	// 10: sessions keep the sign-in claims (re-checked on every request) and
	// an ID to list and revoke them. Sessions from before have groups NULL.
	`ALTER TABLE sessions ADD COLUMN id bigserial, ADD COLUMN groups jsonb, ADD COLUMN verified boolean NOT NULL DEFAULT true;
	CREATE UNIQUE INDEX sessions_id_idx ON sessions (id);`,
	// 11: the role of a learning's author on the repository (issue #32).
	`ALTER TABLE learnings ADD COLUMN source_role text NOT NULL DEFAULT '';`,
	// 12: webhook replay keys shared by every replica (issue #34).
	`CREATE TABLE webhook_seen (
		key     text PRIMARY KEY,
		seen_at timestamptz NOT NULL DEFAULT now()
	);
	CREATE INDEX webhook_seen_at_idx ON webhook_seen (seen_at);`,
	// 13: where sign-ins, changes and webhook deliveries come from (issue #66).
	`ALTER TABLE audit_log ADD COLUMN ip text NOT NULL DEFAULT '', ADD COLUMN user_agent text NOT NULL DEFAULT '';
	ALTER TABLE sessions ADD COLUMN ip text NOT NULL DEFAULT '', ADD COLUMN user_agent text NOT NULL DEFAULT '', ADD COLUMN last_seen timestamptz;
	ALTER TABLE webhook_events ADD COLUMN source_ip text NOT NULL DEFAULT '';`,
	// 14: changes operators asked for, waiting for an admin (issue #68).
	`CREATE TABLE change_requests (
		id            bigserial PRIMARY KEY,
		method        text NOT NULL,
		path          text NOT NULL,
		summary       text NOT NULL,
		preview       jsonb NOT NULL DEFAULT '{}',
		body          text NOT NULL DEFAULT '',
		requested_by  text NOT NULL,
		requested_at  timestamptz NOT NULL DEFAULT now(),
		status        text NOT NULL DEFAULT 'pending',
		decided_by    text NOT NULL DEFAULT '',
		decided_at    timestamptz,
		result_status int NOT NULL DEFAULT 0,
		result        text NOT NULL DEFAULT ''
	);
	CREATE INDEX change_requests_status_idx ON change_requests (status, id DESC);`,
	// 15: a webhook with a signing token takes signed deliveries only,
	// unless this is on (issue #86).
	`ALTER TABLE webhook_endpoints ADD COLUMN accept_secret_token boolean NOT NULL DEFAULT false;`,
	// 16: waiting jobs survive restarts; replicas take over a stopped
	// replica's jobs (issue #88).
	`CREATE TABLE queued_jobs (
		id         text PRIMARY KEY,
		key        text NOT NULL,
		owner      text NOT NULL,
		kind       text NOT NULL,
		payload    jsonb NOT NULL,
		ready_at   timestamptz NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now(),
		started_at timestamptz
	);
	CREATE INDEX queued_jobs_owner_idx ON queued_jobs (owner);
	CREATE TABLE queue_owners (
		owner   text PRIMARY KEY,
		seen_at timestamptz NOT NULL DEFAULT now()
	);`,
	// 17: review scores per job and in the rollups (issue #98), filled in
	// from the review results already stored.
	`ALTER TABLE jobs ADD COLUMN score_quality smallint, ADD COLUMN score_security smallint, ADD COLUMN score_maintainability smallint;
	CREATE INDEX jobs_scored_idx ON jobs (platform, host, lower(repo), id DESC) WHERE score_quality IS NOT NULL;
	ALTER TABLE job_metrics ADD COLUMN scored integer NOT NULL DEFAULT 0, ADD COLUMN quality_sum bigint NOT NULL DEFAULT 0,
		ADD COLUMN security_sum bigint NOT NULL DEFAULT 0, ADD COLUMN maintainability_sum bigint NOT NULL DEFAULT 0;` + backfillScores,
	// 18: a version that host and webhook writes bump, so every replica
	// (and changes from "console hosts import") reloads them (issue #101).
	hostsVersionSQL,
	// 19: Impact page rollups: findings by severity and category, review
	// runs, reviewed merge requests (issue #102).
	impactSQL,
	// 20: rules can turn the bot's approval on or off.
	`ALTER TABLE repo_rules ADD COLUMN approve boolean;`,
	// 21: what each review did with the bot's approval, and approvals
	// given per bucket for Impact (issue #115).
	approvalSQL,
	// 22: per-webhook delivery stats on the Code hosts page (issue #131).
	`CREATE INDEX webhook_events_endpoint_idx ON webhook_events (endpoint, id DESC) WHERE endpoint <> '';`,
	// 23: credentials per group or pattern under a host (issue #136).
	`CREATE TABLE host_credentials (
		id          bigserial PRIMARY KEY,
		host_id     bigint NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
		name        text NOT NULL DEFAULT '',
		pattern     text NOT NULL,
		enabled     boolean NOT NULL DEFAULT true,
		config      jsonb NOT NULL DEFAULT '{}',
		secrets     text NOT NULL DEFAULT '',
		created_at  timestamptz NOT NULL DEFAULT now(),
		created_by  text NOT NULL DEFAULT '',
		updated_at  timestamptz NOT NULL DEFAULT now(),
		updated_by  text NOT NULL DEFAULT '',
		UNIQUE (host_id, pattern)
	);
	CREATE TRIGGER host_credentials_version_bump AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON host_credentials
		FOR EACH STATEMENT EXECUTE FUNCTION bump_hosts_version();`,
	// 24: several status dashboards, each with its repositories (issue #155).
	`CREATE TABLE dashboards (
		id            bigserial PRIMARY KEY,
		slug          text NOT NULL UNIQUE,
		title         text NOT NULL DEFAULT '',
		description   text NOT NULL DEFAULT '',
		access        text NOT NULL DEFAULT 'admins',
		hide_repos    boolean NOT NULL DEFAULT false,
		all_repos     boolean NOT NULL DEFAULT false,
		enabled       boolean NOT NULL DEFAULT true,
		token_hash    text NOT NULL DEFAULT '',
		token_expires timestamptz,
		created_at    timestamptz NOT NULL DEFAULT now(),
		created_by    text NOT NULL DEFAULT '',
		updated_at    timestamptz NOT NULL DEFAULT now(),
		updated_by    text NOT NULL DEFAULT ''
	);
	CREATE TABLE dashboard_repos (
		dashboard_id bigint NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
		repo_id      bigint NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
		PRIMARY KEY (dashboard_id, repo_id)
	);
	CREATE INDEX dashboard_repos_repo_idx ON dashboard_repos (repo_id);
	CREATE TABLE dashboard_patterns (
		dashboard_id bigint NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
		platform     text NOT NULL,
		host         text NOT NULL,
		pattern      text NOT NULL,
		PRIMARY KEY (dashboard_id, platform, host, pattern)
	);`,
}

// approvalSQL adds jobs.approval and job_metrics.approvals, filled in
// from stored results (migration 21).
const approvalSQL = `
	ALTER TABLE jobs ADD COLUMN approval text NOT NULL DEFAULT '';
	UPDATE jobs SET approval = result->>'approval'
	WHERE jsonb_typeof(result) = 'object' AND result->>'approval' IN ('approved', 'already_approved', 'withdrawn', 'not_approved', 'unchanged', 'failed');
	CREATE INDEX jobs_approval_idx ON jobs (approval, id DESC) WHERE approval <> '';
	ALTER TABLE job_metrics ADD COLUMN approvals integer NOT NULL DEFAULT 0;
	UPDATE job_metrics m SET approvals = s.n
	FROM (SELECT g, date_trunc(g, finished AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket, platform, host, lower(repo) AS repo, count(*) AS n
		FROM jobs, unnest(ARRAY['hour', 'day', 'month']) AS g WHERE approval = 'approved' GROUP BY 1, 2, 3, 4, 5) s
	WHERE m.granularity = s.g AND m.bucket = s.bucket AND m.platform = s.platform AND m.host = s.host AND m.repo = s.repo;`

// Approval outcomes the CLI reports (its JSON "approval").
var approvalOutcomes = map[string]bool{"approved": true, "already_approved": true, "withdrawn": true, "not_approved": true, "unchanged": true, "failed": true}

// ApprovalOf is the approval outcome in a review result, or "".
func ApprovalOf(result json.RawMessage) string {
	var r struct {
		Approval string `json:"approval"`
	}
	if len(result) == 0 || json.Unmarshal(result, &r) != nil || !approvalOutcomes[r.Approval] {
		return ""
	}
	return r.Approval
}

// backfillScores fills in the scores of jobs whose stored result has them
// (all three 1..100) and adds them to the rollups (migration 17).
const backfillScores = `
	UPDATE jobs SET score_quality = (result->'scores'->>'quality')::int, score_security = (result->'scores'->>'security')::int,
		score_maintainability = (result->'scores'->>'maintainability')::int
	WHERE score_quality IS NULL AND jsonb_typeof(result->'scores') = 'object'
		AND (result->'scores'->>'quality') ~ '^[0-9]{1,3}$' AND (result->'scores'->>'security') ~ '^[0-9]{1,3}$'
		AND (result->'scores'->>'maintainability') ~ '^[0-9]{1,3}$'
		AND (result->'scores'->>'quality')::int BETWEEN 1 AND 100 AND (result->'scores'->>'security')::int BETWEEN 1 AND 100
		AND (result->'scores'->>'maintainability')::int BETWEEN 1 AND 100;
	UPDATE job_metrics m SET scored = s.n, quality_sum = s.q, security_sum = s.sec, maintainability_sum = s.mt
	FROM (SELECT g, date_trunc(g, finished AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket, platform, host, lower(repo) AS repo,
			count(*) AS n, sum(score_quality) AS q, sum(score_security) AS sec, sum(score_maintainability) AS mt
		FROM jobs, unnest(ARRAY['hour', 'day', 'month']) AS g WHERE score_quality IS NOT NULL GROUP BY 1, 2, 3, 4, 5) s
	WHERE m.granularity = s.g AND m.bucket = s.bucket AND m.platform = s.platform AND m.host = s.host AND m.repo = s.repo;`

// hostsVersionSQL keeps a counter that every write to hosts or
// webhook_endpoints bumps (migration 18).
const hostsVersionSQL = `
	CREATE TABLE hosts_version (id integer PRIMARY KEY CHECK (id = 1), version bigint NOT NULL);
	INSERT INTO hosts_version VALUES (1, 1);
	CREATE FUNCTION bump_hosts_version() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
		UPDATE hosts_version SET version = version + 1 WHERE id = 1;
		RETURN NULL;
	END $$;
	CREATE TRIGGER hosts_version_bump AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON hosts
		FOR EACH STATEMENT EXECUTE FUNCTION bump_hosts_version();
	CREATE TRIGGER webhook_endpoints_version_bump AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON webhook_endpoints
		FOR EACH STATEMENT EXECUTE FUNCTION bump_hosts_version();`

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

const ruleCols = `id, platform, host, pattern, enabled, require_reviewer, skip_branch_sync, score_labels, commit_status, approve, settings, updated_at, updated_by`

func scanRule(row pgx.Row) (*Rule, error) {
	var r Rule
	var settings []byte
	err := row.Scan(&r.ID, &r.Platform, &r.Host, &r.Pattern, &r.Enabled, &r.RequireReviewer, &r.SkipBranchSync, &r.ScoreLabels, &r.CommitStatus, &r.Approve, &settings, &r.UpdatedAt, &r.UpdatedBy)
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
		"score_labels": r.ScoreLabels, "commit_status": r.CommitStatus, "approve": r.Approve, "settings": r.Settings}
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
		INSERT INTO repo_rules (platform, host, pattern, enabled, require_reviewer, skip_branch_sync, score_labels, commit_status, approve, settings, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (platform, host, pattern) DO UPDATE SET enabled = EXCLUDED.enabled, require_reviewer = EXCLUDED.require_reviewer,
			skip_branch_sync = EXCLUDED.skip_branch_sync, score_labels = EXCLUDED.score_labels, commit_status = EXCLUDED.commit_status,
			approve = EXCLUDED.approve, settings = EXCLUDED.settings, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+ruleCols, platform, host, pattern, in.Enabled, in.RequireReviewer, in.SkipBranchSync, in.ScoreLabels, in.CommitStatus, in.Approve, js, actor))
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
		UPDATE repo_rules SET enabled=$2, require_reviewer=$3, skip_branch_sync=$4, score_labels=$5, commit_status=$6, approve=$7, settings=$8,
			updated_at=now(), updated_by=$9 WHERE id=$1
		RETURNING `+ruleCols, id, in.Enabled, in.RequireReviewer, in.SkipBranchSync, in.ScoreLabels, in.CommitStatus, in.Approve, js, actor))
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

const learningCols = `id, platform, host, scope, text, path, status, source, source_url, source_author, source_role, source_ref, created_at, updated_at, updated_by`

func scanLearning(row pgx.Row) (*Learning, error) {
	var l Learning
	err := row.Scan(&l.ID, &l.Platform, &l.Host, &l.Scope, &l.Text, &l.Path, &l.Status, &l.Source, &l.SourceURL, &l.SourceAuthor,
		&l.SourceRole, &l.SourceRef, &l.CreatedAt, &l.UpdatedAt, &l.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

func learningAudit(l *Learning) map[string]any {
	return map[string]any{"scope": l.Scope, "text": l.Text, "path": l.Path, "status": l.Status, "source": l.Source, "source_url": l.SourceURL,
		"source_author": l.SourceAuthor, "source_role": l.SourceRole}
}

func (p *Postgres) ListLearnings(ctx context.Context) ([]Learning, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+learningCols+` FROM learnings ORDER BY platform, host, scope, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Learning{}
	for rows.Next() {
		l, err := scanLearning(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (p *Postgres) CreateLearning(ctx context.Context, in Learning, actor string) (*Learning, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	l, err := scanLearning(tx.QueryRow(ctx, `
		INSERT INTO learnings (platform, host, scope, text, path, status, source, source_url, source_author, source_ref, updated_by, source_role)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (source_ref) WHERE source_ref <> '' DO NOTHING
		RETURNING `+learningCols, strings.ToLower(in.Platform), strings.ToLower(in.Host), in.Scope, in.Text, in.Path, in.Status,
		in.Source, in.SourceURL, in.SourceAuthor, in.SourceRef, actor, in.SourceRole))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrDuplicate
	}
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, actor, "learning.create", l.Platform+":"+l.Host+"/"+l.Scope, learningAudit(l)); err != nil {
		return nil, err
	}
	return l, tx.Commit(ctx)
}

func (p *Postgres) UpdateLearning(ctx context.Context, in Learning, actor string) (*Learning, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	before, err := scanLearning(tx.QueryRow(ctx, `SELECT `+learningCols+` FROM learnings WHERE id=$1 FOR UPDATE`, in.ID))
	if err != nil {
		return nil, err
	}
	after, err := scanLearning(tx.QueryRow(ctx, `
		UPDATE learnings SET scope=$2, text=$3, path=$4, status=$5, updated_at=now(), updated_by=$6 WHERE id=$1
		RETURNING `+learningCols, in.ID, in.Scope, in.Text, in.Path, in.Status, actor))
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"before": learningAudit(before), "after": learningAudit(after)}
	if err := audit(ctx, tx, actor, "learning.update", after.Platform+":"+after.Host+"/"+after.Scope, detail); err != nil {
		return nil, err
	}
	return after, tx.Commit(ctx)
}

func (p *Postgres) DeleteLearning(ctx context.Context, id int64, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	l, err := scanLearning(tx.QueryRow(ctx, `DELETE FROM learnings WHERE id=$1 RETURNING `+learningCols, id))
	if err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "learning.delete", l.Platform+":"+l.Host+"/"+l.Scope, learningAudit(l)); err != nil {
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
	if h == nil {
		return nil
	}
	return map[string]any{"base_url": h.BaseURL, "name": h.Name, "enabled": h.Enabled, "config": h.Config}
}

// changeDetail is an audit detail with the state before and after a save,
// and the names of the secrets it changed.
func changeDetail(before, after any, secrets []string) map[string]any {
	d := map[string]any{"before": before, "after": after}
	if secrets != nil {
		d["secrets_changed"] = secrets
	}
	return d
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
	old, err := scanHost(tx.QueryRow(ctx, `SELECT `+hostCols+` FROM hosts WHERE platform = $1 AND host = $2 FOR UPDATE`,
		strings.ToLower(in.Platform), strings.ToLower(in.Host)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	h, err := scanHost(tx.QueryRow(ctx, `
		INSERT INTO hosts (platform, host, base_url, name, enabled, config, secrets, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (platform, host) DO UPDATE SET base_url = EXCLUDED.base_url, name = EXCLUDED.name, enabled = EXCLUDED.enabled,
			config = EXCLUDED.config, secrets = EXCLUDED.secrets, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+hostCols, strings.ToLower(in.Platform), strings.ToLower(in.Host), in.BaseURL, in.Name, in.Enabled, in.Config, in.Secrets, actor))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, actor, "host.put", h.Platform+":"+h.Host, changeDetail(hostAudit(old), hostAudit(h), nonNil(in.SecretsChanged))); err != nil {
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
	if err := audit(ctx, tx, actor, "host.delete", h.Platform+":"+h.Host, map[string]any{"before": hostAudit(h)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const endpointCols = `id::text, host_id, name, enabled, secrets, created_at, created_by, updated_at, updated_by, accept_secret_token`

func scanEndpoint(row pgx.Row) (*WebhookEndpoint, error) {
	var e WebhookEndpoint
	err := row.Scan(&e.ID, &e.HostID, &e.Name, &e.Enabled, &e.Secrets, &e.CreatedAt, &e.CreatedBy, &e.UpdatedAt, &e.UpdatedBy, &e.AcceptSecretToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

func endpointAudit(e *WebhookEndpoint) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{"host_id": e.HostID, "name": e.Name, "enabled": e.Enabled, "accept_secret_token": e.AcceptSecretToken}
}

func (p *Postgres) HostsVersion(ctx context.Context) (int64, error) {
	var v int64
	err := p.pool.QueryRow(ctx, `SELECT version FROM hosts_version WHERE id = 1`).Scan(&v)
	return v, err
}

func (p *Postgres) ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints ORDER BY host_id, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebhookEndpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (p *Postgres) PutWebhookEndpoint(ctx context.Context, in WebhookEndpoint, actor string) (*WebhookEndpoint, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	old, err := scanEndpoint(tx.QueryRow(ctx, `SELECT `+endpointCols+` FROM webhook_endpoints WHERE id::text = $1 FOR UPDATE`, in.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	e, err := scanEndpoint(tx.QueryRow(ctx, `
		INSERT INTO webhook_endpoints (id, host_id, name, enabled, secrets, created_by, updated_by, accept_secret_token) VALUES ($1, $2, $3, $4, $5, $6, $6, $7)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, enabled = EXCLUDED.enabled, secrets = EXCLUDED.secrets,
			accept_secret_token = EXCLUDED.accept_secret_token, updated_by = EXCLUDED.updated_by, updated_at = now()
		WHERE webhook_endpoints.host_id = EXCLUDED.host_id
		RETURNING `+endpointCols, in.ID, in.HostID, in.Name, in.Enabled, in.Secrets, actor, in.AcceptSecretToken))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, actor, "webhook.put", e.ID, changeDetail(endpointAudit(old), endpointAudit(e), nonNil(in.SecretsChanged))); err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

func (p *Postgres) DeleteWebhookEndpoint(ctx context.Context, id string, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	e, err := scanEndpoint(tx.QueryRow(ctx, `DELETE FROM webhook_endpoints WHERE id::text = $1 RETURNING `+endpointCols, id))
	if err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "webhook.delete", e.ID, map[string]any{"before": endpointAudit(e)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const dashboardCols = `id, slug, title, description, access, hide_repos, all_repos, enabled, token_hash, token_expires, created_at, created_by, updated_at, updated_by`

func scanDashboard(row pgx.Row) (*Dashboard, error) {
	var d Dashboard
	var exp *time.Time
	err := row.Scan(&d.ID, &d.Slug, &d.Title, &d.Description, &d.Access, &d.HideRepos, &d.AllRepos, &d.Enabled, &d.TokenHash, &exp,
		&d.CreatedAt, &d.CreatedBy, &d.UpdatedAt, &d.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if exp != nil {
		d.TokenExpires = *exp
	}
	d.Repos, d.Patterns = []DashboardRepo{}, []DashboardPattern{}
	return &d, err
}

// dashboardAudit is what the audit log keeps of a dashboard: never its
// token or token hash.
func dashboardAudit(d *Dashboard) map[string]any {
	if d == nil {
		return nil
	}
	repos := []string{}
	for _, r := range d.Repos {
		repos = append(repos, r.Platform+":"+r.Host+"/"+r.Path)
	}
	patterns := []string{}
	for _, p := range d.Patterns {
		patterns = append(patterns, p.Platform+":"+p.Host+"/"+p.Pattern)
	}
	out := map[string]any{"slug": d.Slug, "title": d.Title, "description": d.Description, "access": d.Access, "hide_repos": d.HideRepos,
		"all_repos": d.AllRepos, "enabled": d.Enabled, "repos": repos, "patterns": patterns, "token": d.TokenHash != ""}
	if !d.TokenExpires.IsZero() {
		out["token_expires"] = d.TokenExpires.UTC().Format(time.RFC3339)
	}
	return out
}

// dashboardMembers fills in the repositories and patterns of ds (by ID).
func dashboardMembers(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ds map[int64]*Dashboard) error {
	rows, err := q.Query(ctx, `SELECT d.dashboard_id, r.id, r.platform, r.host, r.path FROM dashboard_repos d JOIN repos r ON r.id = d.repo_id ORDER BY r.platform, r.host, r.path`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var r DashboardRepo
		if err := rows.Scan(&id, &r.ID, &r.Platform, &r.Host, &r.Path); err != nil {
			rows.Close()
			return err
		}
		if d := ds[id]; d != nil {
			d.Repos = append(d.Repos, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT dashboard_id, platform, host, pattern FROM dashboard_patterns ORDER BY platform, host, pattern`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p DashboardPattern
		if err := rows.Scan(&id, &p.Platform, &p.Host, &p.Pattern); err != nil {
			return err
		}
		if d := ds[id]; d != nil {
			d.Patterns = append(d.Patterns, p)
		}
	}
	return rows.Err()
}

func (p *Postgres) ListDashboards(ctx context.Context) ([]Dashboard, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+dashboardCols+` FROM dashboards ORDER BY slug = 'default' DESC, title, slug`)
	if err != nil {
		return nil, err
	}
	var list []*Dashboard
	by := map[int64]*Dashboard{}
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, d)
		by[d.ID] = d
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := dashboardMembers(ctx, p.pool, by); err != nil {
		return nil, err
	}
	out := make([]Dashboard, 0, len(list))
	for _, d := range list {
		out = append(out, *d)
	}
	return out, nil
}

// dashboardTx reads one dashboard with its members inside tx.
func dashboardTx(ctx context.Context, tx pgx.Tx, id int64, lock bool) (*Dashboard, error) {
	q := `SELECT ` + dashboardCols + ` FROM dashboards WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	d, err := scanDashboard(tx.QueryRow(ctx, q, id))
	if err != nil {
		return nil, err
	}
	all := map[int64]*Dashboard{d.ID: d}
	return d, dashboardMembers(ctx, tx, all)
}

func (p *Postgres) PutDashboard(ctx context.Context, in Dashboard, actor string) (*Dashboard, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var exp *time.Time
	if !in.TokenExpires.IsZero() {
		exp = &in.TokenExpires
	}
	var old *Dashboard
	id := in.ID
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO dashboards (slug, title, description, access, hide_repos, all_repos, enabled, token_hash, token_expires, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10) RETURNING id`,
			in.Slug, in.Title, in.Description, in.Access, in.HideRepos, in.AllRepos, in.Enabled, in.TokenHash, exp, actor).Scan(&id)
	} else {
		if old, err = dashboardTx(ctx, tx, id, true); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE dashboards SET slug = $2, title = $3, description = $4, access = $5, hide_repos = $6, all_repos = $7, enabled = $8,
			token_hash = $9, token_expires = $10, updated_by = $11, updated_at = now() WHERE id = $1`,
			id, in.Slug, in.Title, in.Description, in.Access, in.HideRepos, in.AllRepos, in.Enabled, in.TokenHash, exp, actor)
	}
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM dashboard_repos WHERE dashboard_id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM dashboard_patterns WHERE dashboard_id = $1`, id); err != nil {
		return nil, err
	}
	for _, r := range in.Repos {
		// A repository deleted meanwhile is skipped (the join finds nothing).
		if _, err := tx.Exec(ctx, `INSERT INTO dashboard_repos (dashboard_id, repo_id) SELECT $1, id FROM repos WHERE id = $2 ON CONFLICT DO NOTHING`, id, r.ID); err != nil {
			return nil, err
		}
	}
	for _, pt := range in.Patterns {
		if _, err := tx.Exec(ctx, `INSERT INTO dashboard_patterns (dashboard_id, platform, host, pattern) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			id, pt.Platform, pt.Host, pt.Pattern); err != nil {
			return nil, err
		}
	}
	d, err := dashboardTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	action, detail := "dashboard.update", changeDetail(dashboardAudit(old), dashboardAudit(d), nil)
	if old == nil {
		action = "dashboard.create"
	}
	if in.TokenChange != "" {
		detail["token_change"] = in.TokenChange
	}
	if err := audit(ctx, tx, actor, action, d.Slug, detail); err != nil {
		return nil, err
	}
	if in.TokenChange == "rotated" || in.TokenChange == "set" {
		if err := audit(ctx, tx, actor, "dashboard.token.rotate", d.Slug, map[string]any{"token_expires": dashboardAudit(d)["token_expires"]}); err != nil {
			return nil, err
		}
	}
	return d, tx.Commit(ctx)
}

func (p *Postgres) DeleteDashboard(ctx context.Context, id int64, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	d, err := dashboardTx(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM dashboards WHERE id = $1`, id); err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "dashboard.delete", d.Slug, map[string]any{"before": dashboardAudit(d)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const credentialCols = `id, host_id, name, pattern, enabled, config, secrets, created_at, created_by, updated_at, updated_by`

func scanCredential(row pgx.Row) (*HostCredential, error) {
	var c HostCredential
	err := row.Scan(&c.ID, &c.HostID, &c.Name, &c.Pattern, &c.Enabled, &c.Config, &c.Secrets, &c.CreatedAt, &c.CreatedBy, &c.UpdatedAt, &c.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func credentialAudit(c *HostCredential) map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{"host_id": c.HostID, "name": c.Name, "pattern": c.Pattern, "enabled": c.Enabled, "config": c.Config}
}

func (p *Postgres) ListHostCredentials(ctx context.Context) ([]HostCredential, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+credentialCols+` FROM host_credentials ORDER BY host_id, pattern, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostCredential{}
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (p *Postgres) PutHostCredential(ctx context.Context, in HostCredential, actor string) (*HostCredential, error) {
	if len(in.Config) == 0 {
		in.Config = json.RawMessage(`{}`)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var old *HostCredential
	var c *HostCredential
	if in.ID == 0 {
		c, err = scanCredential(tx.QueryRow(ctx, `INSERT INTO host_credentials (host_id, name, pattern, enabled, config, secrets, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7) RETURNING `+credentialCols, in.HostID, in.Name, in.Pattern, in.Enabled, in.Config, in.Secrets, actor))
	} else {
		old, err = scanCredential(tx.QueryRow(ctx, `SELECT `+credentialCols+` FROM host_credentials WHERE id = $1 AND host_id = $2 FOR UPDATE`, in.ID, in.HostID))
		if err != nil {
			return nil, err
		}
		c, err = scanCredential(tx.QueryRow(ctx, `UPDATE host_credentials SET name = $2, pattern = $3, enabled = $4, config = $5, secrets = $6,
			updated_by = $7, updated_at = now() WHERE id = $1 RETURNING `+credentialCols, in.ID, in.Name, in.Pattern, in.Enabled, in.Config, in.Secrets, actor))
	}
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			switch pe.Code {
			case "23505":
				return nil, ErrDuplicate
			case "23503":
				return nil, ErrNotFound
			}
		}
		return nil, err
	}
	if err := audit(ctx, tx, actor, "credential.put", strconv.FormatInt(c.ID, 10), changeDetail(credentialAudit(old), credentialAudit(c), nonNil(in.SecretsChanged))); err != nil {
		return nil, err
	}
	return c, tx.Commit(ctx)
}

func (p *Postgres) DeleteHostCredential(ctx context.Context, id int64, actor string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	c, err := scanCredential(tx.QueryRow(ctx, `DELETE FROM host_credentials WHERE id = $1 RETURNING `+credentialCols, id))
	if err != nil {
		return err
	}
	if err := audit(ctx, tx, actor, "credential.delete", strconv.FormatInt(c.ID, 10), map[string]any{"before": credentialAudit(c)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const hookCols = `id, received_at, platform, host, project, number, event, kind, action, sender, delivery, endpoint, auth, outcome, reason, status, duration_ms, source_ip`

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
			e.Sender, e.Delivery, e.Endpoint, e.Auth, e.Outcome, e.Reason, e.Status, e.DurationMS, e.SourceIP})
	}
	_, err := p.pool.CopyFrom(ctx, pgx.Identifier{"webhook_events"},
		[]string{"received_at", "platform", "host", "project", "number", "event", "kind", "action", "sender", "delivery", "endpoint", "auth", "outcome", "reason", "status", "duration_ms", "source_ip"},
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
	if f.Endpoint != "" {
		add("endpoint = $%d", f.Endpoint)
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
		e, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanHook reads a row of hookCols.
func scanHook(row pgx.Row) (WebhookEvent, error) {
	var e WebhookEvent
	err := row.Scan(&e.ID, &e.ReceivedAt, &e.Platform, &e.Host, &e.Project, &e.Number, &e.Event, &e.Kind, &e.Action,
		&e.Sender, &e.Delivery, &e.Endpoint, &e.Auth, &e.Outcome, &e.Reason, &e.Status, &e.DurationMS, &e.SourceIP)
	return e, err
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

func (p *Postgres) HookStats(ctx context.Context) ([]HostHookStats, error) {
	rows, err := p.pool.Query(ctx, `SELECT platform, host, count(*),
		count(*) FILTER (WHERE kind = ANY($1) OR event = 'Note Hook'),
		count(*) FILTER (WHERE event = 'System Hook')
		FROM webhook_events WHERE auth IN ('signature', 'token', 'token-unsigned') AND host <> '' GROUP BY platform, host ORDER BY platform, host`, CommentKinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostHookStats{}
	for rows.Next() {
		var s HostHookStats
		if err := rows.Scan(&s.Platform, &s.Host, &s.Verified, &s.Comments, &s.SystemHook); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) EndpointHookStats(ctx context.Context, since time.Time) ([]EndpointHookStats, error) {
	by := map[string]*EndpointHookStats{}
	var order []string
	get := func(id string) *EndpointHookStats {
		if by[id] == nil {
			by[id] = &EndpointHookStats{Endpoint: id}
			order = append(order, id)
		}
		return by[id]
	}
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT ON (endpoint) `+hookCols+` FROM webhook_events
		WHERE endpoint <> '' ORDER BY endpoint, id DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e, err := scanHook(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		get(e.Endpoint).Last = &e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = p.pool.Query(ctx, `SELECT endpoint,
		count(*) FILTER (WHERE outcome NOT IN ('rejected', 'error')),
		count(*) FILTER (WHERE outcome IN ('rejected', 'error'))
		FROM webhook_events WHERE endpoint <> '' AND received_at >= $1 GROUP BY endpoint`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ok, bad int
		if err := rows.Scan(&id, &ok, &bad); err != nil {
			return nil, err
		}
		st := get(id)
		st.Accepted, st.Rejected = ok, bad
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]EndpointHookStats, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	return out, nil
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

// auditInsert records an audit entry with the request's client (from ctx).
const auditInsert = `INSERT INTO audit_log (actor, action, target, detail, ip, user_agent) VALUES ($1, $2, $3, $4, $5, $6)`

func audit(ctx context.Context, tx pgx.Tx, actor, action, target string, detail any) error {
	js, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	c := clientip.From(ctx)
	_, err = tx.Exec(ctx, auditInsert, actor, action, target, js, c.IP, c.UserAgent)
	return err
}

func (p *Postgres) Audit(ctx context.Context, actor, action, target string, detail any) error {
	js, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	c := clientip.From(ctx)
	_, err = p.pool.Exec(ctx, auditInsert, actor, action, target, js, c.IP, c.UserAgent)
	return err
}

// previousAfter is the state an earlier audit detail ended in: its
// "after", or the whole detail for entries from before before/after were
// recorded. The detail was already redacted by whoever wrote it, so no
// secret comes back this way.
func previousAfter(detail json.RawMessage) any {
	if len(detail) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(detail, &m); err == nil {
		if a, ok := m["after"]; ok {
			if _, hasBefore := m["before"]; hasBefore {
				return a
			}
		}
	}
	return detail
}

func (p *Postgres) PutQueued(ctx context.Context, q QueuedJob) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO queued_jobs (id, key, owner, kind, payload, ready_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, ready_at = EXCLUDED.ready_at, owner = EXCLUDED.owner`,
		q.ID, q.Key, q.Owner, q.Kind, q.Payload, q.ReadyAt)
	return err
}

func (p *Postgres) UpdateQueued(ctx context.Context, id string, payload json.RawMessage, readyAt time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE queued_jobs SET payload = $2, ready_at = $3 WHERE id = $1`, id, payload, readyAt)
	return err
}

func (p *Postgres) StartQueued(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `UPDATE queued_jobs SET started_at = now() WHERE id = $1`, id)
	return err
}

func (p *Postgres) DeleteQueued(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM queued_jobs WHERE id = $1`, id)
	return err
}

func (p *Postgres) ClaimQueued(ctx context.Context, owner string, stale time.Duration) ([]QueuedJob, error) {
	rows, err := p.pool.Query(ctx, `
		WITH c AS (
			SELECT id FROM queued_jobs q
			WHERE q.owner <> $1
			  AND NOT EXISTS (SELECT 1 FROM queue_owners o WHERE o.owner = q.owner AND o.seen_at > now() - make_interval(secs => $2))
			FOR UPDATE SKIP LOCKED
		)
		UPDATE queued_jobs SET owner = $1 FROM c WHERE queued_jobs.id = c.id
		RETURNING queued_jobs.id, key, owner, kind, payload, ready_at, created_at, started_at`, owner, stale.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueuedJob{}
	for rows.Next() {
		var q QueuedJob
		var started *time.Time
		if err := rows.Scan(&q.ID, &q.Key, &q.Owner, &q.Kind, &q.Payload, &q.ReadyAt, &q.CreatedAt, &started); err != nil {
			return nil, err
		}
		if started != nil {
			q.StartedAt = *started
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (p *Postgres) HeartbeatOwner(ctx context.Context, owner string) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO queue_owners (owner) VALUES ($1) ON CONFLICT (owner) DO UPDATE SET seen_at = now()`, owner)
	if err == nil {
		_, err = p.pool.Exec(ctx, `DELETE FROM queue_owners WHERE seen_at < now() - interval '1 day'`)
	}
	return err
}

func (p *Postgres) RemoveOwner(ctx context.Context, owner string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM queue_owners WHERE owner = $1`, owner)
	return err
}

const changeCols = `id, method, path, summary, preview, body, requested_by, requested_at, status, decided_by, decided_at, result_status, result`

func scanChange(row pgx.Row) (*ChangeRequest, error) {
	var c ChangeRequest
	var decided *time.Time
	err := row.Scan(&c.ID, &c.Method, &c.Path, &c.Summary, &c.Preview, &c.Body, &c.RequestedBy, &c.RequestedAt, &c.Status, &c.DecidedBy, &decided, &c.ResultStatus, &c.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if decided != nil {
		c.DecidedAt = *decided
	}
	return &c, err
}

func (p *Postgres) CreateChangeRequest(ctx context.Context, c ChangeRequest) (*ChangeRequest, error) {
	if len(c.Preview) == 0 {
		c.Preview = json.RawMessage("{}")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	out, err := scanChange(tx.QueryRow(ctx, `INSERT INTO change_requests (method, path, summary, preview, body, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+changeCols, c.Method, c.Path, c.Summary, c.Preview, c.Body, c.RequestedBy))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, c.RequestedBy, "change.requested", out.Summary, map[string]any{"id": out.ID, "method": out.Method, "path": out.Path, "after": out.Preview}); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (p *Postgres) GetChangeRequest(ctx context.Context, id int64) (*ChangeRequest, error) {
	return scanChange(p.pool.QueryRow(ctx, `SELECT `+changeCols+` FROM change_requests WHERE id = $1`, id))
}

func (p *Postgres) ListChangeRequests(ctx context.Context, status string, limit int) ([]ChangeRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT `+changeCols+` FROM change_requests WHERE ($1 = '' OR status = $1) ORDER BY id DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeRequest{}
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (p *Postgres) DecideChangeRequest(ctx context.Context, id int64, status, by string, resultStatus int, result string) (*ChangeRequest, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	c, err := scanChange(tx.QueryRow(ctx, `UPDATE change_requests SET status = $2, decided_by = $3, decided_at = now(), result_status = $4, result = $5,
		body = CASE WHEN $2 = 'pending' THEN body ELSE '' END
		WHERE id = $1 AND status = 'pending' RETURNING `+changeCols, id, status, by, resultStatus, result))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, by, "change."+status, c.Summary, map[string]any{"id": c.ID, "requested_by": c.RequestedBy, "result_status": resultStatus, "result": result}); err != nil {
		return nil, err
	}
	return c, tx.Commit(ctx)
}

func (p *Postgres) ClaimChangeRequest(ctx context.Context, id int64, by string) (*ChangeRequest, error) {
	return scanChange(p.pool.QueryRow(ctx, `UPDATE change_requests SET status = 'applying', decided_by = $2, decided_at = now()
		WHERE id = $1 AND status = 'pending' RETURNING `+changeCols, id, by))
}

func (p *Postgres) FinishChangeRequest(ctx context.Context, id int64, status, by string, resultStatus int, result string) (*ChangeRequest, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	c, err := scanChange(tx.QueryRow(ctx, `UPDATE change_requests SET status = $2, decided_by = $3, decided_at = now(), result_status = $4, result = $5, body = ''
		WHERE id = $1 AND status = 'applying' RETURNING `+changeCols, id, status, by, resultStatus, result))
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, by, "change."+status, c.Summary, map[string]any{"id": c.ID, "requested_by": c.RequestedBy, "result_status": resultStatus, "result": result}); err != nil {
		return nil, err
	}
	return c, tx.Commit(ctx)
}

func (p *Postgres) SearchAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	q := `SELECT id, at, actor, action, target, detail, ip, user_agent FROM audit_log WHERE true`
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	like := func(s string) string { return "%" + likeEscape(s) + "%" }
	if f.Actor != "" {
		add("actor ILIKE $%d", like(f.Actor))
	}
	if a, prefix := f.Prefix(); a != "" {
		if prefix {
			add("action LIKE $%d", likeEscape(a)+"%")
		} else {
			add("action = $%d", a)
		}
	}
	if f.Target != "" {
		add("target ILIKE $%d", like(f.Target))
	}
	if f.Query != "" {
		args = append(args, like(f.Query))
		q += fmt.Sprintf(" AND (target ILIKE $%d OR detail::text ILIKE $%d OR actor ILIKE $%d)", len(args), len(args), len(args))
	}
	if !f.From.IsZero() {
		add("at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("at < $%d", f.To)
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
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Target, &a.Detail, &a.IP, &a.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *Postgres) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, at, actor, action, target, detail, ip, user_agent FROM audit_log ORDER BY at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.At, &a.Actor, &a.Action, &a.Target, &a.Detail, &a.IP, &a.UserAgent); err != nil {
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
	if j.Approval == "" {
		j.Approval = ApprovalOf(j.Result)
	}
	var result any
	if len(j.Result) > 0 {
		result = []byte(j.Result)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO jobs (kind, platform, host, repo, number, url, trigger, state, enqueued, started, finished,
			merged, error, note, comments, files, tokens_in, tokens_out, model, result, requested_by,
			score_quality, score_security, score_maintainability, approval)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
		RETURNING id`,
		j.Kind, j.Platform, j.Host, j.Repo, j.Number, j.URL, j.Trigger, j.State, j.Enqueued, nullTime(j.Started), j.Finished,
		j.Merged, j.Error, j.Note, j.Comments, j.Files, j.TokensIn, j.TokensOut, j.Model, result, j.RequestedBy,
		scoreArg(j.Scores, "quality"), scoreArg(j.Scores, "security"), scoreArg(j.Scores, "maintainability"), j.Approval).Scan(&id)
	if err != nil {
		return 0, err
	}
	var durMS int64
	if !j.Started.IsZero() {
		durMS = j.Finished.Sub(j.Started).Milliseconds()
	}
	is := func(s string) int {
		if j.State == s {
			return 1
		}
		return 0
	}
	reviews := 0
	if IsReview(j) {
		reviews = 1
	}
	var sc Scores
	scored := 0
	if j.Scores != nil {
		sc, scored = *j.Scores, 1
	}
	approvals := 0
	if j.Approval == "approved" {
		approvals = 1
	}
	// One row per granularity; buckets are UTC.
	if _, err := tx.Exec(ctx, `
		INSERT INTO job_metrics (granularity, bucket, platform, host, repo, jobs, done, skipped, failed, comments, files, tokens_in, tokens_out, duration_ms,
			scored, quality_sum, security_sum, maintainability_sum, reviews, approvals)
		SELECT g, date_trunc(g, $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', $2, $3, lower($4), 1, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
		FROM unnest(ARRAY['hour', 'day', 'month']) AS g
		ON CONFLICT (granularity, bucket, platform, host, repo) DO UPDATE SET
			jobs = job_metrics.jobs + 1, done = job_metrics.done + EXCLUDED.done, skipped = job_metrics.skipped + EXCLUDED.skipped,
			failed = job_metrics.failed + EXCLUDED.failed, comments = job_metrics.comments + EXCLUDED.comments,
			files = job_metrics.files + EXCLUDED.files, tokens_in = job_metrics.tokens_in + EXCLUDED.tokens_in,
			tokens_out = job_metrics.tokens_out + EXCLUDED.tokens_out, duration_ms = job_metrics.duration_ms + EXCLUDED.duration_ms,
			scored = job_metrics.scored + EXCLUDED.scored, quality_sum = job_metrics.quality_sum + EXCLUDED.quality_sum,
			security_sum = job_metrics.security_sum + EXCLUDED.security_sum,
			maintainability_sum = job_metrics.maintainability_sum + EXCLUDED.maintainability_sum,
			reviews = job_metrics.reviews + EXCLUDED.reviews, approvals = job_metrics.approvals + EXCLUDED.approvals`,
		j.Finished, j.Platform, j.Host, j.Repo, is("done"), is("skipped"), is("failed"), j.Comments, j.Files, j.TokensIn, j.TokensOut, durMS,
		scored, sc.Quality, sc.Security, sc.Maintainability, reviews, approvals); err != nil {
		return 0, err
	}
	if err := recordImpact(ctx, tx, j); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (p *Postgres) Metrics(ctx context.Context, f MetricsFilter) ([]MetricPoint, error) {
	switch f.Granularity {
	case Hourly, Daily, Monthly:
	default:
		return nil, fmt.Errorf("granularity %q: want hour, day or month", f.Granularity)
	}
	q := `SELECT bucket, sum(jobs), sum(done), sum(skipped), sum(failed), sum(comments), sum(files), sum(tokens_in), sum(tokens_out), sum(duration_ms),
		sum(scored), sum(quality_sum), sum(security_sum), sum(maintainability_sum)
		FROM job_metrics WHERE granularity = $1 AND bucket >= $2 AND bucket < $3`
	args := []any{f.Granularity, f.From, f.To}
	for _, c := range []struct{ col, v string }{{"platform", f.Platform}, {"host", strings.ToLower(f.Host)}, {"repo", strings.ToLower(f.Repo)}} {
		if c.v != "" {
			args = append(args, c.v)
			q += fmt.Sprintf(" AND %s = $%d", c.col, len(args))
		}
	}
	rows, err := p.pool.Query(ctx, q+" GROUP BY bucket ORDER BY bucket", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetricPoint{}
	for rows.Next() {
		var m MetricPoint
		if err := rows.Scan(&m.Bucket, &m.Jobs, &m.Done, &m.Skipped, &m.Failed, &m.Comments, &m.Files, &m.TokensIn, &m.TokensOut, &m.DurationMS,
			&m.Scored, &m.QualitySum, &m.SecuritySum, &m.MaintainabilitySum); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *Postgres) TokensUsed(ctx context.Context, at time.Time, platform, host, repo string) (int64, error) {
	q := `SELECT coalesce(sum(tokens_in + tokens_out), 0) FROM job_metrics
		WHERE granularity = 'day' AND bucket = date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`
	args := []any{at}
	if repo != "" {
		q += ` AND platform = $2 AND host = lower($3) AND repo = lower($4)`
		args = append(args, platform, host, repo)
	}
	var n int64
	err := p.pool.QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}

func (p *Postgres) PruneJobs(ctx context.Context, before, hourlyBefore time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM jobs WHERE finished < $1`, before)
	if err != nil {
		return 0, err
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM job_metrics WHERE granularity = 'hour' AND bucket < $1`, hourlyBefore); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const jobCols = `id, kind, platform, host, repo, number, url, trigger, state, enqueued, started, finished,
	merged, error, note, comments, files, tokens_in, tokens_out, model, requested_by, score_quality, score_security, score_maintainability, approval`

// scoreArg is one score for an insert (NULL without scores).
func scoreArg(s *Scores, kind string) any {
	if s == nil {
		return nil
	}
	return s.Get(kind)
}

func scanJob(row pgx.Row, withResult bool) (*Job, error) {
	var j Job
	var started *time.Time
	var q, sec, mt *int
	dest := []any{&j.ID, &j.Kind, &j.Platform, &j.Host, &j.Repo, &j.Number, &j.URL, &j.Trigger, &j.State, &j.Enqueued, &started, &j.Finished,
		&j.Merged, &j.Error, &j.Note, &j.Comments, &j.Files, &j.TokensIn, &j.TokensOut, &j.Model, &j.RequestedBy, &q, &sec, &mt, &j.Approval}
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
	if q != nil && sec != nil && mt != nil {
		j.Scores = &Scores{Quality: *q, Security: *sec, Maintainability: *mt}
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
	if f.Host != "" {
		add("host = lower($%d)", f.Host)
	}
	if f.RepoLike != "" {
		add("repo ILIKE $%d", "%"+likeEscape(f.RepoLike)+"%")
	}
	if f.Trigger != "" {
		add("trigger = $%d", f.Trigger)
	}
	if !f.From.IsZero() {
		add("finished >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("finished < $%d", f.To)
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
	if f.ScoreBelow > 0 {
		switch f.ScoreKind {
		case "quality", "security", "maintainability":
			add("score_"+f.ScoreKind+" < $%d", f.ScoreBelow)
		default: // any
			add("least(score_quality, score_security, score_maintainability) < $%d", f.ScoreBelow)
		}
	}
	if f.Approval != "" {
		add("approval = $%d", f.Approval)
	}
	if f.Severity != "" || f.Category != "" {
		cond := `EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(result->'findings') = 'array' THEN result->'findings' ELSE '[]'::jsonb END) AS fd
			WHERE coalesce(fd->>'kind', '') <> 'question'`
		if f.Severity != "" && f.Severity != "any" {
			args = append(args, strings.ToLower(f.Severity))
			cond += fmt.Sprintf(" AND lower(fd->>'severity') = $%d", len(args))
		}
		if f.Category != "" {
			args = append(args, strings.ToLower(strings.TrimSpace(f.Category)))
			cond += fmt.Sprintf(" AND lower(trim(fd->>'category')) = $%d", len(args))
		}
		where = append(where, cond+")")
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

func (p *Postgres) RepoScores(ctx context.Context, since time.Time) ([]RepoScore, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT DISTINCT ON (platform, host, lower(repo)) platform, host, lower(repo), number, finished,
			score_quality, score_security, score_maintainability
		FROM jobs WHERE score_quality IS NOT NULL
		ORDER BY platform, host, lower(repo), id DESC`)
	if err != nil {
		return nil, err
	}
	out := []RepoScore{}
	idx := map[string]int{}
	for rows.Next() {
		var r RepoScore
		if err := rows.Scan(&r.Platform, &r.Host, &r.Repo, &r.Number, &r.At, &r.Latest.Quality, &r.Latest.Security, &r.Latest.Maintainability); err != nil {
			rows.Close()
			return nil, err
		}
		r.Trend = []MetricPoint{}
		idx[r.Platform+"|"+r.Host+"|"+r.Repo] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := p.pool.Query(ctx, `
		SELECT platform, host, repo, bucket, scored, quality_sum, security_sum, maintainability_sum
		FROM job_metrics WHERE granularity = 'day' AND bucket >= $1 AND scored > 0
		ORDER BY bucket`, since)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var plat, host, repo string
		var m MetricPoint
		if err := trows.Scan(&plat, &host, &repo, &m.Bucket, &m.Scored, &m.QualitySum, &m.SecuritySum, &m.MaintainabilitySum); err != nil {
			return nil, err
		}
		if i, ok := idx[plat+"|"+host+"|"+repo]; ok {
			out[i].Trend = append(out[i].Trend, m)
		}
	}
	return out, trows.Err()
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
	var before json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT config FROM model_profiles WHERE name = $1 FOR UPDATE`, name).Scan(&before); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_profiles (name, config, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		name, js, actor); err != nil {
		return err
	}
	var prev any
	if before != nil {
		prev = before
	}
	if err := audit(ctx, tx, actor, "model.put", name, changeDetail(prev, cfg, nil)); err != nil {
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

func (p *Postgres) MarkSeen(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	// New, or recorded longer ago than ttl: claim it (atomically, so two
	// replicas receiving the same delivery agree on one first).
	var k string
	err := p.pool.QueryRow(ctx, `
		INSERT INTO webhook_seen (key) VALUES ($1)
		ON CONFLICT (key) DO UPDATE SET seen_at = now()
		WHERE webhook_seen.seen_at < now() - make_interval(secs => $2)
		RETURNING key`, key, ttl.Seconds()).Scan(&k)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (p *Postgres) PruneSeen(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM webhook_seen WHERE seen_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
	return p.PutSettingAudited(ctx, key, value, value, actor)
}

func (p *Postgres) PutSettingAudited(ctx context.Context, key string, value, detail any, actor string) error {
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
	var last json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT detail FROM audit_log WHERE action = 'setting.put' AND target = $1 ORDER BY id DESC LIMIT 1`, key).Scan(&last); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := audit(ctx, tx, actor, "setting.put", key, changeDetail(previousAfter(last), detail, nil)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) CreateSession(ctx context.Context, idHash []byte, s Session) error {
	// Opportunistic cleanup keeps the table small.
	if _, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE expires < now()`); err != nil {
		return err
	}
	groups, err := json.Marshal(nonNil(s.Groups))
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO sessions (id_hash, subject, email, name, csrf, expires, groups, verified, ip, user_agent, last_seen)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())`,
		idHash, s.Subject, s.Email, s.Name, s.CSRF, s.Expires, groups, s.Verified, s.IP, s.UserAgent)
	return err
}

// TouchSession records a request on a session, at most once a minute.
func (p *Postgres) TouchSession(ctx context.Context, idHash []byte, ip string) error {
	_, err := p.pool.Exec(ctx, `UPDATE sessions SET last_seen = now(), ip = CASE WHEN $2 = '' THEN ip ELSE $2 END
		WHERE id_hash = $1 AND (last_seen IS NULL OR last_seen < now() - interval '1 minute')`, idHash, ip)
	return err
}

const sessionCols = `id, subject, email, name, csrf, created, expires, groups, verified, ip, user_agent, last_seen`

func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	var groups []byte
	var seen *time.Time
	if err := row.Scan(&s.ID, &s.Subject, &s.Email, &s.Name, &s.CSRF, &s.Created, &s.Expires, &groups, &s.Verified, &s.IP, &s.UserAgent, &seen); err != nil {
		return nil, err
	}
	if seen != nil {
		s.LastSeen = *seen
	}
	if groups != nil {
		if err := json.Unmarshal(groups, &s.Groups); err != nil {
			return nil, fmt.Errorf("session groups: %w", err)
		}
		s.Groups = nonNil(s.Groups)
	}
	return &s, nil
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (p *Postgres) GetSession(ctx context.Context, idHash []byte) (*Session, error) {
	s, err := scanSession(p.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id_hash=$1 AND expires > now()`, idHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (p *Postgres) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+sessionCols+` FROM sessions WHERE expires > now() ORDER BY created DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteSessionByID(ctx context.Context, id int64) (*Session, error) {
	s, err := scanSession(p.pool.QueryRow(ctx, `DELETE FROM sessions WHERE id=$1 AND expires > now() RETURNING `+sessionCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (p *Postgres) DeleteOtherSessions(ctx context.Context, keep []byte) (int, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash <> $1`, keep)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (p *Postgres) DeleteSession(ctx context.Context, idHash []byte) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash=$1`, idHash)
	return err
}
