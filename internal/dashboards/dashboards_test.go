package dashboards

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func TestOpens(t *testing.T) {
	now := time.Now()
	tok := "s3cret-token"
	d := Dashboard{Enabled: true, TokenHash: HashToken(tok)}
	cases := []struct {
		access string
		v      Viewer
		want   bool
	}{
		{Admins, Viewer{Role: "admin"}, true},
		{Admins, Viewer{Role: "viewer"}, false},
		{Admins, Viewer{Token: tok}, false},
		{SignedIn, Viewer{Role: "viewer"}, true},
		{SignedIn, Viewer{Role: "operator"}, true},
		{SignedIn, Viewer{}, false},
		{Token, Viewer{Token: tok}, true},
		{Token, Viewer{Token: "other"}, false},
		{Token, Viewer{Role: "admin"}, true},
		{Token, Viewer{Role: "viewer"}, false},
		{Public, Viewer{}, true},
	}
	for _, c := range cases {
		d.Access = c.access
		if got := Opens(d, c.v); got != c.want {
			t.Errorf("%s %+v: %v, want %v", c.access, c.v, got, c.want)
		}
	}
	d.Access, d.TokenExpires = Token, now.Add(-time.Minute)
	if Opens(d, Viewer{Token: tok}) || !TokenExpiredFor(d, tok, now) {
		t.Error("an expired token doesn't open the dashboard, and says it expired")
	}
	d.Access, d.Enabled = Public, false
	if Opens(d, Viewer{Role: "admin"}) {
		t.Error("a dashboard that is off opens for nobody")
	}
}

func TestScope(t *testing.T) {
	d := Dashboard{
		Repos:    []store.DashboardRepo{{ID: 1, Platform: "gitlab", Host: "gitlab.example.com", Path: "ifmists/common-lib"}},
		Patterns: []store.DashboardPattern{{Platform: "gitlab", Host: "gitlab.example.com", Pattern: "ifmists/hrms"}},
	}
	s := ScopeOf(d)
	for repo, want := range map[string]bool{
		"ifmists/common-lib":  true,
		"IFMISTS/Common-Lib":  true,
		"ifmists/hrms/api":    true, // a new repository in the group
		"ifmists/hrms-legacy": false,
		"ifmists/fms":         false,
	} {
		if got := s.Covers("gitlab", "gitlab.example.com", repo); got != want {
			t.Errorf("%s: %v, want %v", repo, got, want)
		}
	}
	if s.Covers("gitlab", "other.example.com", "ifmists/hrms/api") {
		t.Error("a pattern is for its host only")
	}
	if !s.HasHost("gitlab", "gitlab.example.com") || s.HasHost("github", "github.com") || s.All() {
		t.Error("hosts in scope")
	}
	if all := ScopeOf(Dashboard{AllRepos: true}); !all.Covers("github", "github.com", "any/thing") || !all.HasHost("x", "y") {
		t.Error("all repositories")
	}
}

func TestFromLegacy(t *testing.T) {
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	d := FromLegacy(Legacy{Enabled: true, Title: "Bots", HideRepos: true, Access: Token, TokenHash: "h", TokenExpires: exp})
	if d.Slug != DefaultSlug || !d.AllRepos || d.Access != Token || d.TokenHash != "h" || !d.TokenExpires.Equal(exp) || !d.HideRepos || d.Title != "Bots" {
		t.Errorf("%+v", d)
	}
	// The old "admins" let in anyone signed in: nobody loses the dashboard.
	if d := FromLegacy(Legacy{Access: Admins}); d.Access != SignedIn || d.Title != "Code review" {
		t.Errorf("admins: %+v", d)
	}
}

func TestNormalize(t *testing.T) {
	for name, c := range map[string]struct {
		d    Dashboard
		want string
	}{
		"bad slug":   {Dashboard{Slug: "HRMS team!", Title: "x", Access: Admins}, "slug"},
		"no title":   {Dashboard{Slug: "hrms", Access: Admins}, "title"},
		"bad access": {Dashboard{Slug: "hrms", Title: "x", Access: "everyone"}, "access"},
		"lone star":  {Dashboard{Slug: "hrms", Title: "x", Access: Admins, Patterns: []store.DashboardPattern{{Platform: "gitlab", Host: "g", Pattern: "*"}}}, "pattern"},
		"no host":    {Dashboard{Slug: "hrms", Title: "x", Access: Admins, Patterns: []store.DashboardPattern{{Platform: "gitlab", Pattern: "a"}}}, "host"},
	} {
		if err := Normalize(&c.d); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
	d := Dashboard{Slug: " HRMS ", Title: " HRMS ", Access: Public, TokenHash: "h",
		Repos:    []store.DashboardRepo{{ID: 2}, {ID: 2}, {ID: 0}},
		Patterns: []store.DashboardPattern{{Platform: "GitLab", Host: "G.example", Pattern: "/A/B/"}, {Platform: "gitlab", Host: "g.example", Pattern: "a/b"}}}
	if err := Normalize(&d); err != nil {
		t.Fatal(err)
	}
	if d.Slug != "hrms" || d.Title != "HRMS" || d.TokenHash != "" || len(d.Repos) != 1 || len(d.Patterns) != 1 || d.Patterns[0] != (store.DashboardPattern{Platform: "gitlab", Host: "g.example", Pattern: "a/b"}) {
		t.Errorf("normalized: %+v", d)
	}
}

func TestService(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	legacy := Legacy{Enabled: true, Title: "Bots", Access: Token, TokenHash: HashToken("old")}
	svc := New(st, func(context.Context) Legacy { return legacy })
	if created, err := svc.EnsureDefault(ctx); err != nil || !created {
		t.Fatalf("ensure: %v %v", created, err)
	}
	if created, _ := svc.EnsureDefault(ctx); created {
		t.Error("the default dashboard is created once")
	}
	def, ok, _ := svc.BySlug(ctx, DefaultSlug)
	if !ok || !TokenOK(def, "old", time.Now()) || def.Title != "Bots" {
		t.Fatalf("default: %+v", def)
	}
	// An unrelated Settings save doesn't touch the default dashboard...
	tok, def, err := svc.RotateToken(ctx, def.ID, 30*24*time.Hour, "a@x")
	if err != nil || def.TokenExpires.IsZero() {
		t.Fatal(err)
	}
	if err := svc.SyncLegacy(ctx, legacy, legacy, "a@x"); err != nil {
		t.Fatal(err)
	}
	if def, _, _ = svc.BySlug(ctx, DefaultSlug); !TokenOK(def, tok, time.Now()) {
		t.Error("a token rotated on the dashboard survives an unrelated save")
	}
	// ...but changing the title there does, and only the title.
	after := legacy
	after.Title = "Reviews"
	if err := svc.SyncLegacy(ctx, legacy, after, "a@x"); err != nil {
		t.Fatal(err)
	}
	if def, _, _ = svc.BySlug(ctx, DefaultSlug); def.Title != "Reviews" || !TokenOK(def, tok, time.Now()) {
		t.Errorf("synced: %+v", def)
	}
	if err := svc.Delete(ctx, def.ID, "a@x"); err == nil {
		t.Error("the default dashboard can't be deleted")
	}
	def.Slug = "renamed"
	if _, err := svc.Save(ctx, def, "a@x"); err == nil {
		t.Error("the default dashboard keeps its slug")
	}

	hrms, err := svc.Save(ctx, Dashboard{Slug: "hrms", Title: "HRMS", Access: Token, Enabled: true}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, Dashboard{Slug: "hrms", Title: "Again", Access: Admins}, "a@x"); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Errorf("duplicate slug: %v", err)
	}
	tok2, hrms, _ := svc.RotateToken(ctx, hrms.ID, 0, "a@x")
	hrms.Title = "HRMS team"
	if hrms, err = svc.Save(ctx, hrms, "a@x"); err != nil || !TokenOK(hrms, tok2, time.Now()) || TokenOK(hrms, tok, time.Now()) {
		t.Errorf("an edit keeps the token, and another dashboard's token doesn't open it: %v", err)
	}
	if err := svc.Delete(ctx, hrms.ID, "a@x"); err != nil {
		t.Error(err)
	}
	// Without a database, the old settings are the only dashboard.
	none := New(nil, func(context.Context) Legacy { return legacy })
	if list, _ := none.List(ctx); len(list) != 1 || list[0].Slug != DefaultSlug || list[0].Title != "Bots" {
		t.Errorf("no database: %+v", list)
	}
}
