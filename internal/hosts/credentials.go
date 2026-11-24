package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store"
)

// Credential is an extra credential of a host, used instead of the host's
// own for the repositories its pattern covers (issue #136): a GitLab group
// access token per group, a GitHub token per owner, a Bitbucket token per
// workspace or repository pattern. The most specific matching credential
// wins, the way Rules resolve; the host's own token covers the rest. It is
// part of the licensed console (license feature hosts).
type Credential struct {
	ID      int64  `json:"id"`
	HostID  int64  `json:"host_id"`
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Enabled bool   `json:"enabled"`
	// Settings: the bot name and reviewer for these repositories (else the
	// host's, the console's, then this token's user), and the account
	// email that goes with a Bitbucket API token.
	Settings          CredentialSettings `json:"settings"`
	CreatedAt         time.Time          `json:"created_at"`
	CreatedBy         string             `json:"created_by"`
	UpdatedAt         time.Time          `json:"updated_at"`
	UpdatedBy         string             `json:"updated_by"`
	CredentialSecrets `json:"-"`
}

// CredentialSettings are a credential's non-secret settings.
type CredentialSettings struct {
	BotName  string `json:"bot_name,omitempty" yaml:"bot_name,omitempty"`
	Reviewer string `json:"reviewer,omitempty" yaml:"reviewer,omitempty"`
	Email    string `json:"email,omitempty" yaml:"email,omitempty"` // Bitbucket, with APIToken
}

// CredentialSecrets are a credential's secret values, stored encrypted.
type CredentialSecrets struct {
	Token    string `json:"token,omitempty"`
	APIToken string `json:"api_token,omitempty"` // Bitbucket, with Email
}

// CredentialStatus says which secrets are set, for display.
type CredentialStatus struct {
	Token    bool `json:"token"`
	APIToken bool `json:"api_token"`
}

// SecretStatus reports which secrets are set (never their values).
func (c Credential) SecretStatus() CredentialStatus {
	return CredentialStatus{Token: c.Token != "", APIToken: c.APIToken != ""}
}

// Label names the credential for logs and warnings ("GitLab › platform team").
func (c Credential) Label(h Host) string {
	name := c.Name
	if name == "" {
		name = c.Pattern
	}
	return firstNonEmptyStr(h.Name, h.Host) + " › " + name
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// rule is the credential's pattern as a rule, to reuse the Rules matching
// and ordering (group and subgroups, * within one level).
func (c Credential) rule(h Host) store.Rule {
	return store.Rule{ID: c.ID, Platform: h.Platform, Host: h.Host, Pattern: c.Pattern}
}

// Credentials returns a host's credentials, by pattern.
func (r *Registry) Credentials(hostID int64) []Credential {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Credential
	for _, c := range r.creds {
		if c.HostID == hostID {
			out = append(out, c)
		}
	}
	return out
}

// Credential returns a credential by ID.
func (r *Registry) Credential(id int64) (Credential, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.creds {
		if c.ID == id {
			return c, true
		}
	}
	return Credential{}, false
}

// CredentialFor returns the enabled credential of h that covers repoPath
// most specifically (false: the host's own credentials apply). Without
// the hosts license feature credentials are not used.
func (r *Registry) CredentialFor(h Host, repoPath string) (Credential, bool) {
	if h.ID == 0 || repoPath == "" || !r.Lic.Has(license.Hosts) {
		return Credential{}, false
	}
	var best Credential
	var bestRule store.Rule
	found := false
	for _, c := range r.Credentials(h.ID) {
		if !c.Enabled {
			continue
		}
		cr := c.rule(h)
		if !cr.Matches(h.Platform, h.Host, repoPath) {
			continue
		}
		if !found || store.MoreSpecific(cr, bestRule) {
			best, bestRule, found = c, cr, true
		}
	}
	return best, found
}

// ForRepo returns h as it acts for repoPath: with the matching
// credential's token, bot name, reviewer and email instead of the host's.
// Everything that talks to the code host for that repository (review jobs,
// comments, reactions, learnings) uses the result.
func (r *Registry) ForRepo(h Host, repoPath string) Host {
	c, ok := r.CredentialFor(h, repoPath)
	if !ok {
		return h
	}
	return h.WithCredential(c)
}

// WithCredential returns h acting with credential c.
func (h Host) WithCredential(c Credential) Host {
	h.Credential = c.ID
	// A credential is a token: a GitHub App on the host doesn't apply to
	// the repositories it covers.
	h.Secrets = Secrets{Token: c.Token, APIToken: c.APIToken}
	h.AppID = 0
	h.Email = c.Settings.Email
	if c.Settings.BotName != "" {
		h.BotName = c.Settings.BotName
	}
	if c.Settings.Reviewer != "" {
		h.Reviewer = c.Settings.Reviewer
	}
	return h
}

// Normalize cleans a credential and checks it against its host.
func (c *Credential) Normalize(h Host) error {
	c.Name = strings.TrimSpace(c.Name)
	if len(c.Name) > 100 {
		return errors.New("name: at most 100 characters")
	}
	p, err := store.NormalizePattern(c.Pattern)
	if err != nil {
		return err
	}
	if p == "" {
		return errors.New("pattern: set a group, owner or pattern (acme, acme/platform, acme/svc-*); the host's own token covers the whole host")
	}
	c.Pattern = p
	c.Settings.BotName = strings.TrimPrefix(strings.TrimSpace(c.Settings.BotName), "@")
	c.Settings.Reviewer = strings.TrimPrefix(strings.TrimSpace(c.Settings.Reviewer), "@")
	c.Settings.Email = strings.TrimSpace(c.Settings.Email)
	c.Token, c.APIToken = strings.TrimSpace(c.Token), strings.TrimSpace(c.APIToken)
	switch h.Platform {
	case GitHub, GitLab:
		if c.Token == "" {
			return errors.New(h.Platform + ": a credential needs a token")
		}
		if c.APIToken != "" || c.Settings.Email != "" {
			return errors.New("an email and API token are for Bitbucket")
		}
	case Bitbucket:
		if c.Token == "" && (c.Settings.Email == "" || c.APIToken == "") {
			return errors.New("bitbucket: set an access token, or an account email and API token")
		}
	}
	return nil
}

// SaveCredential validates, encrypts and stores a credential (ID 0 adds
// one), then reloads.
func (r *Registry) SaveCredential(ctx context.Context, c Credential, actor string) (Credential, error) {
	if r.Mode() != "database" || r.st == nil || r.box == nil {
		return Credential{}, errors.New("credentials are managed in the admin panel: set " + secrets.EnvKey + " and a database")
	}
	h, ok := r.Get(c.HostID)
	if !ok {
		return Credential{}, errors.New("host not found")
	}
	if err := c.Normalize(h); err != nil {
		return Credential{}, err
	}
	cfg, err := json.Marshal(c.Settings)
	if err != nil {
		return Credential{}, err
	}
	pt, err := json.Marshal(c.CredentialSecrets)
	if err != nil {
		return Credential{}, err
	}
	sealed, err := r.box.Seal(pt)
	if err != nil {
		return Credential{}, err
	}
	var old CredentialSecrets
	if c.ID != 0 {
		prev, ok := r.Credential(c.ID)
		if !ok || prev.HostID != c.HostID {
			return Credential{}, store.ErrNotFound
		}
		old = prev.CredentialSecrets
	}
	changed := append(valueChange("token", old.Token, c.Token), valueChange("api_token", old.APIToken, c.APIToken)...)
	row, err := r.st.PutHostCredential(ctx, store.HostCredential{ID: c.ID, HostID: c.HostID, Name: c.Name, Pattern: c.Pattern, Enabled: c.Enabled,
		Config: cfg, Secrets: sealed, SecretsChanged: nonNilStrings(changed)}, actor)
	if errors.Is(err, store.ErrDuplicate) {
		return Credential{}, fmt.Errorf("%s already has a credential for %s: edit that one", h.Host, c.Pattern)
	}
	if err != nil {
		return Credential{}, err
	}
	if err := r.Load(ctx); err != nil {
		return Credential{}, err
	}
	if r.OnChange != nil {
		r.OnChange()
	}
	saved, _ := r.Credential(row.ID)
	return saved, nil
}

// DeleteCredential removes a credential and reloads; its repositories fall
// back to the next matching credential or the host's own.
func (r *Registry) DeleteCredential(ctx context.Context, id int64, actor string) error {
	if r.Mode() != "database" || r.st == nil {
		return errors.New("credentials are managed in the admin panel")
	}
	if err := r.st.DeleteHostCredential(ctx, id, actor); err != nil {
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

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (r *Registry) decodeCredential(row store.HostCredential) (Credential, error) {
	c := Credential{ID: row.ID, HostID: row.HostID, Name: row.Name, Pattern: row.Pattern, Enabled: row.Enabled,
		CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy}
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &c.Settings); err != nil {
			return c, fmt.Errorf("settings: %w", err)
		}
	}
	if row.Secrets != "" {
		pt, err := r.box.Open(row.Secrets)
		if err != nil {
			return c, err
		}
		if err := json.Unmarshal(pt, &c.CredentialSecrets); err != nil {
			return c, fmt.Errorf("secrets: %w", err)
		}
	}
	return c, nil
}
