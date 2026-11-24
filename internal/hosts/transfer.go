package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/store"
)

// File is the import/export format for hosts and their webhooks (see
// `console hosts export` and `console hosts import`). Secret fields that
// are absent keep the stored value on import; ${VAR} references are
// expanded from the environment, so a file can live in Git without the
// secrets themselves.
type File struct {
	Hosts []FileHost `yaml:"hosts"`
}

// FileHost is one host in a File.
type FileHost struct {
	Platform string   `yaml:"platform"`
	BaseURL  string   `yaml:"base_url,omitempty"`
	Name     string   `yaml:"name,omitempty"`
	Enabled  *bool    `yaml:"enabled,omitempty"`
	Settings Settings `yaml:"settings,omitempty"`

	Token         *string `yaml:"token,omitempty"`
	AppPrivateKey *string `yaml:"app_private_key,omitempty"`
	APIToken      *string `yaml:"api_token,omitempty"`
	// WebhookSecrets and SigningTokens were the shared webhook URL's
	// (removed in 0.12): read from older files and ignored.
	WebhookSecrets *[]string `yaml:"webhook_secrets,omitempty"`
	SigningTokens  *[]string `yaml:"signing_tokens,omitempty"`

	Webhooks []FileWebhook `yaml:"webhooks,omitempty"`
	// Credentials cover groups or patterns instead of the host's own token
	// (issue #136); matched by pattern on import.
	Credentials []FileCredential `yaml:"credentials,omitempty"`
}

// FileCredential is one host credential in a File.
type FileCredential struct {
	Pattern  string             `yaml:"pattern"`
	Name     string             `yaml:"name,omitempty"`
	Enabled  *bool              `yaml:"enabled,omitempty"`
	Settings CredentialSettings `yaml:"settings,omitempty"`
	Token    *string            `yaml:"token,omitempty"`
	APIToken *string            `yaml:"api_token,omitempty"`
}

// FileWebhook is one webhook endpoint in a File. Keeping the ID keeps its
// URL, so hooks on the code host need no change after a move.
type FileWebhook struct {
	ID            string    `yaml:"id,omitempty"`
	Name          string    `yaml:"name,omitempty"`
	Enabled       *bool     `yaml:"enabled,omitempty"`
	Secret        *string   `yaml:"secret,omitempty"`
	SigningTokens *[]string `yaml:"signing_tokens,omitempty"`
	// AcceptSecretToken: with a signing token, also accept the secret token.
	AcceptSecretToken *bool `yaml:"accept_secret_token,omitempty"`
	// URL is informational (written on export, ignored on import).
	URL string `yaml:"url,omitempty"`
}

// Export returns every host and webhook. Without withSecrets, secret
// fields are left out (an import of that file keeps the stored secrets).
// baseURL, when set, fills in each webhook's URL.
func (r *Registry) Export(withSecrets bool, baseURL string) File {
	f := File{Hosts: []FileHost{}}
	for _, h := range r.All() {
		enabled := h.Enabled
		fh := FileHost{Platform: h.Platform, BaseURL: h.BaseURL, Name: h.Name, Enabled: &enabled, Settings: h.Settings}
		if withSecrets {
			fh.Token, fh.AppPrivateKey, fh.APIToken = optional(h.Token), optional(h.AppPrivateKey), optional(h.APIToken)
		}
		for _, e := range r.Endpoints(h.ID) {
			en := e.Enabled
			fw := FileWebhook{ID: e.ID, Name: e.Name, Enabled: &en}
			if e.AcceptSecretToken {
				accept := true
				fw.AcceptSecretToken = &accept
			}
			if baseURL != "" {
				fw.URL = strings.TrimRight(baseURL, "/") + e.Path(h.Platform)
			}
			if withSecrets {
				if len(e.WebhookSecrets) > 0 {
					fw.Secret = optional(e.WebhookSecrets[0])
				}
				fw.SigningTokens = optionalList(e.SigningTokens)
			}
			fh.Webhooks = append(fh.Webhooks, fw)
		}
		for _, c := range r.Credentials(h.ID) {
			en := c.Enabled
			fc := FileCredential{Pattern: c.Pattern, Name: c.Name, Enabled: &en, Settings: c.Settings}
			if withSecrets {
				fc.Token, fc.APIToken = optional(c.Token), optional(c.APIToken)
			}
			fh.Credentials = append(fh.Credentials, fc)
		}
		f.Hosts = append(f.Hosts, fh)
	}
	return f
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalList(l []string) *[]string {
	if len(l) == 0 {
		return nil
	}
	return &l
}

// ParseFile reads a File, expanding ${VAR} references and rejecting
// unknown fields (so a typo fails instead of being skipped).
func ParseFile(data []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(config.ExpandEnv(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("hosts file: %w", err)
	}
	return f, nil
}

// ImportResult says what an import did (or, in a dry run, would do).
type ImportResult struct {
	Lines []string
	// Secrets holds webhook secrets generated for webhooks without one,
	// keyed by "<platform>:<host> <name> (<id>)"; they are shown once.
	Secrets map[string]string
}

// Import creates or updates hosts (matched by platform and web host) and
// their webhooks (matched by ID). It never deletes. Everything is checked
// before anything is written; with dryRun nothing is written at all.
func (r *Registry) Import(ctx context.Context, f File, actor string, dryRun bool) (ImportResult, error) {
	res := ImportResult{Secrets: map[string]string{}}
	if r.Mode() != "database" || r.st == nil || r.box == nil {
		return res, errors.New("hosts can only be imported with a database and CODEREVIEW_CONSOLE_SECRET_KEY")
	}
	var plan []planned
	seenHosts, seenHooks := map[string]bool{}, map[string]bool{}
	for i, fh := range f.Hosts {
		h := Host{Platform: fh.Platform, BaseURL: fh.BaseURL, Enabled: true}
		if err := h.Normalize(); err != nil {
			return res, fmt.Errorf("hosts[%d]: %w", i, err)
		}
		key := h.Platform + ":" + h.Host
		if seenHosts[key] {
			return res, fmt.Errorf("hosts[%d]: %s is listed twice", i, key)
		}
		seenHosts[key] = true
		created := true
		var stored Host
		for _, x := range r.All() {
			if x.Platform == h.Platform && x.Host == h.Host {
				h, stored, created = x, x, false // start from the stored host: absent secrets are kept
			}
		}
		if fh.BaseURL != "" {
			h.BaseURL = fh.BaseURL
		}
		if fh.Name != "" {
			h.Name = fh.Name
		}
		if fh.Enabled != nil {
			h.Enabled = *fh.Enabled
		}
		h.Settings = fh.Settings
		setStr(&h.Token, fh.Token)
		setStr(&h.AppPrivateKey, fh.AppPrivateKey)
		setStr(&h.APIToken, fh.APIToken)
		if err := h.Normalize(); err != nil {
			return res, fmt.Errorf("%s: %w", key, err)
		}
		if err := h.Validate(); err != nil {
			return res, fmt.Errorf("%s: %w", key, err)
		}
		if err := r.importLicense(h, created, stored, plan); err != nil {
			return res, fmt.Errorf("%s: %w", key, err)
		}
		p := planned{host: h, created: created, sharedSecrets: fh.WebhookSecrets != nil || fh.SigningTokens != nil}
		for j, fw := range fh.Webhooks {
			id := strings.ToLower(strings.TrimSpace(fw.ID))
			var e Endpoint
			isNew := true
			if id == "" {
				id = uuid.NewString()
			} else if _, err := uuid.Parse(id); err != nil {
				return res, fmt.Errorf("%s webhooks[%d]: id %q is not a UUID", key, j, fw.ID)
			}
			if seenHooks[id] {
				return res, fmt.Errorf("%s webhooks[%d]: id %s is listed twice", key, j, id)
			}
			seenHooks[id] = true
			if old, ok := r.Endpoint(id); ok {
				if created || old.HostID != h.ID {
					return res, fmt.Errorf("%s webhooks[%d]: id %s belongs to another host", key, j, id)
				}
				e, isNew = old, false
			} else {
				e = Endpoint{ID: id, Enabled: true}
			}
			if fw.Name != "" || isNew {
				e.Name = strings.TrimSpace(fw.Name)
				if e.Name == "" {
					e.Name = "Webhook"
				}
			}
			if fw.Enabled != nil {
				e.Enabled = *fw.Enabled
			}
			if fw.Secret != nil && strings.TrimSpace(*fw.Secret) != "" {
				e.WebhookSecrets = []string{strings.TrimSpace(*fw.Secret)}
			}
			if fw.AcceptSecretToken != nil {
				e.AcceptSecretToken = *fw.AcceptSecretToken
			}
			if fw.SigningTokens != nil {
				e.SigningTokens = clean(*fw.SigningTokens)
			}
			if len(e.SigningTokens) > 0 && h.Platform != GitLab {
				return res, fmt.Errorf("%s webhooks[%d]: signing tokens are for GitLab webhooks", key, j)
			}
			if err := checkSigningTokens(e.SigningTokens); err != nil {
				return res, fmt.Errorf("%s webhooks[%d]: %w", key, j, err)
			}
			p.webhooks, p.newHooks = append(p.webhooks, e), append(p.newHooks, isNew)
		}
		if len(fh.Credentials) > 0 && !r.Lic.Has(license.Hosts) {
			return res, fmt.Errorf("%s: credentials: %w", key, &license.Error{Feature: license.Hosts})
		}
		seenPatterns := map[string]bool{}
		for j, fc := range fh.Credentials {
			c := Credential{Enabled: true, Pattern: fc.Pattern}
			norm, err := store.NormalizePattern(fc.Pattern)
			if err != nil {
				return res, fmt.Errorf("%s credentials[%d]: %w", key, j, err)
			}
			isNew := true
			if !created {
				for _, old := range r.Credentials(h.ID) {
					if old.Pattern == norm {
						c, isNew = old, false // absent secrets are kept
					}
				}
			}
			if fc.Name != "" || isNew {
				c.Name = fc.Name
			}
			if fc.Enabled != nil {
				c.Enabled = *fc.Enabled
			}
			c.Settings = fc.Settings
			setStr(&c.Token, fc.Token)
			setStr(&c.APIToken, fc.APIToken)
			if err := c.Normalize(h); err != nil {
				return res, fmt.Errorf("%s credentials[%d]: %w", key, j, err)
			}
			if seenPatterns[c.Pattern] {
				return res, fmt.Errorf("%s credentials[%d]: %s is listed twice", key, j, c.Pattern)
			}
			seenPatterns[c.Pattern] = true
			p.creds, p.newCreds = append(p.creds, c), append(p.newCreds, isNew)
		}
		plan = append(plan, p)
	}

	verb := func(created bool) string {
		switch {
		case dryRun && created:
			return "would create"
		case dryRun:
			return "would update"
		case created:
			return "created"
		}
		return "updated"
	}
	for _, p := range plan {
		h := p.host
		if !dryRun {
			saved, err := r.Save(ctx, h, actor)
			if err != nil {
				return res, fmt.Errorf("%s:%s: %w", h.Platform, h.Host, err)
			}
			h = saved
		}
		res.Lines = append(res.Lines, fmt.Sprintf("host %s:%s: %s", h.Platform, h.Host, verb(p.created)))
		if p.sharedSecrets {
			res.Lines = append(res.Lines, "  its webhook_secrets/signing_tokens were ignored: the shared webhook URL was removed, so each webhook under webhooks: has its own")
		}
		for i, e := range p.webhooks {
			label := fmt.Sprintf("%s:%s %s (%s)", h.Platform, h.Host, e.Name, e.ID)
			if len(e.WebhookSecrets) == 0 {
				secret, err := GenerateSecret()
				if err != nil {
					return res, err
				}
				e.WebhookSecrets = []string{secret}
				res.Secrets[label] = secret
			}
			if !dryRun {
				e.HostID = h.ID
				if _, err := r.SaveEndpoint(ctx, e, actor); err != nil {
					return res, fmt.Errorf("webhook %s: %w", label, err)
				}
			}
			res.Lines = append(res.Lines, fmt.Sprintf("  webhook %s: %s", label, verb(p.newHooks[i])))
		}
		for i, c := range p.creds {
			if !dryRun {
				c.HostID = h.ID
				if _, err := r.SaveCredential(ctx, c, actor); err != nil {
					return res, fmt.Errorf("credential %s:%s %s: %w", h.Platform, h.Host, c.Pattern, err)
				}
			}
			res.Lines = append(res.Lines, fmt.Sprintf("  credential %s: %s", c.Pattern, verb(p.newCreds[i])))
		}
	}
	if dryRun {
		res.Secrets = map[string]string{} // nothing was stored, so nothing to show
	}
	return res, nil
}

type planned struct {
	host    Host
	created bool
	// sharedSecrets: the file had host-level webhook secrets (ignored).
	sharedSecrets bool
	webhooks      []Endpoint
	newHooks      []bool
	creds         []Credential
	newCreds      []bool
}

// importLicense applies the admin panel's license rules to an import:
// without the Hosts feature, no second host on a platform (counting hosts
// this import creates) and no new custom bot identity.
func (r *Registry) importLicense(h Host, created bool, stored Host, plan []planned) error {
	if r.Lic.Has(license.Hosts) {
		return nil
	}
	if created {
		if err := r.CheckLicense(h); err != nil {
			return err
		}
		for _, p := range plan {
			if p.created && p.host.Platform == h.Platform {
				return &license.Error{Feature: license.Hosts}
			}
		}
		return nil
	}
	if (h.BotName != stored.BotName || h.Reviewer != stored.Reviewer) && (h.BotName != "" || h.Reviewer != "") {
		return &license.Error{Feature: license.Hosts}
	}
	return nil
}

func setStr(dst *string, v *string) {
	if v != nil {
		*dst = strings.TrimSpace(*v)
	}
}
