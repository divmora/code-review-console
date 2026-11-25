// Package admin serves the admin panel: OIDC single sign-on, the
// repository allowlist with per-repo settings, and the audit log.
package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/divmora/code-review-console/internal/dashboards"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/clientip"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/execsettings"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/models"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/tuning"
)

const (
	sessionCookie = "crb_admin"
	flowCookie    = "crb_oidc"
)

// Admin is the admin panel.
type Admin struct {
	cfg      config.Admin
	store    store.Store
	logger   *slog.Logger
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	secret   []byte // signs the short-lived login-flow cookie
	secure   bool
	origin   string
	// Settings are the runtime settings shared with the server; nil means
	// config-file values only.
	Settings *settings.Service
	// Hosts is the code host registry shared with the server; nil hides
	// the Hosts page.
	Hosts *hosts.Registry
	// ExecutorType is the configured executor (local, docker, kubernetes);
	// docker and kubernetes need the executors license feature to run.
	ExecutorType string
	// Models are the model profiles shared with the server; nil means
	// those in the database only.
	Models *models.Service
	// Tuning holds the queue, review engine, bot name and dashboard token
	// settings (built over the store when nil).
	Tuning *tuning.Service
	// Box encrypts the requests operators' changes wait with (their bodies
	// can hold tokens). Without it, operators can't ask for those changes.
	Box *secrets.Box
	// mux is where approved changes are applied (set by Register).
	mux *http.ServeMux
	// Dashboards are the status dashboards (built over the store when nil).
	Dashboards *dashboards.Service
	// Executor holds the executor settings; nil hides them.
	Executor *execsettings.Service
	// Tags lists image versions for the Executor page (built on first use).
	Tags *execsettings.Tags
	// Lic is the installed license (nil = the free core). License keys
	// pasted in the panel are stored under licenseKey.
	Lic *license.Manager
	// Reviewer queues reviews requested from the panel (the server); nil
	// hides "Review now" and re-runs.
	Reviewer interface {
		ReviewNow(ctx context.Context, ref vcs.Ref, full bool, by string) error
	}
	// Labels reads a repository's labels for the label picker (the
	// server); nil leaves only typed patterns.
	Labels interface {
		RepoLabels(ctx context.Context, platform, host, repo string) ([]vcs.Label, error)
	}
}

func (a *Admin) models() *models.Service {
	if a.Models == nil {
		a.Models = models.New(nil, a.store)
	}
	return a.Models
}

func (a *Admin) settings() *settings.Service {
	if a.Settings == nil {
		a.Settings = settings.New(&config.Config{}, a.store)
	}
	return a.Settings
}

// New validates config and discovers the OIDC provider.
func New(ctx context.Context, cfg config.Admin, st store.Store, logger *slog.Logger) (*Admin, error) {
	o := cfg.OIDC
	switch {
	case st == nil:
		return nil, errors.New("admin: database.url is required")
	case cfg.BaseURL == "":
		return nil, errors.New("admin: base_url is required (the public URL of this server)")
	case o.Issuer == "" || o.ClientID == "":
		return nil, errors.New("admin: oidc.issuer and oidc.client_id are required")
	case len(o.AllowedEmails)+len(o.AllowedDomains)+len(o.AllowedGroups)+len(o.AllowedSubjects) == 0:
		return nil, errors.New("admin: set at least one of oidc.allowed_emails, allowed_domains, allowed_groups or allowed_subjects")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("admin: base_url %q is not a URL", cfg.BaseURL)
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(dctx, o.Issuer)
	if err != nil {
		return nil, fmt.Errorf("admin: OIDC discovery for %s: %w", o.Issuer, err)
	}
	secret := []byte(cfg.CookieSecret)
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	} else if len(secret) < 32 {
		return nil, errors.New("admin: cookie_secret must be at least 32 characters")
	}
	scopes := o.Scopes
	if !slices.Contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return &Admin{
		cfg: cfg, store: st, logger: logger,
		oauth: oauth2.Config{
			ClientID: o.ClientID, ClientSecret: o.ClientSecret, Endpoint: provider.Endpoint(),
			RedirectURL: cfg.BaseURL + "/admin/callback", Scopes: scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: o.ClientID}),
		secret:   secret,
		secure:   u.Scheme == "https",
		origin:   u.Scheme + "://" + u.Host,
	}, nil
}

// ---- login flow ----

type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
	// Next is the admin page to return to after sign-in.
	Next string `json:"x,omitempty"`
}

// safeNext keeps only known admin pages, so the return target can never
// send someone off-site.
func safeNext(next string) string {
	path, query, hasQuery := strings.Cut(next, "?")
	sec, ok := strings.CutPrefix(path, "/admin/")
	if !ok || !adminSections[sec] && sec != "dashboard" && sec != "signed-in" {
		return "/admin"
	}
	if !hasQuery {
		return path
	}
	// A page's filters (/admin/reviews?score_kind=security) come back too,
	// re-encoded so nothing but a query string follows the path.
	q, err := url.ParseQuery(query)
	if err != nil || len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *Admin) sign(payload []byte) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Admin) unsign(v string) ([]byte, bool) {
	p, s, ok := strings.Cut(v, ".")
	if !ok {
		return nil, false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p)
	sig, err2 := base64.RawURLEncoding.DecodeString(s)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	m := hmac.New(sha256.New, a.secret)
	m.Write(payload)
	return payload, hmac.Equal(m.Sum(nil), sig)
}

func (a *Admin) handleLogin(w http.ResponseWriter, r *http.Request) {
	fs := flowState{State: randToken(24), Nonce: randToken(24), Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(10 * time.Minute).Unix(),
		Next: safeNext(r.URL.Query().Get("next"))}
	js, _ := json.Marshal(fs)
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: a.sign(js), Path: "/admin/callback", MaxAge: 600,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	target := a.oauth.AuthCodeURL(fs.State, oidc.Nonce(fs.Nonce), oauth2.S256ChallengeOption(fs.Verifier))
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *Admin) handleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string, args ...any) {
		a.logger.Warn("admin login failed", append([]any{"reason", msg}, args...)...)
		http.Error(w, "Sign-in failed: "+msg, status)
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(http.StatusUnauthorized, "identity provider returned "+e)
		return
	}
	c, err := r.Cookie(flowCookie)
	if err != nil {
		fail(http.StatusBadRequest, "login session expired, start again at /admin/login")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: "", Path: "/admin/callback", MaxAge: -1, HttpOnly: true, Secure: a.secure})
	raw, ok := a.unsign(c.Value)
	var fs flowState
	if !ok || json.Unmarshal(raw, &fs) != nil || time.Now().Unix() > fs.Expires {
		fail(http.StatusBadRequest, "login session invalid or expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(fs.State)) != 1 {
		fail(http.StatusBadRequest, "state mismatch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(fs.Verifier))
	if err != nil {
		fail(http.StatusUnauthorized, "code exchange failed", "error", err)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		fail(http.StatusUnauthorized, "no id_token in token response")
		return
	}
	idt, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		fail(http.StatusUnauthorized, "id_token verification failed", "error", err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(fs.Nonce)) != 1 {
		fail(http.StatusUnauthorized, "nonce mismatch")
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		fail(http.StatusUnauthorized, "bad claims")
		return
	}
	id := identityFrom(claims, a.cfg.OIDC)
	if id.Subject == "" {
		id.Subject = idt.Subject
	}
	if reason := a.authorize(id); reason != "" {
		_ = a.store.Audit(ctx, firstNonEmpty(id.Email, id.Subject), "login.denied", "admin", map[string]any{"reason": reason})
		fail(http.StatusForbidden, "your account is not allowed to administer this server", "email", id.Email, "why", reason)
		return
	}
	sid := randToken(32)
	sum := sha256.Sum256([]byte(sid))
	client := clientip.From(r.Context())
	err = a.store.CreateSession(ctx, sum[:], store.Session{
		Subject: id.Subject, Email: id.Email, Name: id.Name, Groups: id.Groups, Verified: id.Verified,
		CSRF: randToken(24), Expires: time.Now().Add(a.cfg.SessionTTL), IP: client.IP, UserAgent: client.UserAgent,
	})
	if err != nil {
		fail(http.StatusInternalServerError, "could not create session", "error", err)
		return
	}
	_ = a.store.Audit(ctx, id.Email, "login", "admin", map[string]any{"subject": id.Subject, "name": id.Name})
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sid, Path: "/admin", MaxAge: int(a.cfg.SessionTTL.Seconds()),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	a.logger.Info("admin signed in", "email", id.Email, "ip", client.IP)
	http.Redirect(w, r, safeNext(fs.Next), http.StatusFound)
}

type identity struct {
	// Subject is the subject_claim's value (default sub): what the
	// *_subjects rules match.
	Subject string
	Email   string
	Name    string
	Groups  []string
	// Verified: the provider said email_verified: true, or the claim is
	// missing and require_email_verified is off. Email and domain rules
	// match only verified addresses.
	Verified bool
	// NoVerifiedClaim: the ID token had no email_verified claim (only known
	// at sign-in, for the refusal message).
	NoVerifiedClaim bool
}

func identityFrom(claims map[string]any, o config.OIDC) identity {
	var id identity
	id.Email, _ = claims["email"].(string)
	id.Name, _ = claims["name"].(string)
	if claim := firstNonEmpty(o.SubjectClaim, "sub"); claim != "" {
		id.Subject, _ = claims[claim].(string)
	}
	switch v := claims["email_verified"].(type) {
	case bool:
		id.Verified = v
	case string: // some providers send "true"
		id.Verified = strings.EqualFold(v, "true")
	default:
		id.NoVerifiedClaim = true
		id.Verified = !o.EmailVerifiedRequired()
	}
	switch g := claims[o.GroupsClaim].(type) {
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				id.Groups = append(id.Groups, s)
			}
		}
	case string:
		id.Groups = strings.Fields(strings.ReplaceAll(g, ",", " "))
	}
	return id
}

// authorize returns why an identity is refused, or "".
func (a *Admin) authorize(id identity) string {
	_, reason := a.roleOf(id)
	return reason
}

// roleOf works out the role an identity signs in with: admin when it
// matches allowed_emails, allowed_domains or allowed_groups; otherwise
// operator, then viewer, from their own lists. Domain and group rules,
// and the operator and viewer roles, need the sso_rules license feature
// (without it, allowed_emails are admins and nobody else signs in).
func (a *Admin) roleOf(id identity) (string, string) {
	o := a.cfg.OIDC
	email := strings.ToLower(strings.TrimSpace(id.Email))
	paid := a.Lic.Has(license.SSORules)
	hasEmail := func(list []string) bool {
		if email == "" || !id.Verified {
			return false
		}
		for _, e := range list {
			if strings.EqualFold(strings.TrimSpace(e), email) {
				return true
			}
		}
		return false
	}
	hasSubject := func(list []string) bool {
		for _, s := range list {
			if id.Subject != "" && strings.TrimSpace(s) == id.Subject {
				return true
			}
		}
		return false
	}
	inGroup := func(list []string) bool {
		for _, g := range id.Groups {
			for _, want := range list {
				if g == want {
					return true
				}
			}
		}
		return false
	}
	if hasEmail(o.AllowedEmails) || hasSubject(o.AllowedSubjects) {
		return RoleAdmin, ""
	}
	if paid {
		if _, domain, ok := strings.Cut(email, "@"); ok && email != "" && id.Verified {
			for _, d := range o.AllowedDomains {
				if strings.EqualFold(strings.TrimPrefix(d, "@"), domain) {
					return RoleAdmin, ""
				}
			}
		}
		switch {
		case inGroup(o.AllowedGroups):
			return RoleAdmin, ""
		case hasEmail(o.OperatorEmails) || inGroup(o.OperatorGroups) || hasSubject(o.OperatorSubjects):
			return RoleOperator, ""
		case hasEmail(o.ViewerEmails) || inGroup(o.ViewerGroups) || hasSubject(o.ViewerSubjects):
			return RoleViewer, ""
		}
	}
	return "", a.denyReason(id, email)
}

func (a *Admin) denyReason(id identity, email string) string {
	o := a.cfg.OIDC
	if email != "" && !id.Verified && id.NoVerifiedClaim {
		return "the identity provider sent no email_verified claim, so email and domain rules don't apply: use allowed_subjects or group rules, or set admin.oidc.require_email_verified: false if this provider verifies every email it sends"
	}
	if email != "" && !id.Verified {
		return "email not verified by the identity provider"
	}
	if !a.Lic.Has(license.SSORules) && len(o.AllowedDomains)+len(o.AllowedGroups)+len(o.OperatorEmails)+len(o.OperatorGroups)+len(o.ViewerEmails)+len(o.ViewerGroups)+len(o.OperatorSubjects)+len(o.ViewerSubjects) > 0 {
		return "not in allowed_emails (domain and group rules, operators and viewers need a Divmora license with the sso_rules feature)"
	}
	if len(o.OperatorEmails)+len(o.OperatorGroups)+len(o.ViewerEmails)+len(o.ViewerGroups)+len(o.OperatorSubjects)+len(o.ViewerSubjects) > 0 {
		return "not in allowed_emails, allowed_domains or allowed_groups, nor an operator or viewer"
	}
	return "not in allowed_emails, allowed_domains or allowed_groups"
}

func (a *Admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s, ok := a.session(r); ok && a.checkCSRF(r, s) {
		c, _ := r.Cookie(sessionCookie)
		sum := sha256.Sum256([]byte(c.Value))
		_ = a.store.DeleteSession(r.Context(), sum[:])
		_ = a.store.Audit(r.Context(), s.Email, "logout", "admin", map[string]any{})
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, Secure: a.secure})
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

// ---- sessions ----

type ctxKey struct{}

// session returns the request's session. Every request checks it against
// the current sign-in rules (and license): a session that no longer
// matches, for example after an admin is removed from allowed_emails and
// the console restarted, or the license with sso_rules ended, is ended at
// once instead of lasting until session_ttl.
func (a *Admin) session(r *http.Request) (*store.Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	sum := sha256.Sum256([]byte(c.Value))
	s, err := a.store.GetSession(r.Context(), sum[:])
	if err != nil {
		return nil, false
	}
	id := identity{Subject: s.Subject, Email: s.Email, Name: s.Name, Groups: s.Groups, Verified: s.Verified}
	role, reason := a.roleOf(id)
	s.Role = role
	if reason != "" {
		_ = a.store.DeleteSession(r.Context(), sum[:])
		_ = a.store.Audit(r.Context(), firstNonEmpty(s.Email, s.Subject), "session.ended", "admin",
			map[string]any{"reason": "no longer allowed by the sign-in rules: " + reason})
		a.logger.Warn("admin session ended: no longer allowed by the sign-in rules", "email", s.Email, "why", reason)
		return nil, false
	}
	// Signed out after a while without activity (issue #87): the last
	// request the person made, not the panel's background refreshes.
	if idle := a.settings().Get(r.Context()).SessionIdleMinutes; idle > 0 && !s.LastSeen.IsZero() {
		if quiet := time.Since(s.LastSeen); quiet > time.Duration(idle)*time.Minute {
			_ = a.store.DeleteSession(r.Context(), sum[:])
			_ = a.store.Audit(r.Context(), firstNonEmpty(s.Email, s.Subject), "session.ended", "admin",
				map[string]any{"reason": "idle", "idle_minutes": idle, "last_seen": s.LastSeen})
			a.logger.Info("admin session ended after inactivity", "email", s.Email, "idle", quiet.Round(time.Minute).String())
			return nil, false
		}
	}
	if time.Since(s.LastSeen) > time.Minute && !isBackground(r) {
		_ = a.store.TouchSession(r.Context(), sum[:], clientip.From(r.Context()).IP)
	}
	return s, true
}

// SignedIn reports whether the request comes from a signed-in admin (for
// pages the server serves under /admin, such as the status dashboard).
func (a *Admin) SignedIn(r *http.Request) bool {
	_, ok := a.session(r)
	return ok
}

// Role is the signed-in user's role ("admin", "operator" or "viewer"), or
// "" without a session. The session cookie is scoped to /admin.
func (a *Admin) Role(r *http.Request) string {
	if s, ok := a.session(r); ok {
		return s.Role
	}
	return ""
}

// isBackground reports a request the panel makes on its own (a periodic
// refresh), which doesn't count as the person being active.
func isBackground(r *http.Request) bool { return r.Header.Get("X-Background") == "1" }

// sessionHash is the stored hash of the request's session cookie.
func sessionHash(r *http.Request) []byte {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256([]byte(c.Value))
	return sum[:]
}

// checkCSRF requires the session's CSRF token in X-CSRF-Token and, when the
// browser sends one, a same-origin Origin header.
func (a *Admin) checkCSRF(r *http.Request, s *store.Session) bool {
	if o := r.Header.Get("Origin"); o != "" && o != a.origin {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) == 1
}

// requireAPI wraps admin API handlers: a valid session, and CSRF for writes.
func (a *Admin) requireAPI(next func(http.ResponseWriter, *http.Request, *store.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		// An approved change runs as its requester, approved by an admin.
		if s, ok := replaySession(r); ok {
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)), s)
			return
		}
		s, ok := a.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in at /admin/login"})
			return
		}
		if r.Method != http.MethodGet && !a.checkCSRF(r, s) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid CSRF token"})
			return
		}
		switch ok, queue := allowed(s.Role, classOf(r)); {
		case queue:
			a.queueChange(w, r, s)
			return
		case !ok && s.Role == RoleViewer:
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "you have read-only access: ask an admin to make this change"})
			return
		case !ok:
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an admin can do this"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)), s)
	}
}

func noStore(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
