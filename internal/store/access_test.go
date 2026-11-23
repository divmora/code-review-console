package store

import (
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
)

func ptr[T any](v T) *T { return &v }

func TestNormalizePattern(t *testing.T) {
	for in, want := range map[string]string{
		" /Acme/ ":       "acme",
		"acme/Platform":  "acme/platform",
		"acme/svc-*":     "acme/svc-*",
		"acme/*/api":     "acme/*/api",
		"group/sub.team": "group/sub.team",
	} {
		if got, err := NormalizePattern(in); err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
	if p, err := NormalizePattern("  /  "); err != nil || p != "" {
		t.Errorf("empty pattern means the whole host: %q %v", p, err)
	}
	for _, bad := range []string{"*", "**", "acme/../x", "acme//x", "acme/[x", "a b", "acme/."} {
		if _, err := NormalizePattern(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestRuleMatches(t *testing.T) {
	gl := func(p string) Rule { return Rule{Platform: "gitlab", Host: "gitlab.com", Pattern: p} }
	cases := []struct {
		rule Rule
		path string
		want bool
	}{
		{gl("acme"), "acme/api", true},
		{gl("acme"), "acme/platform/api", true}, // subgroups
		{gl("acme"), "acme-corp/api", false},    // segment boundary
		{gl("acme/platform"), "acme/api", false},
		{gl("acme/svc-*"), "acme/svc-billing", true},
		{gl("acme/svc-*"), "acme/svc-billing/x", false}, // * stays in one segment
		{gl("acme/*/api"), "acme/team/api", true},
		{gl("acme"), "ACME/Api", true}, // case-insensitive
	}
	for _, c := range cases {
		if got := c.rule.Matches("gitlab", "gitlab.com", c.path); got != c.want {
			t.Errorf("%s vs %s: got %v", c.rule.Pattern, c.path, got)
		}
	}
	if gl("acme").Matches("github", "github.com", "acme/api") || gl("acme").Matches("gitlab", "gitlab.example.com", "acme/api") {
		t.Error("rules are scoped to their platform and host")
	}
}

func TestResolve(t *testing.T) {
	r := func(id int64, p string, on bool, st config.Overrides) Rule {
		return Rule{ID: id, Platform: "gitlab", Host: "gitlab.com", Pattern: p, Enabled: &on, Settings: st}
	}
	rules := []Rule{
		r(1, "acme", true, config.Overrides{Profile: ptr(config.Chill), MaxComments: ptr(10), Instructions: "Org rules."}),
		r(2, "acme/legacy", false, config.Overrides{}),
		r(3, "acme/svc-*", true, config.Overrides{Profile: ptr(config.Assertive), Instructions: "Service rules."}),
	}
	repo := func(path, mode string) Repo {
		return Repo{Platform: "gitlab", Host: "gitlab.com", Path: path, Mode: mode}
	}

	a := Resolve(repo("acme/api", ModeInherit), rules, Base{Allowlist: true})
	if !a.Enabled || a.Source != "rule acme" || *a.Settings.Profile != config.Chill {
		t.Errorf("group rule: %+v", a)
	}
	a = Resolve(repo("acme/legacy/billing", ModeInherit), rules, Base{Allowlist: true})
	if a.Enabled || a.Source != "rule acme/legacy" {
		t.Errorf("deeper rule should win: %+v", a)
	}
	a = Resolve(repo("acme/svc-pay", ModeInherit), rules, Base{Allowlist: true})
	if !a.Enabled || a.Source != "rule acme/svc-*" || *a.Settings.Profile != config.Assertive || *a.Settings.MaxComments != 10 {
		t.Errorf("pattern rule should win and merge: %+v", a)
	}
	if a.Settings.Instructions != "Org rules.\n\nService rules." || strings.Join(a.Rules, ",") != "acme/svc-*,acme" {
		t.Errorf("instructions and rule list: %q %v", a.Settings.Instructions, a.Rules)
	}
	a = Resolve(Repo{Platform: "gitlab", Host: "gitlab.com", Path: "acme/legacy/x", Mode: ModeEnabled, Settings: config.Overrides{MaxComments: ptr(3)}}, rules, Base{Allowlist: true})
	if !a.Enabled || a.Source != "repository" || *a.Settings.MaxComments != 3 {
		t.Errorf("repository mode and settings win: %+v", a)
	}
	if a := Resolve(repo("other/api", ModeInherit), rules, Base{Allowlist: true}); a.Enabled || a.Source != "allowlist" {
		t.Errorf("no match with allowlist: %+v", a)
	}
	if a := Resolve(repo("other/api", ModeInherit), rules, Base{}); !a.Enabled || a.Source != "default" {
		t.Errorf("no match without allowlist: %+v", a)
	}
	if a := Resolve(repo("acme/legacy/x", ModeInherit), rules, Base{}); a.Enabled {
		t.Errorf("a disable rule applies even without the allowlist: %+v", a)
	}
}

func TestMergeInstructionsLimit(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := Merge(config.Overrides{Instructions: long}, config.Overrides{Instructions: long + "b"})
	if got.Instructions != long+"b" {
		t.Errorf("over the CLI limit, the more specific text should win (len %d)", len(got.Instructions))
	}
}

func TestResolveLevels(t *testing.T) {
	on, off := true, false
	host := Rule{ID: 1, Platform: "gitlab", Host: "gl.x", Pattern: "", Enabled: &on, RequireReviewer: &off, ScoreLabels: &on,
		Settings: config.Overrides{MaxComments: ptr(20)}}
	group := Rule{ID: 2, Platform: "gitlab", Host: "gl.x", Pattern: "acme/payroll", RequireReviewer: &on, Settings: config.Overrides{MaxComments: ptr(5)}}
	other := Rule{ID: 3, Platform: "gitlab", Host: "gl.y", Pattern: "", Enabled: &off}
	rules := []Rule{group, host, other}
	base := Base{Allowlist: true, RequireReviewer: true, SkipBranchSync: true, ReviewDefaults: config.Overrides{Profile: ptr(config.Chill)}}

	a := Resolve(Repo{Platform: "gitlab", Host: "gl.x", Path: "acme/payroll/payroll-ui", Mode: ModeInherit}, rules, base)
	if !a.Enabled || a.Source != "rule host gl.x" {
		t.Errorf("host rule enables every repository on the host: %+v", a)
	}
	if !a.RequireReviewer || a.RequireReviewerSource != "rule acme/payroll" {
		t.Errorf("group setting beats the host setting: %+v", a)
	}
	if !a.ScoreLabels || a.ScoreLabelsSource != "rule host gl.x" || !a.SkipBranchSync || a.SkipBranchSyncSource != "settings" {
		t.Errorf("unset at group level falls back to host, then settings: %+v", a)
	}
	if *a.Settings.MaxComments != 5 || *a.Settings.Profile != config.Chill {
		t.Errorf("settings merge global, host, group: %+v", a.Settings)
	}
	if strings.Join(a.Rules, ",") != "acme/payroll,host gl.x" {
		t.Errorf("rules most specific first: %v", a.Rules)
	}
	b := Resolve(Repo{Platform: "gitlab", Host: "gl.x", Path: "acme/finance", Mode: ModeInherit}, rules, base)
	if b.RequireReviewer || b.RequireReviewerSource != "rule host gl.x" || *b.Settings.MaxComments != 20 {
		t.Errorf("outside the group, the host level applies: %+v", b)
	}
	c := Resolve(Repo{Platform: "gitlab", Host: "gl.y", Path: "acme/finance", Mode: ModeInherit}, rules, base)
	if c.Enabled || !c.RequireReviewer || c.ScoreLabels {
		t.Errorf("another host has its own rules: %+v", c)
	}
}

func TestResolveApprove(t *testing.T) {
	on, off := true, false
	rules := []Rule{
		{ID: 1, Platform: "gitlab", Host: "gl.x", Approve: &on},                            // the whole host approves
		{ID: 2, Platform: "gitlab", Host: "gl.x", Pattern: "acme/payments", Approve: &off}, // except payments
	}
	if a := Resolve(Repo{Platform: "gitlab", Host: "gl.x", Path: "acme/web"}, rules, Base{}); !a.Approve || a.ApproveSource != "rule host gl.x" {
		t.Errorf("host rule: %+v", a)
	}
	if a := Resolve(Repo{Platform: "gitlab", Host: "gl.x", Path: "acme/payments/api"}, rules, Base{}); a.Approve || a.ApproveSource != "rule acme/payments" {
		t.Errorf("group rule beats the host rule: %+v", a)
	}
	if a := Resolve(Repo{Platform: "gitlab", Host: "gl.y", Path: "x/y"}, rules, Base{Approve: true}); !a.Approve || a.ApproveSource != "settings" {
		t.Errorf("no rule: the settings decide: %+v", a)
	}
}
