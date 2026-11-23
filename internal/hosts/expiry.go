package hosts

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TokenExpiry is what the console knows about when a host's token stops
// working. It is looked up when a host is saved or tested, at start and
// once a day, and kept in memory (each replica checks for itself).
type TokenExpiry struct {
	// Expires is when the token stops working; zero with Known means it
	// never expires.
	Expires time.Time `json:"expires,omitzero"`
	// Known is false when the code host doesn't say (a GitHub App, a
	// Bitbucket token, or an older GitLab).
	Known   bool      `json:"known"`
	Checked time.Time `json:"checked,omitzero"`
	// Error is why the last lookup failed ("" = it worked).
	Error string `json:"error,omitempty"`
}

// expiryClient bounds lookups; tests replace it.
var expiryClient = &http.Client{Timeout: 10 * time.Second}

// LookupTokenExpiry asks the code host when h's token expires:
//   - GitLab: GET /api/v4/personal_access_tokens/self (personal, group
//     and project access tokens), field expires_at.
//   - GitHub (a token, not an App): the github-authentication-token-
//     expiration header GitHub sends with responses to expiring tokens.
//
// A GitHub App mints short-lived tokens itself, and Bitbucket doesn't
// report expiry, so those are not Known.
func LookupTokenExpiry(ctx context.Context, h Host) TokenExpiry {
	out := TokenExpiry{Checked: time.Now()}
	switch {
	case h.Platform == GitLab && h.Token != "":
		exp, known, err := gitlabExpiry(ctx, h)
		out.Expires, out.Known = exp, known
		if err != nil {
			out.Error = err.Error()
		}
	case h.Platform == GitHub && h.Token != "" && h.AppID == 0:
		exp, err := githubExpiry(ctx, h)
		out.Expires, out.Known = exp, err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}
	return out
}

func gitlabExpiry(ctx context.Context, h Host) (time.Time, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.BaseURL, "/")+"/api/v4/personal_access_tokens/self", nil)
	if err != nil {
		return time.Time{}, false, err
	}
	req.Header.Set("PRIVATE-TOKEN", h.Token)
	c := expiryClient
	if h.SkipTLSVerify {
		c = &http.Client{Timeout: expiryClient.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // the host's own setting
	}
	resp, err := c.Do(req)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("token expiry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return time.Time{}, false, nil // older GitLab: not reported
	case http.StatusUnauthorized:
		return time.Time{}, false, fmt.Errorf("GitLab rejected the token (expired or revoked)")
	default:
		return time.Time{}, false, fmt.Errorf("token expiry: GitLab answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return time.Time{}, false, fmt.Errorf("token expiry: %w", err)
	}
	if body.ExpiresAt == nil || *body.ExpiresAt == "" {
		return time.Time{}, true, nil // never expires
	}
	// A date: the token works through that day (UTC).
	d, err := time.Parse("2006-01-02", *body.ExpiresAt)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("token expiry: %q: %w", *body.ExpiresAt, err)
	}
	return d.Add(24 * time.Hour), true, nil
}

func githubExpiry(ctx context.Context, h Host) (time.Time, error) {
	base := strings.TrimRight(h.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := expiryClient.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("token expiry: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return time.Time{}, fmt.Errorf("GitHub rejected the token (expired or revoked)")
	}
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("token expiry: GitHub answered HTTP %d", resp.StatusCode)
	}
	v := resp.Header.Get("Github-Authentication-Token-Expiration")
	if v == "" {
		return time.Time{}, nil // a token without an expiry
	}
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("token expiry: unreadable %q", v)
}

// CheckTokenExpiry looks up h's token expiry and remembers it.
func (r *Registry) CheckTokenExpiry(ctx context.Context, h Host) TokenExpiry {
	e := LookupTokenExpiry(ctx, h)
	r.mu.Lock()
	if r.expiry == nil {
		r.expiry = map[int64]TokenExpiry{}
	}
	r.expiry[h.ID] = e
	r.mu.Unlock()
	return e
}

// RefreshTokenExpiry checks every enabled host.
func (r *Registry) RefreshTokenExpiry(ctx context.Context) {
	for _, h := range r.Enabled("") {
		r.CheckTokenExpiry(ctx, h)
	}
}

// TokenExpiryOf returns what is known about host id's token.
func (r *Registry) TokenExpiryOf(id int64) (TokenExpiry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.expiry[id]
	return e, ok
}

// Check is the result of the last Test of a host's credentials.
type Check struct {
	OK bool `json:"ok"`
	// Summary is a few words for the table ("Token rejected (401)");
	// Message the full result or error.
	Summary string    `json:"summary"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// RecordCheck remembers a Test result for host id.
func (r *Registry) RecordCheck(id int64, c Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checks == nil {
		r.checks = map[int64]Check{}
	}
	r.checks[id] = c
}

// CheckOf returns the last Test result for host id.
func (r *Registry) CheckOf(id int64) (Check, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.checks[id]
	return c, ok
}

// Summarize turns a failed check's error into a few words an admin can act
// on; the full error stays in the message.
func Summarize(err error) string {
	m := strings.ToLower(err.Error())
	switch {
	case strings.Contains(m, "no such host"):
		return "Can't find the host (DNS)"
	case strings.Contains(m, "connection refused"), strings.Contains(m, "no route to host"), strings.Contains(m, "network is unreachable"):
		return "Can't reach the host"
	case strings.Contains(m, "timeout"), strings.Contains(m, "deadline exceeded"):
		return "The host didn't answer in time"
	case strings.Contains(m, "x509"), strings.Contains(m, "certificate"), strings.Contains(m, "tls"):
		return "TLS certificate problem"
	case strings.Contains(m, "401"), strings.Contains(m, "unauthorized"), strings.Contains(m, "bad credentials"):
		return "Token rejected (401)"
	case strings.Contains(m, "403"), strings.Contains(m, "forbidden"):
		return "Token lacks access (403)"
	case strings.Contains(m, "404"), strings.Contains(m, "not found"):
		return "Not found (404): check the URL"
	}
	return "Check failed"
}
