package store

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/config"
)

// Repository modes. Inherit takes the state from the most specific rule,
// or the allowlist default when no rule matches.
const (
	ModeInherit  = "inherit"
	ModeEnabled  = "enabled"
	ModeDisabled = "disabled"
)

// ValidMode reports whether m is a repository mode.
func ValidMode(m string) bool {
	return m == ModeInherit || m == ModeEnabled || m == ModeDisabled
}

// Rule sets defaults for every repository on a host (empty Pattern), or
// under a group, organisation or pattern on that host. Each setting is
// optional: nil inherits from the less specific level.
//
// A pattern without "*" is a group: it matches that path and everything
// below it ("acme" matches acme/api and, on GitLab, acme/platform/api). A
// pattern with "*" matches whole paths, and "*" never crosses "/"
// ("acme/svc-*", "acme/*/api").
type Rule struct {
	ID              int64            `json:"id"`
	Platform        string           `json:"platform"`
	Host            string           `json:"host"`
	Pattern         string           `json:"pattern"` // "" = the whole host
	Enabled         *bool            `json:"enabled"`
	RequireReviewer *bool            `json:"require_reviewer"`
	SkipBranchSync  *bool            `json:"skip_branch_sync"`
	ScoreLabels     *bool            `json:"score_labels"`
	CommitStatus    *bool            `json:"commit_status"`
	Approve         *bool            `json:"approve"`
	Settings        config.Overrides `json:"settings"`
	UpdatedAt       time.Time        `json:"updated_at"`
	UpdatedBy       string           `json:"updated_by"`
}

// Label names the rule's scope: "host gitlab.example.com" or "acme/*".
func (r Rule) Label() string {
	if r.Pattern == "" {
		return "host " + r.Host
	}
	return r.Pattern
}

var patternRe = regexp.MustCompile(`^[a-z0-9_.*\-]+(/[a-z0-9_.*\-]+)*$`)

// NormalizePattern lower-cases and validates a rule pattern. An empty
// pattern means the whole host.
func NormalizePattern(p string) (string, error) {
	p = strings.ToLower(strings.Trim(strings.TrimSpace(p), "/"))
	if p == "" {
		return "", nil
	}
	if !patternRe.MatchString(p) {
		return "", errors.New("pattern: use a group or owner (acme, acme/platform), a path with * (acme/svc-*), or leave it empty for the whole host")
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.Trim(seg, ".") == "" {
			return "", errors.New(`pattern: "." and ".." segments are not allowed`)
		}
		if strings.Trim(seg, "*") == "" && strings.Count(p, "/") == 0 {
			return "", errors.New("pattern: a lone * would match the whole host; leave the pattern empty instead")
		}
	}
	if _, err := path.Match(p, ""); err != nil {
		return "", fmt.Errorf("pattern: %w", err)
	}
	return p, nil
}

// Matches reports whether the rule covers the repository.
func (r Rule) Matches(platform, host, repoPath string) bool {
	platform, host, repoPath = RepoKey(platform, host, repoPath)
	if r.Platform != platform || r.Host != host {
		return false
	}
	if r.Pattern == "" {
		return true
	}
	if !strings.Contains(r.Pattern, "*") {
		return repoPath == r.Pattern || strings.HasPrefix(repoPath, r.Pattern+"/")
	}
	ok, _ := path.Match(r.Pattern, repoPath)
	return ok
}

// specificity orders rules: more path segments first (the host-wide rule
// has none), then patterns without wildcards, then fewer wildcards, then
// longer patterns.
func (r Rule) specificity() [4]int {
	if r.Pattern == "" {
		return [4]int{0, 0, 0, 0}
	}
	wild := strings.Count(r.Pattern, "*")
	literal := 1
	if wild > 0 {
		literal = 0
	}
	return [4]int{strings.Count(r.Pattern, "/") + 1, literal, -wild, len(r.Pattern)}
}

// MoreSpecific reports whether rule a is more specific than b (the order
// Rules resolve in; host credentials use it too).
func MoreSpecific(a, b Rule) bool { return moreSpecific(a, b) }

func moreSpecific(a, b Rule) bool {
	sa, sb := a.specificity(), b.specificity()
	for i := range sa {
		if sa[i] != sb[i] {
			return sa[i] > sb[i]
		}
	}
	return a.ID < b.ID
}

// Base is the global level, from the console settings.
type Base struct {
	Allowlist       bool
	RequireReviewer bool
	SkipBranchSync  bool
	ScoreLabels     bool
	CommitStatus    bool
	Approve         bool
	ReviewDefaults  config.Overrides
}

// Access is the effective state of one repository.
type Access struct {
	Enabled bool `json:"enabled"`
	// Source says what decided Enabled: "repository", "rule <label>",
	// "allowlist" (no rule decides, allowlist on) or "default".
	Source          string `json:"source"`
	RequireReviewer bool   `json:"require_reviewer"`
	// RequireReviewerSource and SkipBranchSyncSource are "settings" or
	// "rule <label>".
	RequireReviewerSource string `json:"require_reviewer_source"`
	SkipBranchSync        bool   `json:"skip_branch_sync"`
	SkipBranchSyncSource  string `json:"skip_branch_sync_source"`
	// ScoreLabels posts Quality/Security/Maintainability score labels;
	// CommitStatus sets a "codereview" status on the reviewed commit.
	ScoreLabels        bool   `json:"score_labels"`
	ScoreLabelsSource  string `json:"score_labels_source"`
	CommitStatus       bool   `json:"commit_status"`
	CommitStatusSource string `json:"commit_status_source"`
	// Approve has the bot approve merge requests whose review passes the
	// rule in the settings.
	Approve       bool   `json:"approve"`
	ApproveSource string `json:"approve_source"`
	// Rules are the matching rules' labels, most specific first.
	Rules []string `json:"rules,omitempty"`
	// Settings merge the review defaults, the matching rules (least
	// specific first) and then the repository's own settings.
	Settings config.Overrides `json:"settings"`
}

// Resolve computes a repository's effective state. For each setting the
// most specific level that sets it wins: repository, then the most
// specific matching rule (group or pattern, then the host-wide rule), then
// the global settings. Reviews: an explicit repository mode wins; with no
// level deciding, the allowlist decides (on = not reviewed).
func Resolve(repo Repo, rules []Rule, b Base) Access {
	var matched []Rule
	for _, r := range rules {
		if r.Matches(repo.Platform, repo.Host, repo.Path) {
			matched = append(matched, r)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return moreSpecific(matched[i], matched[j]) })

	a := Access{Enabled: !b.Allowlist, Source: "default",
		RequireReviewer: b.RequireReviewer, RequireReviewerSource: "settings",
		SkipBranchSync: b.SkipBranchSync, SkipBranchSyncSource: "settings",
		ScoreLabels: b.ScoreLabels, ScoreLabelsSource: "settings",
		CommitStatus: b.CommitStatus, CommitStatusSource: "settings",
		Approve: b.Approve, ApproveSource: "settings"}
	if b.Allowlist {
		a.Source = "allowlist"
	}
	// For each setting, the first (most specific) rule that sets it wins.
	type flag struct {
		get      func(Rule) *bool
		dst      *bool
		src      *string
		resolved bool
	}
	flags := []*flag{
		{get: func(r Rule) *bool { return r.Enabled }, dst: &a.Enabled, src: &a.Source},
		{get: func(r Rule) *bool { return r.RequireReviewer }, dst: &a.RequireReviewer, src: &a.RequireReviewerSource},
		{get: func(r Rule) *bool { return r.SkipBranchSync }, dst: &a.SkipBranchSync, src: &a.SkipBranchSyncSource},
		{get: func(r Rule) *bool { return r.ScoreLabels }, dst: &a.ScoreLabels, src: &a.ScoreLabelsSource},
		{get: func(r Rule) *bool { return r.CommitStatus }, dst: &a.CommitStatus, src: &a.CommitStatusSource},
		{get: func(r Rule) *bool { return r.Approve }, dst: &a.Approve, src: &a.ApproveSource},
	}
	for _, r := range matched {
		for _, f := range flags {
			if v := f.get(r); !f.resolved && v != nil {
				*f.dst, *f.src, f.resolved = *v, "rule "+r.Label(), true
			}
		}
		a.Rules = append(a.Rules, r.Label())
	}
	switch repo.Mode {
	case ModeEnabled:
		a.Enabled, a.Source = true, "repository"
	case ModeDisabled:
		a.Enabled, a.Source = false, "repository"
	}
	a.Settings = b.ReviewDefaults
	for i := len(matched) - 1; i >= 0; i-- {
		a.Settings = Merge(a.Settings, matched[i].Settings)
	}
	a.Settings = Merge(a.Settings, repo.Settings)
	return a
}

const maxInstructions = 8000

// Merge applies over on top of base: set fields win; instructions add up.
func Merge(base, over config.Overrides) config.Overrides {
	out := base
	if over.Profile != nil {
		out.Profile = over.Profile
	}
	if over.MaxComments != nil {
		out.MaxComments = over.MaxComments
	}
	if over.MinSeverity != nil {
		out.MinSeverity = over.MinSeverity
	}
	if over.Model != nil {
		out.Model = over.Model
	}
	if over.AutoReview != nil {
		out.AutoReview = over.AutoReview
	}
	if over.Drafts != nil {
		out.Drafts = over.Drafts
	}
	if over.Diagram != nil {
		out.Diagram = over.Diagram
	}
	out.Labels = out.Labels.Merge(over.Labels)
	if out.Labels.Empty() {
		out.Labels = nil // an empty block would still reach CLIs that reject the key
	}
	if over.Instructions != "" {
		// The CLI accepts 8000 characters; keep the more specific text if
		// the combination would not fit.
		if out.Instructions != "" && len(out.Instructions)+2+len(over.Instructions) <= maxInstructions {
			out.Instructions += "\n\n" + over.Instructions
		} else {
			out.Instructions = over.Instructions
		}
	}
	return out
}
