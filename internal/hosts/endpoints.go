package hosts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store"
)

// Endpoint is one webhook URL of a host: /webhooks/<platform>/<ID>. The ID
// is a random UUID generated when an admin adds the webhook. Deliveries on
// that URL are verified against the endpoint's own secrets only, so one
// webhook (say, one GitLab group's) can be rotated or revoked on its own.
// The shared /webhooks/<platform> URL keeps using the host's secrets.
type Endpoint struct {
	ID              string    `json:"id"`
	HostID          int64     `json:"host_id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	CreatedBy       string    `json:"created_by"`
	UpdatedAt       time.Time `json:"updated_at"`
	UpdatedBy       string    `json:"updated_by"`
	EndpointSecrets `json:"-"`
}

// EndpointSecrets are an endpoint's secret values; they are stored encrypted.
// WebhookSecrets holds the secret the console generated (GitHub and
// Bitbucket sign with it; GitLab sends it as the secret token).
// SigningTokens are GitLab webhook signing tokens (GitLab 19.0+), which
// GitLab generates and the admin pastes in.
type EndpointSecrets struct {
	WebhookSecrets []string `json:"webhook_secrets,omitempty"`
	SigningTokens  []string `json:"signing_tokens,omitempty"`
}

// EndpointStatus says which endpoint secrets are set, for display.
type EndpointStatus struct {
	WebhookSecret bool `json:"webhook_secret"`
	SigningTokens int  `json:"signing_tokens"`
}

// SecretStatus reports which secrets are set (never their values).
func (e Endpoint) SecretStatus() EndpointStatus {
	return EndpointStatus{WebhookSecret: len(e.WebhookSecrets) > 0, SigningTokens: len(e.SigningTokens)}
}

// Path is the endpoint's URL path.
func (e Endpoint) Path(platform string) string { return "/webhooks/" + platform + "/" + e.ID }

// GenerateSecret returns a new random webhook secret (256 bits, hex).
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ErrNoEndpoint means a webhook URL does not name a usable endpoint.
var ErrNoEndpoint = errors.New("unknown webhook URL")

// Endpoints returns a host's endpoints, oldest first.
func (r *Registry) Endpoints(hostID int64) []Endpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Endpoint
	for _, e := range r.endpoints {
		if e.HostID == hostID {
			out = append(out, e)
		}
	}
	return out
}

// Endpoint returns an endpoint by ID (enabled or not).
func (r *Registry) Endpoint(id string) (Endpoint, bool) {
	id = strings.ToLower(id)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.endpoints {
		if e.ID == id {
			return e, true
		}
	}
	return Endpoint{}, false
}

// Route resolves a delivery on /webhooks/<platform>/<id>. It returns the
// endpoint's host, as Enabled returns it, with the webhook secrets and
// signing tokens replaced by the endpoint's own. The endpoint, its host and
// the platform must all match and be active.
func (r *Registry) Route(platform, id string) (Host, Endpoint, error) {
	e, ok := r.Endpoint(id)
	if !ok {
		return Host{}, Endpoint{}, ErrNoEndpoint
	}
	if !e.Enabled {
		return Host{}, e, fmt.Errorf("webhook %q is disabled", e.Name)
	}
	for _, h := range r.Enabled(platform) {
		if h.ID == e.HostID {
			h.WebhookSecrets, h.SigningTokens = e.WebhookSecrets, e.SigningTokens
			return h, e, nil
		}
	}
	if h, ok := r.Get(e.HostID); ok && h.Platform == platform {
		return Host{}, e, fmt.Errorf("host %s is disabled or not covered by the license", h.Host)
	}
	return Host{}, Endpoint{}, ErrNoEndpoint // another platform's endpoint: don't reveal it exists
}

// AddEndpoint creates an endpoint for a host with a fresh UUID and webhook
// secret. It returns the endpoint and the secret, which is shown once.
func (r *Registry) AddEndpoint(ctx context.Context, hostID int64, name, actor string) (Endpoint, string, error) {
	if _, ok := r.Get(hostID); !ok {
		return Endpoint{}, "", errors.New("host not found")
	}
	secret, err := GenerateSecret()
	if err != nil {
		return Endpoint{}, "", err
	}
	e := Endpoint{ID: uuid.NewString(), HostID: hostID, Name: name, Enabled: true,
		EndpointSecrets: EndpointSecrets{WebhookSecrets: []string{secret}}}
	saved, err := r.SaveEndpoint(ctx, e, actor)
	if err != nil {
		return Endpoint{}, "", err
	}
	return saved, secret, nil
}

// SaveEndpoint validates, encrypts and stores an endpoint, then reloads.
func (r *Registry) SaveEndpoint(ctx context.Context, e Endpoint, actor string) (Endpoint, error) {
	if r.Mode() != "database" || r.st == nil || r.box == nil {
		return Endpoint{}, errors.New("webhooks are managed in the admin panel: set " + secrets.EnvKey + " and a database")
	}
	h, ok := r.Get(e.HostID)
	if !ok {
		return Endpoint{}, errors.New("host not found")
	}
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		e.Name = "Webhook"
	}
	if len(e.Name) > 100 {
		return Endpoint{}, errors.New("name: at most 100 characters")
	}
	e.WebhookSecrets, e.SigningTokens = clean(e.WebhookSecrets), clean(e.SigningTokens)
	if len(e.WebhookSecrets) == 0 {
		return Endpoint{}, errors.New("a webhook needs its secret")
	}
	if len(e.SigningTokens) > 0 && h.Platform != GitLab {
		return Endpoint{}, errors.New("signing tokens are for GitLab webhooks")
	}
	if err := checkSigningTokens(e.SigningTokens); err != nil {
		return Endpoint{}, err
	}
	pt, err := json.Marshal(e.EndpointSecrets)
	if err != nil {
		return Endpoint{}, err
	}
	sealed, err := r.box.Seal(pt)
	if err != nil {
		return Endpoint{}, err
	}
	row, err := r.st.PutWebhookEndpoint(ctx, store.WebhookEndpoint{ID: e.ID, HostID: e.HostID, Name: e.Name, Enabled: e.Enabled, Secrets: sealed}, actor)
	if err != nil {
		return Endpoint{}, err
	}
	if err := r.Load(ctx); err != nil {
		return Endpoint{}, err
	}
	saved, _ := r.Endpoint(row.ID)
	return saved, nil
}

// DeleteEndpoint removes an endpoint and reloads. Its URL stops working.
func (r *Registry) DeleteEndpoint(ctx context.Context, id, actor string) error {
	if r.Mode() != "database" || r.st == nil {
		return errors.New("webhooks are managed in the admin panel")
	}
	if err := r.st.DeleteWebhookEndpoint(ctx, strings.ToLower(id), actor); err != nil {
		return err
	}
	return r.Load(ctx)
}

func (r *Registry) decodeEndpoint(row store.WebhookEndpoint) (Endpoint, error) {
	e := Endpoint{ID: strings.ToLower(row.ID), HostID: row.HostID, Name: row.Name, Enabled: row.Enabled,
		CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy}
	if row.Secrets != "" {
		pt, err := r.box.Open(row.Secrets)
		if err != nil {
			return e, err
		}
		if err := json.Unmarshal(pt, &e.EndpointSecrets); err != nil {
			return e, fmt.Errorf("secrets: %w", err)
		}
	}
	return e, nil
}
