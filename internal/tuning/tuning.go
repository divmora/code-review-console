// Package tuning holds the queue, review engine and integration settings
// managed in the admin panel: how many reviews run at once, timeouts,
// the codereview CLI's review: settings, the mention handle, extra
// environment variables for review jobs and the dashboard token. They
// apply without a restart.
//
// The config file's server.workers, queue_size, job_timeout, debounce,
// dashboard.token and dashboard.history, review:, allow_repo_model,
// bot_name and executor.env are copied into the database on the first
// start (Import) and ignored after that.
package tuning

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
)

// Key is the settings row holding Settings.
const Key = "tuning"

// ImportActor is the audit actor of the copy from the config file.
const ImportActor = "config file (initial)"

const cacheTTL = 5 * time.Second

// Built-in values, used when nothing is saved.
const (
	DefaultWorkers    = 4
	DefaultQueueSize  = 100
	DefaultJobTimeout = 20 * time.Minute
	DefaultDebounce   = 30 * time.Second
	DefaultHistory    = 200
)

// Settings are saved in the database under Key.
type Settings struct {
	// Workers is how many reviews run at once; QueueSize bounds pending
	// jobs (extra webhooks get HTTP 503).
	Workers   int `json:"workers"`
	QueueSize int `json:"queue_size"`
	// JobTimeout bounds one review; Debounce waits after a push so rapid
	// pushes collapse into one review. Go durations ("20m", "30s").
	JobTimeout string `json:"job_timeout"`
	Debounce   string `json:"debounce"`
	// History is how many finished jobs the dashboard keeps in memory.
	History int `json:"history"`
	// BotName is the default mention handle (@<bot_name> review) for hosts
	// that set none; empty uses each host's token user.
	BotName string `json:"bot_name,omitempty"`
	// AllowRepoModel lets a repository's .codereview.yaml pick a model profile.
	AllowRepoModel bool `json:"allow_repo_model"`
	// ExtraEnv are environment variable names of the console passed to
	// every review job (model key variables are passed already).
	ExtraEnv []string `json:"extra_env,omitempty"`
	// Engine is the codereview CLI's review: section.
	Engine Engine `json:"engine"`
	// DashboardTokenHash is the SHA-256 (hex) of the /dashboard token;
	// empty = no token. The token itself is never stored.
	DashboardTokenHash string `json:"dashboard_token_hash,omitempty"`
	// DashboardTokenExpires is when the token stops working (RFC 3339,
	// UTC); empty = never.
	DashboardTokenExpires string `json:"dashboard_token_expires,omitempty"`
	// DashboardAccess says who can open the status dashboard: AccessAdmins,
	// AccessToken or AccessPublic. Empty: see Access.
	DashboardAccess string `json:"dashboard_access,omitempty"`
}

// Who can open the status dashboard.
const (
	// AccessAdmins: signed-in admins only (at /admin/dashboard).
	AccessAdmins = "admins"
	// AccessToken: signed-in admins, and anyone with the dashboard token.
	AccessToken = "token"
	// AccessPublic: anyone who can reach the console.
	AccessPublic = "public"
)

// Access is who can open the status dashboard. Never chosen: anyone with
// the token when one is set (so links already shared keep working),
// otherwise admins only.
func (s Settings) Access() string {
	switch s.DashboardAccess {
	case AccessAdmins, AccessToken, AccessPublic:
		return s.DashboardAccess
	}
	if s.DashboardTokenHash != "" {
		return AccessToken
	}
	return AccessAdmins
}

// Engine is the codereview CLI's review: section (zero values use the
// CLI's defaults). Defaults is review.defaults: a repository config
// applied under each repository's .codereview.yaml.
type Engine struct {
	Concurrency      int            `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	BatchTokens      int            `json:"batch_tokens,omitempty" yaml:"batch_tokens,omitempty"`
	MaxFiles         int            `json:"max_files,omitempty" yaml:"max_files,omitempty"`
	FullFileMaxLines int            `json:"full_file_max_lines,omitempty" yaml:"full_file_max_lines,omitempty"`
	ContextLines     int            `json:"context_lines,omitempty" yaml:"context_lines,omitempty"`
	Passes           int            `json:"passes,omitempty" yaml:"passes,omitempty"`
	Verify           *bool          `json:"verify,omitempty" yaml:"verify,omitempty"`
	VerifyModel      string         `json:"verify_model,omitempty" yaml:"verify_model,omitempty"`
	VerifyCanDrop    string         `json:"verify_can_drop,omitempty" yaml:"verify_can_drop,omitempty"`
	VerifyCanAdd     bool           `json:"verify_can_add,omitempty" yaml:"verify_can_add,omitempty"`
	RepoConfigFile   string         `json:"repo_config_file,omitempty" yaml:"repo_config_file,omitempty"`
	Defaults         map[string]any `json:"defaults,omitempty" yaml:"defaults,omitempty"`
}

// Map is the engine settings as the CLI's review: section.
func (e Engine) Map() map[string]any {
	b, _ := yaml.Marshal(e)
	var m map[string]any
	_ = yaml.Unmarshal(b, &m)
	return m
}

// engineKeys are the review: keys the CLI knows; defaultKeys the keys of
// review.defaults (a repository config).
var (
	engineKeys  = []string{"concurrency", "batch_tokens", "max_files", "full_file_max_lines", "context_lines", "passes", "verify", "verify_model", "verify_can_drop", "verify_can_add", "repo_config_file", "defaults"}
	defaultKeys = map[string]bool{
		"profile": true, "learnings": true, "instructions": true, "path_instructions": true, "path_filters": true,
		"max_comments": true, "min_severity": true, "language": true, "poem": true, "auto_review": true, "guidelines": true,
		"high_level_summary": true, "high_level_summary_placeholder": true, "high_level_summary_instructions": true,
		"auto_title_placeholder": true, "auto_title_instructions": true, "model": true,
	}
)

// EngineFromMap reads a config file's review: section.
func EngineFromMap(m map[string]any) (Engine, error) {
	var e Engine
	if len(m) == 0 {
		return e, nil
	}
	known := map[string]bool{}
	for _, k := range engineKeys {
		known[k] = true
	}
	for k := range m {
		if !known[k] {
			return e, fmt.Errorf("review.%s is not a review setting", k)
		}
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return e, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&e); err != nil {
		return e, fmt.Errorf("review: %w", err)
	}
	return e, nil
}

// Effective resolves zero values to the built-in ones.
func (s Settings) Effective() Settings {
	if s.Workers <= 0 {
		s.Workers = DefaultWorkers
	}
	if s.QueueSize <= 0 {
		s.QueueSize = DefaultQueueSize
	}
	if s.JobTimeout == "" {
		s.JobTimeout = shortDuration(DefaultJobTimeout)
	}
	if s.Debounce == "" {
		s.Debounce = shortDuration(DefaultDebounce)
	}
	if s.History <= 0 {
		s.History = DefaultHistory
	}
	return s
}

// JobTimeoutDuration and DebounceDuration parse the durations (with the
// built-in values when unset or invalid).
func (s Settings) JobTimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(s.JobTimeout); err == nil && d > 0 {
		return d
	}
	return DefaultJobTimeout
}

// DebounceDuration is how long to wait after a push.
func (s Settings) DebounceDuration() time.Duration {
	if d, err := time.ParseDuration(s.Debounce); err == nil && d >= 0 {
		return d
	}
	return DefaultDebounce
}

// DashboardTokenOK reports whether token is the dashboard token and has
// not expired (false when none is set).
func (s Settings) DashboardTokenOK(token string) bool {
	return s.tokenMatches(token) && !s.TokenExpired(time.Now())
}

// DashboardTokenExpiredFor reports whether token is the dashboard token
// but has expired, so the page can say so instead of "token required".
func (s Settings) DashboardTokenExpiredFor(token string) bool {
	return s.tokenMatches(token) && s.TokenExpired(time.Now())
}

func (s Settings) tokenMatches(token string) bool {
	if s.DashboardTokenHash == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(s.DashboardTokenHash)) == 1
}

// TokenExpiry is when the dashboard token expires (zero = never).
func (s Settings) TokenExpiry() time.Time {
	t, _ := time.Parse(time.RFC3339, s.DashboardTokenExpires)
	return t
}

// TokenExpired reports whether the dashboard token has expired at now.
func (s Settings) TokenExpired(now time.Time) bool {
	exp := s.TokenExpiry()
	return !exp.IsZero() && !now.Before(exp)
}

// TokenValidDays are the choices for how long a new dashboard token
// works (0 = never expires).
var TokenValidDays = []int{7, 30, 90, 365, 0}

// DashboardOpen reports whether a request with token (and signed in as an
// admin or not) may see the status dashboard.
func (s Settings) DashboardOpen(token string, admin bool) bool {
	switch s.Access() {
	case AccessPublic:
		return true
	case AccessToken:
		return admin || s.DashboardTokenOK(token)
	}
	return admin
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

var (
	envNameRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	botNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	reservedEnv = map[string]bool{
		"DATABASE_URL": true, "GITHUB_TOKEN": true, "GITHUB_WEBHOOK_SECRET": true, "GITHUB_APP_PRIVATE_KEY": true,
		"GITLAB_TOKEN": true, "GITLAB_WEBHOOK_SECRET": true, "GITLAB_WEBHOOK_SIGNING_TOKEN": true, "BITBUCKET_TOKEN": true,
		"BITBUCKET_API_TOKEN": true, "BITBUCKET_WEBHOOK_SECRET": true, "OIDC_CLIENT_SECRET": true, "DASHBOARD_TOKEN": true,
	}
)

// CheckEnvName rejects names that aren't valid or that hold the
// console's own secrets (they would leave the console with every job).
func CheckEnvName(name string) error {
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("%q: use an environment variable name such as MY_PROXY_CA", name)
	}
	if reservedEnv[name] || strings.HasPrefix(name, "CODEREVIEW_") || strings.HasPrefix(name, "POSTGRES") || strings.HasPrefix(name, "PG") {
		return fmt.Errorf("%q holds one of the console's own settings or secrets and can't be passed to review jobs", name)
	}
	return nil
}

// Validate checks s before it is saved. profiles are the model profile
// names (for verify_model).
func (s Settings) Validate(profiles map[string]bool) error {
	if s.Workers < 1 || s.Workers > 64 {
		return errors.New("reviews at once must be between 1 and 64")
	}
	if s.QueueSize < 1 || s.QueueSize > 10000 {
		return errors.New("queue size must be between 1 and 10000")
	}
	if d, err := time.ParseDuration(s.JobTimeout); err != nil || d < time.Minute || d > 6*time.Hour {
		return fmt.Errorf("job timeout %q: want a duration between 1m and 6h, such as 20m", s.JobTimeout)
	}
	if d, err := time.ParseDuration(s.Debounce); err != nil || d < 0 || d > time.Hour {
		return fmt.Errorf("debounce %q: want a duration between 0s and 1h, such as 30s", s.Debounce)
	}
	if s.History < 10 || s.History > 5000 {
		return errors.New("dashboard history must be between 10 and 5000 jobs")
	}
	if s.BotName != "" && !botNameRe.MatchString(s.BotName) {
		return fmt.Errorf("bot name %q: use the account's username without @", s.BotName)
	}
	switch s.DashboardAccess {
	case "", AccessAdmins, AccessToken, AccessPublic:
	default:
		return fmt.Errorf("dashboard access %q: want admins, token or public", s.DashboardAccess)
	}
	if s.DashboardAccess == AccessToken && s.DashboardTokenHash == "" {
		return errors.New("set a dashboard token, or choose another dashboard access")
	}
	if len(s.ExtraEnv) > 50 {
		return errors.New("at most 50 extra environment variables")
	}
	for _, n := range s.ExtraEnv {
		if err := CheckEnvName(n); err != nil {
			return fmt.Errorf("extra environment variable %w", err)
		}
	}
	return s.Engine.Validate(profiles)
}

// Validate checks the engine settings against the CLI's limits.
func (e Engine) Validate(profiles map[string]bool) error {
	for name, v := range map[string][3]int{
		"concurrency": {e.Concurrency, 0, 32}, "batch tokens": {e.BatchTokens, 0, 1_000_000}, "max files": {e.MaxFiles, 0, 10_000},
		"full file max lines": {e.FullFileMaxLines, 0, 100_000}, "context lines": {e.ContextLines, 0, 10_000}, "passes": {e.Passes, 0, 3},
	} {
		if v[0] < v[1] || v[0] > v[2] {
			return fmt.Errorf("%s must be between %d and %d (0 = the CLI's default)", name, v[1], v[2])
		}
	}
	switch e.VerifyCanDrop {
	case "", "confident", "minor", "all":
	default:
		return fmt.Errorf("verify can drop %q: want confident, minor or all", e.VerifyCanDrop)
	}
	if e.VerifyModel != "" && !profiles[e.VerifyModel] && !strings.Contains(e.VerifyModel, ":") {
		return fmt.Errorf("verify model %q is not a model profile (or provider:model)", e.VerifyModel)
	}
	if strings.Contains(e.RepoConfigFile, "..") || strings.HasPrefix(e.RepoConfigFile, "/") {
		return fmt.Errorf("repository config file %q: want a path inside the repository", e.RepoConfigFile)
	}
	keys := make([]string, 0, len(e.Defaults))
	for k := range e.Defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !defaultKeys[k] {
			return fmt.Errorf("review defaults: %q is not a repository setting", k)
		}
	}
	return nil
}

// Service reads and changes the settings.
type Service struct {
	cfg *config.Config
	st  store.Store
	// OnChange is called after a save on this instance.
	OnChange func(Settings)

	mu     sync.Mutex
	cached *Settings
	at     time.Time
}

// New returns a Service (st nil: the config file's values, for tests).
func New(cfg *config.Config, st store.Store) *Service {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Service{cfg: cfg, st: st}
}

// FromConfig reads the config file's values.
func FromConfig(cfg *config.Config) (Settings, error) {
	eng, err := EngineFromMap(cfg.Review)
	s := Settings{BotName: cfg.BotName, AllowRepoModel: cfg.AllowRepoModel, ExtraEnv: append([]string{}, cfg.Executor.Env...), Engine: eng}
	// Values the file doesn't set keep the built-in ones.
	if cfg.InFile("server.workers") {
		s.Workers = cfg.Server.Workers
	}
	if cfg.InFile("server.queue_size") {
		s.QueueSize = cfg.Server.QueueSize
	}
	if cfg.InFile("server.dashboard") {
		s.History = cfg.Server.Dashboard.History
	}
	if cfg.InFile("server.job_timeout") && cfg.Server.JobTimeout > 0 {
		s.JobTimeout = shortDuration(cfg.Server.JobTimeout)
	}
	if cfg.InFile("server.debounce") {
		s.Debounce = shortDuration(cfg.Server.Debounce)
	}
	if t := cfg.Server.Dashboard.Token; t != "" {
		s.DashboardTokenHash = hashToken(t)
	}
	return s, err
}

// Get returns the settings in use (built-in values filled in).
func (s *Service) Get(ctx context.Context) Settings {
	if s.st == nil {
		f, _ := FromConfig(s.cfg)
		return f.Effective()
	}
	s.mu.Lock()
	if s.cached != nil && time.Since(s.at) < cacheTTL {
		c := *s.cached
		s.mu.Unlock()
		return c
	}
	s.mu.Unlock()
	var st Settings
	found, err := s.st.GetSetting(ctx, Key, &st)
	if err != nil || !found {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil && s.cached != nil {
			return *s.cached
		}
		f, _ := FromConfig(s.cfg)
		return f.Effective()
	}
	st = st.Effective()
	s.mu.Lock()
	s.cached, s.at = &st, time.Now()
	s.mu.Unlock()
	return st
}

// Update is a save from the admin panel. Token: "" keeps the dashboard
// token (and its expiry), ClearToken removes it. ValidDays is how long a
// new token works (one of TokenValidDays; 0 = never).
type Update struct {
	Settings
	DashboardToken string `json:"dashboard_token"`
	ClearToken     bool   `json:"clear_dashboard_token"`
	ValidDays      int    `json:"dashboard_token_valid_days"`
}

// Put validates and saves an update, keeping the stored dashboard token
// unless the update sets or clears it.
func (s *Service) Put(ctx context.Context, u Update, profiles map[string]bool, actor string) (Settings, error) {
	if s.st == nil {
		return Settings{}, errors.New("settings need a database")
	}
	n := clean(u.Settings)
	cur := s.Get(ctx)
	n.DashboardTokenHash, n.DashboardTokenExpires = cur.DashboardTokenHash, cur.DashboardTokenExpires
	switch t := strings.TrimSpace(u.DashboardToken); {
	case u.ClearToken:
		n.DashboardTokenHash, n.DashboardTokenExpires = "", ""
	case t != "":
		if len(t) < 16 {
			return Settings{}, errors.New("the dashboard token needs at least 16 characters")
		}
		valid := false
		for _, d := range TokenValidDays {
			valid = valid || d == u.ValidDays
		}
		if !valid {
			return Settings{}, fmt.Errorf("token validity %d days: choose 7, 30, 90 or 365 days, or never (0)", u.ValidDays)
		}
		n.DashboardTokenHash, n.DashboardTokenExpires = hashToken(t), ""
		if u.ValidDays > 0 {
			n.DashboardTokenExpires = time.Now().UTC().Add(time.Duration(u.ValidDays) * 24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
		}
	}
	if err := n.Validate(profiles); err != nil {
		return Settings{}, err
	}
	if err := s.st.PutSettingAudited(ctx, Key, n, auditDetail(n, cur.DashboardTokenHash), actor); err != nil {
		return Settings{}, err
	}
	n = n.Effective()
	s.mu.Lock()
	s.cached, s.at = &n, time.Now()
	s.mu.Unlock()
	if s.OnChange != nil {
		s.OnChange(n)
	}
	return n, nil
}

// auditDetail is s for the audit log, which admins can read: the token's
// hash is replaced by what happened to the token, so a short hand-typed
// token can't be guessed offline from the log.
func auditDetail(s Settings, oldHash string) Settings {
	switch {
	case s.DashboardTokenHash == "" && oldHash != "":
		s.DashboardTokenHash = "(removed)"
	case s.DashboardTokenHash == "":
	case oldHash == "":
		s.DashboardTokenHash = "(set)"
	case s.DashboardTokenHash != oldHash:
		s.DashboardTokenHash = "(changed)"
	default:
		s.DashboardTokenHash = "(unchanged)"
	}
	return s
}

func clean(s Settings) Settings {
	s.JobTimeout, s.Debounce, s.BotName = strings.TrimSpace(s.JobTimeout), strings.TrimSpace(s.Debounce), strings.TrimPrefix(strings.TrimSpace(s.BotName), "@")
	env := []string{}
	seen := map[string]bool{}
	for _, n := range s.ExtraEnv {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			env = append(env, n)
		}
	}
	s.ExtraEnv = env
	e := &s.Engine
	e.VerifyModel, e.VerifyCanDrop, e.RepoConfigFile = strings.TrimSpace(e.VerifyModel), strings.TrimSpace(e.VerifyCanDrop), strings.TrimSpace(e.RepoConfigFile)
	if len(e.Defaults) == 0 {
		e.Defaults = nil
	}
	return s.Effective()
}

// Import copies the config file's values into the database on the first
// start (when nothing is saved yet). A review: section the panel can't
// hold is reported as an error and the rest is still copied.
func (s *Service) Import(ctx context.Context) (imported bool, err error) {
	if s.st == nil {
		return false, nil
	}
	var cur Settings
	found, err := s.st.GetSetting(ctx, Key, &cur)
	if err != nil || found {
		return false, err
	}
	f, ferr := FromConfig(s.cfg)
	f = clean(f)
	if err := s.st.PutSettingAudited(ctx, Key, f, auditDetail(f, ""), ImportActor); err != nil {
		return false, err
	}
	return true, ferr
}

// shortDuration is d.String() without zero trailing units: 10m, not 10m0s.
func shortDuration(d time.Duration) string {
	out := d.String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}
