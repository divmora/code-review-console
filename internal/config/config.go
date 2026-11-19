// Package config loads the console config: the review engine settings it
// hands to the codereview CLI (models, review limits, code hosts) plus the
// console's own server, database, admin, allowlist and executor settings.
//
// The console does not import the CLI's config types. Engine settings are
// the documented codereview.yaml keys; the console passes them through as
// YAML (CODEREVIEW_CONFIG_YAML), and the CLI binary that runs the reviews
// judges them ("console check-config" runs "codereview config validate"
// through the executor).
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the console config file.
type Config struct {
	// Engine settings, in the same format as the CLI's codereview.yaml.
	Model    string                  `yaml:"model"`
	Fallback []string                `yaml:"fallback"`
	Models   map[string]ModelProfile `yaml:"models"`
	// AllowRepoModel lets a repository's .codereview.yaml pick a profile.
	AllowRepoModel bool `yaml:"allow_repo_model"`
	// Review is passed to the CLI as-is (limits, verify, defaults, ...).
	Review  map[string]any `yaml:"review"`
	GitHub  GitHub         `yaml:"github"`
	GitLab  []GitLab       `yaml:"gitlab"`
	BotName string         `yaml:"bot_name"`

	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Admin    Admin    `yaml:"admin"`
	Repos    Repos    `yaml:"repos"`
	Executor Executor `yaml:"executor"`
}

// GitHub configures GitHub webhooks and API access.
type GitHub struct {
	APIURL string `yaml:"api_url"` // default https://api.github.com
	// Token is a PAT or fine-grained token. A GitHub App is recommended.
	Token          string `yaml:"token"`
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
	PrivateKey     string `yaml:"private_key"`
	WebhookSecret  string `yaml:"webhook_secret"`
	Identity       `yaml:",inline"`
}

// Identity is who the bot is on one code host. Both default from the
// top-level bot_name and the token's user, so set them only when a host
// uses a different bot account or mention handle.
type Identity struct {
	// BotName is the mention handle for chat commands on this host
	// (@<bot_name> review). Default: the top-level bot_name, else the
	// token's user (e.g. gitlab-bot), else "codereview".
	BotName string `yaml:"bot_name"`
	// Reviewer is the username that counts as the bot for
	// repos.require_reviewer. Default: the token's user.
	Reviewer string `yaml:"reviewer"`
}

// GitLab configures one GitLab instance (gitlab.com or self-managed).
//
// Webhooks are authenticated with a signing token (GitLab 19.0+, the
// webhook-signature header, HMAC-SHA256), a secret token (the plain-text
// X-Gitlab-Token header), or both while migrating: a signed request is
// checked by its signature, an unsigned one falls back to the secret token.
type GitLab struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
	// WebhookSecret is the webhook's secret token (X-Gitlab-Token).
	WebhookSecret string `yaml:"webhook_secret"`
	// SigningToken is the webhook's signing token ("whsec_..."), shown once
	// when it is generated in GitLab.
	SigningToken string `yaml:"signing_token"`
	// SigningTokens and WebhookSecrets add more, for instances that send
	// through several hooks (a system hook plus group or project hooks),
	// each with its own token. A request may match any of them.
	SigningTokens  []string `yaml:"signing_tokens"`
	WebhookSecrets []string `yaml:"webhook_secrets"`
	// RequireSignature rejects unsigned webhooks, even with a valid secret
	// token. Turn it on once every webhook has a signing token.
	RequireSignature bool `yaml:"require_signature"`
	SkipTLSVerify    bool `yaml:"skip_tls_verify"`
	Identity         `yaml:",inline"`
}

// ModelProfile is one entry of models: in codereview.yaml (provider, model,
// api_key_env, region, base_url, ...). It is kept as a map so keys the
// console does not know about still reach the CLI unchanged.
type ModelProfile map[string]any

// String returns the profile's value for key, or "".
func (m ModelProfile) String(key string) string {
	s, _ := m[key].(string)
	return s
}

// Spec is "provider:model".
func (m ModelProfile) Spec() string { return m.String("provider") + ":" + m.String("model") }

// WithKeyEnv returns a copy with any literal api_key removed and
// api_key_env set to env, plus the removed key ("" when there was none).
func (m ModelProfile) WithKeyEnv(env string) (ModelProfile, string) {
	out := ModelProfile{}
	for k, v := range m {
		out[k] = v
	}
	key := out.String("api_key")
	if key == "" {
		return out, ""
	}
	delete(out, "api_key")
	out["api_key_env"] = env
	return out, key
}

// WithoutKey returns a copy without a literal api_key.
func (m ModelProfile) WithoutKey() ModelProfile {
	out, _ := m.WithKeyEnv("")
	if m.String("api_key") != "" {
		if env := m.String("api_key_env"); env != "" {
			out["api_key_env"] = env
		} else {
			delete(out, "api_key_env")
		}
	}
	return out
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv replaces ${VAR} references with environment values. Only the
// ${VAR} form is expanded, so "$" elsewhere stays literal.
func ExpandEnv(data []byte) []byte {
	return []byte(envRef.ReplaceAllStringFunc(string(data), func(m string) string {
		return os.Getenv(envRef.FindStringSubmatch(m)[1])
	}))
}

// Server configures the HTTP server and queue.
type Server struct {
	Addr    string `yaml:"addr"`
	Workers int    `yaml:"workers"`
	// QueueSize bounds pending jobs; extra webhooks get HTTP 503.
	QueueSize int `yaml:"queue_size"`
	// JobTimeout bounds one review.
	JobTimeout time.Duration `yaml:"job_timeout"`
	// Debounce waits this long after a push before reviewing, so rapid
	// pushes collapse into one review.
	Debounce  time.Duration `yaml:"debounce"`
	Dashboard Dashboard     `yaml:"dashboard"`
	// WebhookRetention keeps webhook delivery history this long (needs a
	// database). Default 14 days; at most 100,000 deliveries are kept.
	WebhookRetention time.Duration `yaml:"webhook_retention"`
}

// Dashboard configures the status page defaults. Admins can change the
// public status page at runtime from the UI; these are the defaults.
type Dashboard struct {
	// Enabled turns on the token-protected status page at /dashboard.
	Enabled bool `yaml:"enabled"`
	// Token, when set, is required as ?token=... or "Authorization: Bearer".
	Token string `yaml:"token"`
	// HideRepos masks repository names, PR links and error details.
	HideRepos bool `yaml:"hide_repos"`
	// History is how many finished jobs to keep in memory.
	History int `yaml:"history"`
	// Title shown at the top of the page.
	Title string `yaml:"title"`
}

// Database configures PostgreSQL.
type Database struct {
	// URL is a postgres:// connection string (or DATABASE_URL).
	URL string `yaml:"url"`
}

// Admin configures sign-in to the admin UI with OIDC single sign-on.
type Admin struct {
	Enabled bool `yaml:"enabled"`
	// BaseURL is the public URL of the console, used for the OIDC redirect
	// (<base_url>/admin/callback).
	BaseURL string `yaml:"base_url"`
	OIDC    OIDC   `yaml:"oidc"`
	// SessionTTL bounds an admin session. Default 12h.
	SessionTTL time.Duration `yaml:"session_ttl"`
	// CookieSecret signs the short-lived sign-in cookie. Set it (32+
	// random characters) when several instances share a load balancer.
	CookieSecret string `yaml:"cookie_secret"`
}

// OIDC is an OpenID Connect provider.
type OIDC struct {
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	Scopes       []string `yaml:"scopes"`
	// Who may sign in: any match grants admin. At least one must be set.
	AllowedEmails  []string `yaml:"allowed_emails"`
	AllowedDomains []string `yaml:"allowed_domains"`
	AllowedGroups  []string `yaml:"allowed_groups"`
	// GroupsClaim names the ID-token claim holding group names. Default "groups".
	GroupsClaim string `yaml:"groups_claim"`
}

// Repos controls which repositories are reviewed.
type Repos struct {
	// Allowlist reviews only repositories enabled in the admin UI.
	Allowlist bool `yaml:"allowlist"`
	// RequireReviewer reviews a pull/merge request only when the bot user
	// (the token's user) is one of its reviewers. Requesting the bot as a
	// reviewer starts a review. Chat commands (@bot review) still work.
	// Applies on top of the allowlist; with it, the allowlist can be off.
	RequireReviewer bool `yaml:"require_reviewer"`
	// SkipBranchSync skips automatic reviews of merge/pull requests whose
	// source branch is the project's default branch (e.g. master into a
	// release branch): their commits were reviewed on the way in. Default
	// true. Chat commands still review them.
	SkipBranchSync *bool `yaml:"skip_branch_sync"`
	// SyncSourceBranches are more source branches (globs, e.g. develop,
	// release/*) treated as branch syncs.
	SyncSourceBranches []string `yaml:"sync_source_branches"`
	// ScoreLabels posts Quality/Security/Maintainability score labels on the
	// merge/pull request; CommitStatus sets a "codereview" status on the
	// reviewed commit. Initial values; then managed in the admin panel.
	ScoreLabels  bool `yaml:"score_labels"`
	CommitStatus bool `yaml:"commit_status"`
}

// Executor selects where reviews run. Each review runs the codereview CLI
// once, as a process, a Docker container or a Kubernetes Job.
type Executor struct {
	// Type is local, docker or kubernetes. Default local.
	Type       string         `yaml:"type"`
	Local      LocalExecutor  `yaml:"local"`
	Docker     DockerExecutor `yaml:"docker"`
	Kubernetes KubeExecutor   `yaml:"kubernetes"`
	// Env lists extra environment variable names passed through to every
	// review (e.g. provider keys: OPENAI_API_KEY, AWS_REGION).
	Env []string `yaml:"env"`
}

// LocalExecutor runs the CLI binary on the console host.
type LocalExecutor struct {
	// Binary is the codereview CLI path. Default: "codereview" on PATH.
	Binary string `yaml:"binary"`
}

// DockerExecutor runs each review in a container.
type DockerExecutor struct {
	// Image is the CLI image, e.g. ghcr.io/divmora/code-review-bot:v0.1.0.
	Image string `yaml:"image"`
	// Network for the container (default bridge).
	Network string `yaml:"network"`
	// CPUs and Memory limit each container (docker --cpus / --memory).
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
	// Binary is the docker CLI. Default "docker".
	Binary string `yaml:"binary"`
}

// KubeExecutor runs each review as a Kubernetes Job.
type KubeExecutor struct {
	Image          string `yaml:"image"`
	Namespace      string `yaml:"namespace"`
	ServiceAccount string `yaml:"service_account"`
	CPU            string `yaml:"cpu"`
	Memory         string `yaml:"memory"`
	// TTL keeps finished Jobs this long for debugging. Default 10m.
	TTL time.Duration `yaml:"ttl"`
	// Kubeconfig path; empty means in-cluster config.
	Kubeconfig string `yaml:"kubeconfig"`
}

// Load reads the console config. An empty path uses CODEREVIEW_CONSOLE_CONFIG
// or ./console.yaml.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if path == "" {
		path = os.Getenv("CODEREVIEW_CONSOLE_CONFIG")
	}
	if path == "" {
		if _, err := os.Stat("console.yaml"); err == nil {
			path = "console.yaml"
		}
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(ExpandEnv(data), cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	cfg.applyEnv()
	cfg.applyDefaults()
	return cfg, cfg.validate()
}

// applyEnv reads the same environment variables as the CLI.
func (c *Config) applyEnv() {
	if v := os.Getenv("CODEREVIEW_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("CODEREVIEW_FALLBACK"); v != "" {
		c.Fallback = nil
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				c.Fallback = append(c.Fallback, f)
			}
		}
	}
	if c.GitHub.Token == "" {
		c.GitHub.Token = os.Getenv("GITHUB_TOKEN")
	}
	if c.GitHub.WebhookSecret == "" {
		c.GitHub.WebhookSecret = os.Getenv("GITHUB_WEBHOOK_SECRET")
	}
	if len(c.GitLab) == 0 && os.Getenv("GITLAB_TOKEN") != "" {
		base := os.Getenv("GITLAB_URL")
		if base == "" {
			base = "https://gitlab.com"
		}
		c.GitLab = []GitLab{{BaseURL: base, Token: os.Getenv("GITLAB_TOKEN"), WebhookSecret: os.Getenv("GITLAB_WEBHOOK_SECRET"),
			SigningToken: os.Getenv("GITLAB_WEBHOOK_SIGNING_TOKEN")}}
	}
}

func (c *Config) applyDefaults() {
	if c.GitHub.APIURL == "" {
		c.GitHub.APIURL = "https://api.github.com"
	}
	c.GitHub.APIURL = strings.TrimRight(c.GitHub.APIURL, "/")
	for i := range c.GitLab {
		if c.GitLab[i].BaseURL == "" {
			c.GitLab[i].BaseURL = "https://gitlab.com"
		}
		c.GitLab[i].BaseURL = strings.TrimRight(c.GitLab[i].BaseURL, "/")
	}
	if c.Database.URL == "" {
		c.Database.URL = os.Getenv("DATABASE_URL")
	}
	s := &c.Server
	if s.Addr == "" {
		s.Addr = ":8080"
	}
	if s.Workers <= 0 {
		s.Workers = 4
	}
	if s.QueueSize <= 0 {
		s.QueueSize = 100
	}
	if s.JobTimeout <= 0 {
		s.JobTimeout = 20 * time.Minute
	}
	if s.Debounce < 0 {
		s.Debounce = 0
	}
	if s.WebhookRetention <= 0 {
		s.WebhookRetention = 14 * 24 * time.Hour
	}
	if s.Dashboard.History <= 0 {
		s.Dashboard.History = 200
	}
	if s.Dashboard.Title == "" {
		s.Dashboard.Title = "Code review"
	}
	a := &c.Admin
	if a.SessionTTL <= 0 {
		a.SessionTTL = 12 * time.Hour
	}
	if a.OIDC.GroupsClaim == "" {
		a.OIDC.GroupsClaim = "groups"
	}
	if len(a.OIDC.Scopes) == 0 {
		a.OIDC.Scopes = []string{"openid", "email", "profile"}
	}
	a.BaseURL = strings.TrimRight(a.BaseURL, "/")
	e := &c.Executor
	if e.Type == "" {
		e.Type = "local"
	}
	if e.Local.Binary == "" {
		e.Local.Binary = "codereview"
	}
	if e.Docker.Binary == "" {
		e.Docker.Binary = "docker"
	}
	if e.Kubernetes.Namespace == "" {
		e.Kubernetes.Namespace = "default"
	}
	if e.Kubernetes.TTL <= 0 {
		e.Kubernetes.TTL = 10 * time.Minute
	}
}

func (c *Config) validate() error {
	switch c.Executor.Type {
	case "local":
	case "docker":
		if c.Executor.Docker.Image == "" {
			return errors.New("executor.docker.image is required")
		}
	case "kubernetes":
		if c.Executor.Kubernetes.Image == "" {
			return errors.New("executor.kubernetes.image is required")
		}
	default:
		return fmt.Errorf("executor.type %q: want local, docker or kubernetes", c.Executor.Type)
	}
	if c.Admin.Enabled && c.Database.URL == "" {
		return errors.New("admin.enabled needs database.url")
	}
	if c.Repos.Allowlist && c.Database.URL == "" {
		return errors.New("repos.allowlist needs database.url")
	}
	for name, m := range c.Models {
		if m.String("provider") == "" || m.String("model") == "" {
			return fmt.Errorf("models.%s needs provider and model", name)
		}
	}
	return nil
}

// AllSigningTokens returns signing_token plus signing_tokens.
func (g GitLab) AllSigningTokens() []string {
	return nonEmpty(append([]string{g.SigningToken}, g.SigningTokens...))
}

// AllSecrets returns webhook_secret plus webhook_secrets.
func (g GitLab) AllSecrets() []string {
	return nonEmpty(append([]string{g.WebhookSecret}, g.WebhookSecrets...))
}

func nonEmpty(vals []string) []string {
	var out []string
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// GitLabFor finds the instance whose host matches.
func (c *Config) GitLabFor(host string) (GitLab, bool) {
	for _, g := range c.GitLab {
		if hostOf(g.BaseURL) == strings.ToLower(host) {
			return g, true
		}
	}
	return GitLab{}, false
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

// ProfileNames lists the model profiles from the config file.
func (c *Config) ProfileNames() []string {
	out := make([]string, 0, len(c.Models))
	for n := range c.Models {
		out = append(out, n)
	}
	return out
}
