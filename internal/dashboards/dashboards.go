// Package dashboards manages the status dashboards (issue #155): each has
// its own title, access, token and repositories, so a team gets a view of
// its own repositories only. The "default" dashboard is today's single
// dashboard (/dashboard), created from the old settings on upgrade.
package dashboards

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/tuning"
)

// DefaultSlug is the dashboard at /dashboard and /api/status.
const DefaultSlug = "default"

// Who can open a dashboard.
const (
	// Admins: signed-in admins only.
	Admins = "admins"
	// SignedIn: anyone signed in to the admin panel (viewer, operator, admin).
	SignedIn = "signed_in"
	// Token: signed-in admins, and anyone with this dashboard's token.
	Token = "token"
	// Public: anyone who can reach the console.
	Public = "public"
)

// Dashboard is a status dashboard.
type Dashboard = store.Dashboard

// Viewer is who asks to open a dashboard: their admin panel role ("" when
// not signed in, or on a page outside /admin where the session cookie
// doesn't apply) and the token they sent.
type Viewer struct {
	Role  string
	Token string
}

// Opens reports whether v may open d.
func Opens(d Dashboard, v Viewer) bool {
	if !d.Enabled {
		return false
	}
	switch d.Access {
	case Public:
		return true
	case Token:
		return v.Role == "admin" || TokenOK(d, v.Token, time.Now())
	case SignedIn:
		return v.Role != ""
	}
	return v.Role == "admin"
}

// NeedsSignIn reports whether d is opened by signing in (at /admin/...).
func NeedsSignIn(d Dashboard) bool { return d.Access == Admins || d.Access == SignedIn }

// TokenOK reports whether token is d's token and has not expired.
func TokenOK(d Dashboard, token string, now time.Time) bool {
	return tokenMatches(d, token) && !expired(d, now)
}

// TokenExpiredFor reports whether token is d's token but has expired, so
// the page can say so instead of "token required".
func TokenExpiredFor(d Dashboard, token string, now time.Time) bool {
	return tokenMatches(d, token) && expired(d, now)
}

func expired(d Dashboard, now time.Time) bool {
	return !d.TokenExpires.IsZero() && !now.Before(d.TokenExpires)
}

func tokenMatches(d Dashboard, token string) bool {
	if d.TokenHash == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(d.TokenHash)) == 1
}

// HashToken is how dashboard tokens are stored (SHA-256, hex), the same as
// the old single dashboard token, so it carries over on upgrade.
func HashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// NewToken returns a random token (256 bits, URL-safe).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Scope is the set of repositories a dashboard shows.
type Scope struct {
	all   bool
	repos map[string]bool
	rules []store.Rule
	hosts map[string]bool
}

// ScopeOf builds d's scope.
func ScopeOf(d Dashboard) Scope {
	s := Scope{all: d.AllRepos, repos: map[string]bool{}, hosts: map[string]bool{}}
	for _, r := range d.Repos {
		p, h, path := store.RepoKey(r.Platform, r.Host, r.Path)
		s.repos[p+"|"+h+"|"+path] = true
		s.hosts[p+"|"+h] = true
	}
	for _, pt := range d.Patterns {
		s.rules = append(s.rules, store.Rule{Platform: pt.Platform, Host: pt.Host, Pattern: pt.Pattern})
		s.hosts[strings.ToLower(pt.Platform)+"|"+strings.ToLower(pt.Host)] = true
	}
	return s
}

// All reports whether the scope is every repository.
func (s Scope) All() bool { return s.all }

// Covers reports whether the scope includes the repository.
func (s Scope) Covers(platform, host, repo string) bool {
	if s.all {
		return true
	}
	p, h, path := store.RepoKey(platform, host, repo)
	if s.repos[p+"|"+h+"|"+path] {
		return true
	}
	for _, r := range s.rules {
		if r.Matches(platform, host, repo) {
			return true
		}
	}
	return false
}

// HasHost reports whether any of the scope's repositories can be on host.
func (s Scope) HasHost(platform, host string) bool {
	return s.all || s.hosts[strings.ToLower(platform)+"|"+strings.ToLower(host)]
}

// Legacy is the single dashboard's settings before dashboards existed, for
// the default dashboard (on upgrade, and without a database).
type Legacy struct {
	Enabled      bool
	Title        string
	HideRepos    bool
	Access       string // the old admins, token or public
	TokenHash    string
	TokenExpires time.Time
}

// FromLegacy is the default dashboard as the old settings describe it. The
// old "admins" let in anyone signed in, so it becomes SignedIn: nobody who
// could open the dashboard before loses it.
func FromLegacy(l Legacy) Dashboard {
	access := l.Access
	switch access {
	case Token, Public:
	default:
		access = SignedIn
	}
	return Dashboard{Slug: DefaultSlug, Title: firstNonEmpty(l.Title, "Code review"), Access: access, HideRepos: l.HideRepos, AllRepos: true,
		Enabled: l.Enabled, TokenHash: l.TokenHash, TokenExpires: l.TokenExpires, Repos: []store.DashboardRepo{}, Patterns: []store.DashboardPattern{}}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Service reads and writes dashboards. Without a store (no database) the
// only dashboard is the default one, from Legacy.
type Service struct {
	st store.Store
	// Legacy returns the old settings (for the default dashboard).
	Legacy func(ctx context.Context) Legacy
}

// New returns a Service over st (nil: no database).
func New(st store.Store, legacy func(ctx context.Context) Legacy) *Service {
	return &Service{st: st, Legacy: legacy}
}

func (s *Service) legacy(ctx context.Context) Dashboard {
	if s.Legacy == nil {
		return FromLegacy(Legacy{Enabled: true})
	}
	return FromLegacy(s.Legacy(ctx))
}

// List returns every dashboard, the default one first.
func (s *Service) List(ctx context.Context) ([]Dashboard, error) {
	if s.st == nil {
		return []Dashboard{s.legacy(ctx)}, nil
	}
	list, err := s.st.ListDashboards(ctx)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 { // before EnsureDefault ran
		return []Dashboard{s.legacy(ctx)}, nil
	}
	return list, nil
}

// BySlug returns a dashboard by its slug.
func (s *Service) BySlug(ctx context.Context, slug string) (Dashboard, bool, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Dashboard{}, false, err
	}
	for _, d := range list {
		if d.Slug == slug {
			return d, true, nil
		}
	}
	return Dashboard{}, false, nil
}

// ByID returns a stored dashboard by ID.
func (s *Service) ByID(ctx context.Context, id int64) (Dashboard, bool, error) {
	if s.st == nil {
		return Dashboard{}, false, nil
	}
	list, err := s.st.ListDashboards(ctx)
	if err != nil {
		return Dashboard{}, false, err
	}
	for _, d := range list {
		if d.ID == id {
			return d, true, nil
		}
	}
	return Dashboard{}, false, nil
}

// EnsureDefault stores the default dashboard from the old settings when
// there is none (the upgrade to dashboards). It reports whether it did.
func (s *Service) EnsureDefault(ctx context.Context) (bool, error) {
	if s.st == nil {
		return false, nil
	}
	list, err := s.st.ListDashboards(ctx)
	if err != nil {
		return false, err
	}
	for _, d := range list {
		if d.Slug == DefaultSlug {
			return false, nil
		}
	}
	if _, err := s.st.PutDashboard(ctx, s.legacy(ctx), "upgrade: dashboards"); err != nil && !errors.Is(err, store.ErrDuplicate) {
		return false, err
	}
	return true, nil
}

// SyncLegacy applies the old dashboard settings to the default dashboard,
// when an admin changes them where they used to live (Settings). Only the
// fields that changed are applied, so a token rotated on the default
// dashboard isn't overwritten by an unrelated save.
func (s *Service) SyncLegacy(ctx context.Context, before, after Legacy, actor string) error {
	if s.st == nil || before == after {
		return nil
	}
	d, ok, err := s.BySlug(ctx, DefaultSlug)
	if err != nil || !ok || d.ID == 0 {
		return err
	}
	if before.Enabled != after.Enabled {
		d.Enabled = after.Enabled
	}
	if before.Title != after.Title {
		d.Title = firstNonEmpty(after.Title, "Code review")
	}
	if before.HideRepos != after.HideRepos {
		d.HideRepos = after.HideRepos
	}
	if before.Access != after.Access {
		d.Access = FromLegacy(after).Access
	}
	if before.TokenHash != after.TokenHash || !before.TokenExpires.Equal(after.TokenExpires) {
		d.TokenChange = map[bool]string{true: "removed", false: "rotated"}[after.TokenHash == ""]
		d.TokenHash, d.TokenExpires = after.TokenHash, after.TokenExpires
	}
	_, err = s.st.PutDashboard(ctx, d, actor)
	return err
}

var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// Normalize cleans and checks a dashboard before it is saved.
func Normalize(d *Dashboard) error {
	d.Slug = strings.ToLower(strings.TrimSpace(d.Slug))
	d.Title, d.Description = strings.TrimSpace(d.Title), strings.TrimSpace(d.Description)
	if !slugRe.MatchString(d.Slug) {
		return errors.New("slug: lower-case letters, digits and dashes, up to 64 (it goes in the link: /dashboards/<slug>)")
	}
	if d.Title == "" {
		return errors.New("title: required")
	}
	if len(d.Title) > 100 || len(d.Description) > 500 {
		return errors.New("title: at most 100 characters; description: at most 500")
	}
	switch d.Access {
	case Admins, SignedIn, Token, Public:
	default:
		return fmt.Errorf("access: one of %s, %s, %s or %s", Admins, SignedIn, Token, Public)
	}
	if d.Access != Token {
		d.TokenHash, d.TokenExpires = "", time.Time{}
	}
	seen := map[int64]bool{}
	repos := []store.DashboardRepo{}
	for _, r := range d.Repos {
		if r.ID > 0 && !seen[r.ID] {
			seen[r.ID] = true
			repos = append(repos, store.DashboardRepo{ID: r.ID})
		}
	}
	d.Repos = repos
	pats := []store.DashboardPattern{}
	have := map[string]bool{}
	for i, p := range d.Patterns {
		norm, err := store.NormalizePattern(p.Pattern)
		if err != nil {
			return fmt.Errorf("patterns[%d]: %w", i, err)
		}
		if norm == "" {
			return fmt.Errorf("patterns[%d]: set a group or pattern (for every repository, choose all repositories)", i)
		}
		platform, host := strings.ToLower(strings.TrimSpace(p.Platform)), strings.ToLower(strings.TrimSpace(p.Host))
		if platform == "" || host == "" {
			return fmt.Errorf("patterns[%d]: platform and host are required", i)
		}
		k := platform + "|" + host + "|" + norm
		if !have[k] {
			have[k] = true
			pats = append(pats, store.DashboardPattern{Platform: platform, Host: host, Pattern: norm})
		}
	}
	d.Patterns = pats
	if d.AllRepos {
		d.Repos, d.Patterns = []store.DashboardRepo{}, []store.DashboardPattern{}
	}
	return nil
}

// Save validates and stores d (ID 0 creates it). Saving a token dashboard
// without a token hash keeps its current token.
func (s *Service) Save(ctx context.Context, d Dashboard, actor string) (Dashboard, error) {
	if s.st == nil {
		return Dashboard{}, errors.New("dashboards need a database")
	}
	if err := Normalize(&d); err != nil {
		return Dashboard{}, err
	}
	if d.ID != 0 {
		old, ok, err := s.ByID(ctx, d.ID)
		if err != nil {
			return Dashboard{}, err
		}
		if !ok {
			return Dashboard{}, store.ErrNotFound
		}
		if old.Slug == DefaultSlug && d.Slug != DefaultSlug {
			return Dashboard{}, errors.New("the default dashboard keeps its slug (it serves /dashboard)")
		}
		if d.Access == Token && old.Access == Token && d.TokenHash == "" {
			d.TokenHash, d.TokenExpires = old.TokenHash, old.TokenExpires // keep the token
		}
	}
	saved, err := s.st.PutDashboard(ctx, d, actor)
	if errors.Is(err, store.ErrDuplicate) {
		return Dashboard{}, fmt.Errorf("a dashboard with the slug %q exists already", d.Slug)
	}
	if err != nil {
		return Dashboard{}, err
	}
	return *saved, nil
}

// Delete removes a dashboard; its repositories stay. The default one can
// be turned off but not deleted.
func (s *Service) Delete(ctx context.Context, id int64, actor string) error {
	d, ok, err := s.ByID(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	if d.Slug == DefaultSlug {
		return errors.New("the default dashboard serves /dashboard: turn it off instead of deleting it")
	}
	return s.st.DeleteDashboard(ctx, id, actor)
}

// RotateToken gives a token dashboard a new token valid for ttl (0: never
// expires) and returns it; it is shown once.
func (s *Service) RotateToken(ctx context.Context, id int64, ttl time.Duration, actor string) (string, Dashboard, error) {
	d, ok, err := s.ByID(ctx, id)
	if err != nil {
		return "", Dashboard{}, err
	}
	if !ok {
		return "", Dashboard{}, store.ErrNotFound
	}
	if d.Access != Token {
		return "", Dashboard{}, errors.New("only a dashboard opened with a token has one: set its access to token first")
	}
	tok, err := NewToken()
	if err != nil {
		return "", Dashboard{}, err
	}
	d.TokenChange = map[bool]string{true: "set", false: "rotated"}[d.TokenHash == ""]
	d.TokenHash, d.TokenExpires = HashToken(tok), time.Time{}
	if ttl > 0 {
		d.TokenExpires = time.Now().Add(ttl).UTC().Truncate(time.Second)
	}
	saved, err := s.st.PutDashboard(ctx, d, actor)
	if err != nil {
		return "", Dashboard{}, err
	}
	return tok, *saved, nil
}

// LegacyFrom reads the old dashboard settings from where they lived:
// Settings (on/off, title, hide names) and the queue settings (access,
// token).
func LegacyFrom(eff settings.Effective, t tuning.Settings) Legacy {
	return Legacy{Enabled: eff.DashboardEnabled, Title: eff.DashboardTitle, HideRepos: eff.HideRepos, Access: t.Access(),
		TokenHash: t.DashboardTokenHash, TokenExpires: t.TokenExpiry()}
}
