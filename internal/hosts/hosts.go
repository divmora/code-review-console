// Package hosts is the registry of code hosts (GitHub, GitLab, Bitbucket)
// the console serves. The config file is the initial configuration: on
// first start its hosts are copied into the database, and from then on
// hosts are managed from the admin panel. Secrets (tokens, keys, webhook
// secrets) are encrypted with CODEREVIEW_CONSOLE_SECRET_KEY before they
// reach the database. Without a database, or without a key while the
// config file has secrets, the config file is used as is (read-only).
package hosts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store"
)

// Platforms.
const (
	GitHub    = "github"
	GitLab    = "gitlab"
	Bitbucket = "bitbucket"
)

// Host is one code host with its credentials.
type Host struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	// Host is the web host repositories live on (github.com, gitlab.example.com,
	// bitbucket.org); it keys repositories, rules and webhook routing.
	Host string `json:"host"`
	// BaseURL is the API URL for GitHub and Bitbucket, the instance URL for GitLab.
	BaseURL string `json:"base_url"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Settings
	Secrets `json:"-"`
	// Hook are the secrets of the webhook a delivery came in on, set by
	// Route (never stored on the host: every webhook has its own URL and
	// secrets).
	Hook EndpointSecrets `json:"-"`
	// Credential is the credential this copy of the host acts with for a
	// repository (see ForRepo); 0 for the host's own.
	Credential int64 `json:"-"`
}

// Settings are a host's non-secret settings.
type Settings struct {
	AppID            int64  `json:"app_id,omitempty" yaml:"app_id,omitempty"`                       // GitHub App
	Email            string `json:"email,omitempty" yaml:"email,omitempty"`                         // Bitbucket, with APIToken
	RequireSignature bool   `json:"require_signature,omitempty" yaml:"require_signature,omitempty"` // GitLab
	SkipTLSVerify    bool   `json:"skip_tls_verify,omitempty" yaml:"skip_tls_verify,omitempty"`     // GitLab
	BotName          string `json:"bot_name,omitempty" yaml:"bot_name,omitempty"`
	Reviewer         string `json:"reviewer,omitempty" yaml:"reviewer,omitempty"`
}

// Secrets are a host's secret values; they are stored encrypted.
type Secrets struct {
	Token         string `json:"token,omitempty"`
	AppPrivateKey string `json:"app_private_key,omitempty"`
	APIToken      string `json:"api_token,omitempty"`
}

// sharedHookSecrets are the host-level webhook secrets of the shared
// /webhooks/<platform> URL, removed in 0.12: read only to delete them.
type sharedHookSecrets struct {
	WebhookSecrets []string `json:"webhook_secrets"`
	SigningTokens  []string `json:"signing_tokens"`
}

func (s Secrets) empty() bool {
	return s.Token == "" && s.AppPrivateKey == "" && s.APIToken == ""
}

// AppKey returns the GitHub App private key ("file:<path>" reads a file,
// as configured with private_key_file).
func (h Host) AppKey() ([]byte, error) {
	if p, ok := strings.CutPrefix(h.AppPrivateKey, "file:"); ok {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read the GitHub App key: %w", err)
		}
		return b, nil
	}
	return []byte(h.AppPrivateKey), nil
}

// Status says which secrets are set, for display (never the values).
type Status struct {
	Token         bool `json:"token"`
	AppPrivateKey bool `json:"app_private_key"`
	APIToken      bool `json:"api_token"`
}

// SecretStatus reports which secrets are set.
func (h Host) SecretStatus() Status {
	return Status{Token: h.Token != "", AppPrivateKey: h.AppPrivateKey != "", APIToken: h.APIToken != ""}
}

// Normalize fills defaults and derives Host from BaseURL.
func (h *Host) Normalize() error {
	h.Platform = strings.ToLower(strings.TrimSpace(h.Platform))
	h.BaseURL = strings.TrimRight(strings.TrimSpace(h.BaseURL), "/")
	switch h.Platform {
	case GitHub:
		if h.BaseURL == "" {
			h.BaseURL = "https://api.github.com"
		}
	case GitLab:
		if h.BaseURL == "" {
			h.BaseURL = "https://gitlab.com"
		}
	case Bitbucket:
		if h.BaseURL == "" {
			h.BaseURL = "https://api.bitbucket.org/2.0"
		}
	default:
		return fmt.Errorf("platform %q: want github, gitlab or bitbucket", h.Platform)
	}
	u, err := url.Parse(h.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("base URL %q: want an http(s) URL", h.BaseURL)
	}
	h.Host = WebHost(h.Platform, h.BaseURL)
	if h.Name == "" {
		h.Name = h.Host
	}
	return nil
}

// WebHost maps an API or instance URL to the host repositories live on.
func WebHost(platform, baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch {
	case platform == GitHub && host == "api.github.com":
		return "github.com"
	case platform == Bitbucket && host == "api.bitbucket.org":
		return "bitbucket.org"
	}
	return host
}

func clean(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Validate checks that a host can authenticate API calls and webhooks.
func (h Host) Validate() error {
	switch h.Platform {
	// The host's own token is optional: credentials (issue #136) can cover
	// its repositories instead, and a repository neither covers fails with
	// a message naming its group. Half-set alternatives are still refused.
	case GitHub:
		if (h.AppID != 0) != (h.AppPrivateKey != "") {
			return errors.New("github: a GitHub App needs both its ID and its private key")
		}
	case Bitbucket:
		if (h.Email != "") != (h.APIToken != "") {
			return errors.New("bitbucket: an API token needs the account email, and the email its API token")
		}
	}
	// Webhooks (endpoints, added under Manage webhooks) have their own URL
	// and secrets.
	return nil
}

func checkSigningTokens(tokens []string) error {
	for _, t := range tokens {
		raw, ok := strings.CutPrefix(t, "whsec_")
		if _, err := base64.StdEncoding.DecodeString(raw); !ok || err != nil {
			return errors.New(`gitlab: a signing token starts with "whsec_" followed by base64 (copy it from GitLab)`)
		}
	}
	return nil
}

// Registry holds the hosts in memory. It is safe for concurrent use.
type Registry struct {
	cfg    *config.Config
	st     store.Store
	box    *secrets.Box
	logger *slog.Logger

	mu        sync.RWMutex
	list      []Host
	endpoints []Endpoint
	creds     []Credential
	mode      string // "database" or "config"
	// shared are hosts that still store shared-URL webhook secrets, with
	// the audit's secrets_changed entries for deleting them (see
	// dropSharedSecrets).
	shared map[int64][]string
	// version is the store's HostsVersion at the last load (see Refresh);
	// refreshMu keeps refreshes one at a time.
	version   int64
	refreshMu sync.Mutex
	notes     []string
	// expiry is what is known about each host's token expiry (by ID);
	// checks the last Test result.
	expiry map[int64]TokenExpiry
	checks map[int64]Check
	// credExpiry and credChecks are the same for credentials (by ID).
	credExpiry map[int64]TokenExpiry
	credChecks map[int64]Check
	// OnChange runs after hosts change (e.g. to refresh bot identities).
	OnChange func()
	// Lic gates the Hosts feature: without it only the first host of each
	// platform is active, and per-host bot identities are ignored.
	Lic *license.Manager
}

// New returns a Registry. st and box may be nil.
func New(cfg *config.Config, st store.Store, box *secrets.Box, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{cfg: cfg, st: st, box: box, logger: logger}
}

// FromConfig returns the hosts in the config file.
func FromConfig(cfg *config.Config) []Host {
	var out []Host
	g := cfg.GitHub
	if g.AppID != 0 || g.Token != "" {
		key := g.PrivateKey
		if key == "" && g.PrivateKeyFile != "" {
			key = "file:" + g.PrivateKeyFile // read when used, see AppKey
		}
		out = append(out, Host{Platform: GitHub, BaseURL: g.APIURL, Enabled: true,
			Settings: Settings{AppID: g.AppID, BotName: g.BotName, Reviewer: g.Reviewer},
			Secrets:  Secrets{Token: g.Token, AppPrivateKey: key}})
	}
	for _, l := range cfg.GitLab {
		out = append(out, Host{Platform: GitLab, BaseURL: l.BaseURL, Enabled: true,
			Settings: Settings{RequireSignature: l.RequireSignature, SkipTLSVerify: l.SkipTLSVerify, BotName: l.BotName, Reviewer: l.Reviewer},
			Secrets:  Secrets{Token: l.Token}})
	}
	for _, b := range cfg.Bitbucket {
		out = append(out, Host{Platform: Bitbucket, BaseURL: b.APIURL, Enabled: true,
			Settings: Settings{Email: b.Email, BotName: b.BotName, Reviewer: b.Reviewer},
			Secrets:  Secrets{Token: b.Token, APIToken: b.APIToken}})
	}
	for i := range out {
		_ = out[i].Normalize()
	}
	return out
}

// Init copies config-file hosts into the database on first start (when it
// has none) and loads the registry. It returns notes for the log.
func (r *Registry) Init(ctx context.Context) ([]string, error) {
	if r.st != nil {
		rows, err := r.st.ListHosts(ctx)
		if err != nil {
			return nil, err
		}
		file := FromConfig(r.cfg)
		if len(rows) == 0 && len(file) > 0 {
			needKey := false
			for _, h := range file {
				needKey = needKey || !h.empty()
			}
			if needKey && r.box == nil {
				r.addNote("hosts come from the config file (read-only): set " + secrets.EnvKey + " to manage them in the admin panel")
			} else {
				for _, h := range file {
					if _, err := r.save(ctx, h, "config file (initial)"); err != nil {
						return nil, fmt.Errorf("copy host %s from the config file: %w", h.Host, err)
					}
				}
				r.addNote(fmt.Sprintf("copied %d host(s) from the config file into the database; manage them in the admin panel from now on", len(file)))
			}
		}
	}
	if err := r.Load(ctx); err != nil {
		return r.notes, err
	}
	return r.notes, r.dropSharedSecrets(ctx)
}

func (r *Registry) addNote(n string) { r.notes = append(r.notes, n) }

// Load reads the hosts from the database (or the config file).
func (r *Registry) Load(ctx context.Context) error {
	list, mode := FromConfig(r.cfg), "config"
	shared := map[int64][]string{}
	var version int64
	if r.st != nil {
		// Read the version first: a write during the load bumps it again,
		// so the next Refresh reloads.
		v, err := r.st.HostsVersion(ctx)
		if err != nil {
			return err
		}
		version = v
		rows, err := r.st.ListHosts(ctx)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			list, mode = nil, "database"
			for _, row := range rows {
				h, legacy, err := r.decode(row)
				if err != nil {
					return fmt.Errorf("host %s:%s: %w", row.Platform, row.Host, err)
				}
				list = append(list, h)
				if len(legacy) > 0 {
					shared[h.ID] = legacy
				}
			}
		} else if r.box != nil {
			mode = "database" // nothing stored yet, but the admin panel can add hosts
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Platform+list[i].Host < list[j].Platform+list[j].Host })
	var eps []Endpoint
	var creds []Credential
	if r.st != nil && mode == "database" && r.box != nil {
		rows, err := r.st.ListWebhookEndpoints(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			e, err := r.decodeEndpoint(row)
			if err != nil {
				return fmt.Errorf("webhook %s: %w", row.ID, err)
			}
			eps = append(eps, e)
		}
		crows, err := r.st.ListHostCredentials(ctx)
		if err != nil {
			return err
		}
		for _, row := range crows {
			c, err := r.decodeCredential(row)
			if err != nil {
				return fmt.Errorf("credential %d: %w", row.ID, err)
			}
			creds = append(creds, c)
		}
	}
	r.mu.Lock()
	r.list, r.mode, r.endpoints, r.creds, r.shared, r.version = list, mode, eps, creds, shared, version
	r.mu.Unlock()
	return nil
}

// Refresh reloads the hosts and webhooks when the database has changed
// since the last load: a write by another replica or by "console hosts
// import". It costs one small query when nothing changed. changed reports
// a reload (OnChange has run).
func (r *Registry) Refresh(ctx context.Context) (changed bool, err error) {
	if r.st == nil || r.box == nil {
		return false, nil
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	v, err := r.st.HostsVersion(ctx)
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	same := v == r.version
	r.mu.RUnlock()
	if same {
		return false, nil
	}
	if err := r.Load(ctx); err != nil {
		return false, err
	}
	if r.OnChange != nil {
		r.OnChange()
	}
	return true, nil
}

// Mode is "database" (managed in the admin panel) or "config" (read-only).
func (r *Registry) Mode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mode
}

// All returns every host, enabled or not.
func (r *Registry) All() []Host {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Host(nil), r.list...)
}

// Enabled returns the active hosts of a platform ("" for all): enabled,
// and within what the license covers.
func (r *Registry) Enabled(platform string) []Host {
	var out []Host
	for _, h := range r.All() {
		if h.Enabled && (platform == "" || h.Platform == platform) && r.Active(h) {
			if !r.Lic.Has(license.Hosts) {
				h.BotName, h.Reviewer = "", "" // the token user
			}
			out = append(out, h)
		}
	}
	return out
}

// Active reports whether the license covers h: with the Hosts feature every
// host; without it only the first enabled host of each platform (the
// oldest, or the first in the config file).
func (r *Registry) Active(h Host) bool {
	if r.Lic.Has(license.Hosts) {
		return true
	}
	first, ok := r.first(h.Platform)
	return ok && first.ID == h.ID && first.Host == h.Host
}

func (r *Registry) first(platform string) (Host, bool) {
	var best Host
	found := false
	for _, h := range r.All() { // sorted by platform+host; config hosts have ID 0
		if h.Enabled && h.Platform == platform && (!found || (h.ID != 0 && h.ID < best.ID)) {
			best, found = h, true
		}
	}
	return best, found
}

// CheckLicense returns a *license.Error when saving h would need the Hosts
// feature: a second host on a platform, or a custom bot identity.
func (r *Registry) CheckLicense(h Host) error {
	if r.Lic.Has(license.Hosts) {
		return nil
	}
	if h.BotName != "" || h.Reviewer != "" {
		return &license.Error{Feature: license.Hosts}
	}
	for _, o := range r.All() {
		if o.Platform == h.Platform && o.ID != h.ID {
			return &license.Error{Feature: license.Hosts}
		}
	}
	return nil
}

// Find returns the enabled host of a platform by web host. With a single
// enabled host for the platform and an empty host, that host is returned.
func (r *Registry) Find(platform, host string) (Host, bool) {
	host = strings.ToLower(host)
	hs := r.Enabled(platform)
	for _, h := range hs {
		if h.Host == host {
			return h, true
		}
	}
	if host == "" && len(hs) == 1 {
		return hs[0], true
	}
	return Host{}, false
}

// Get returns a host by ID (enabled or not).
func (r *Registry) Get(id int64) (Host, bool) {
	for _, h := range r.All() {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}

// Save validates, encrypts and stores a host, then reloads.
func (r *Registry) Save(ctx context.Context, h Host, actor string) (Host, error) {
	if r.Mode() != "database" || r.st == nil {
		return Host{}, errors.New("hosts come from the config file: set " + secrets.EnvKey + " (and a database) to manage them here")
	}
	saved, err := r.save(ctx, h, actor)
	if err != nil {
		return Host{}, err
	}
	if err := r.Load(ctx); err != nil {
		return Host{}, err
	}
	if r.OnChange != nil {
		r.OnChange()
	}
	return saved, nil
}

// save stores h. removed names secrets deleted outside h.Secrets (the
// shared URL's), for the audit's secrets_changed.
func (r *Registry) save(ctx context.Context, h Host, actor string, removed ...string) (Host, error) {
	if err := h.Normalize(); err != nil {
		return Host{}, err
	}
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	cfgJSON, err := json.Marshal(h.Settings)
	if err != nil {
		return Host{}, err
	}
	var sealed string
	if !h.empty() {
		pt, err := json.Marshal(h.Secrets)
		if err != nil {
			return Host{}, err
		}
		if sealed, err = r.box.Seal(pt); err != nil {
			return Host{}, err
		}
	}
	var old Secrets
	for _, x := range r.All() {
		if x.Platform == h.Platform && x.Host == h.Host {
			old = x.Secrets
		}
	}
	row, err := r.st.PutHost(ctx, store.HostRow{Platform: h.Platform, Host: h.Host, BaseURL: h.BaseURL, Name: h.Name,
		Enabled: h.Enabled, Config: cfgJSON, Secrets: sealed, SecretsChanged: append(SecretChanges(old, h.Secrets), removed...)}, actor)
	if err != nil {
		return Host{}, err
	}
	h.ID = row.ID
	return h, nil
}

// Delete removes a host and reloads.
func (r *Registry) Delete(ctx context.Context, id int64, actor string) error {
	if r.Mode() != "database" || r.st == nil {
		return errors.New("hosts come from the config file")
	}
	if err := r.st.DeleteHost(ctx, id, actor); err != nil {
		return err
	}
	if err := r.Load(ctx); err != nil {
		return err
	}
	if r.OnChange != nil {
		r.OnChange()
	}
	return nil
}

// decode opens a stored host; legacy lists the shared URL's webhook
// secrets it still has, as secrets_changed entries for removing them
// ("webhook_secrets:-1"; see dropSharedSecrets).
func (r *Registry) decode(row store.HostRow) (h Host, legacy []string, err error) {
	h = Host{ID: row.ID, Platform: row.Platform, Host: row.Host, BaseURL: row.BaseURL, Name: row.Name, Enabled: row.Enabled}
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &h.Settings); err != nil {
			return h, nil, fmt.Errorf("settings: %w", err)
		}
	}
	if row.Secrets != "" {
		pt, err := r.box.Open(row.Secrets)
		if err != nil {
			return h, nil, err
		}
		if err := json.Unmarshal(pt, &h.Secrets); err != nil {
			return h, nil, fmt.Errorf("secrets: %w", err)
		}
		var old sharedHookSecrets
		if json.Unmarshal(pt, &old) == nil {
			legacy = append(listChange("webhook_secrets", old.WebhookSecrets, nil), listChange("signing_tokens", old.SigningTokens, nil)...)
		}
	}
	return h, legacy, nil
}

// dropSharedSecrets deletes the webhook secrets still stored on hosts
// from the shared /webhooks/<platform> URL, removed in 0.12: saving a
// host stores only its current secrets.
func (r *Registry) dropSharedSecrets(ctx context.Context) error {
	r.mu.RLock()
	shared := maps.Clone(r.shared)
	r.mu.RUnlock()
	if len(shared) == 0 || r.st == nil || r.box == nil {
		return nil
	}
	var names []string
	for _, id := range slices.Sorted(maps.Keys(shared)) {
		h, ok := r.Get(id)
		if !ok {
			continue
		}
		// The audit entry lists what was deleted (issue #133): the
		// host's own settings and secrets are unchanged.
		if _, err := r.save(ctx, h, "upgrade: shared webhook URL removed", shared[id]...); err != nil {
			return fmt.Errorf("remove the shared webhook secrets of %s: %w", h.Host, err)
		}
		names = append(names, h.Platform+" "+h.Host)
	}
	r.addNote(fmt.Sprintf("removed the webhook secrets of the shared /webhooks/<platform> URL from %s: it no longer exists, so point those webhooks at a URL from Code hosts → Manage webhooks", strings.Join(names, ", ")))
	return r.Load(ctx)
}
