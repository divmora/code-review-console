package config

import (
	"fmt"
	"strings"
)

// Profile sets how picky the reviewer is.
type Profile string

// Review profiles.
const (
	Chill     Profile = "chill"
	Balanced  Profile = "balanced"
	Assertive Profile = "assertive"
)

// Overrides are per-repository settings an admin sets in the UI. They are
// passed to the CLI as CODEREVIEW_OVERRIDES (JSON); the keys are the CLI's
// documented contract. The CLI validates them again before every review.
type Overrides struct {
	Profile     *Profile `json:"profile,omitempty"`
	MaxComments *int     `json:"max_comments,omitempty"`
	MinSeverity *string  `json:"min_severity,omitempty"`
	// Model is a model profile name.
	Model        *string `json:"model,omitempty"`
	AutoReview   *bool   `json:"auto_review,omitempty"`
	Drafts       *bool   `json:"drafts,omitempty"`
	Instructions string  `json:"instructions,omitempty"` // appended to repo instructions
	// Diagram is the walkthrough's flow diagram: off, auto or always (nil:
	// the repository's .codereview.yaml). "off" also refuses "@bot
	// diagram". Needs code-review-bot 0.7.0 (DiagramCLIVersion).
	Diagram *string `json:"diagram,omitempty"`
	// Labels pick the project's own labels for the PR (nil: the
	// repository's .codereview.yaml). Set fields win over the lower
	// levels. Needs code-review-bot 0.8.0 (LabelsCLIVersion).
	Labels *Labels `json:"labels,omitempty"`
	// Learnings are team decisions attached by the console per job (from
	// its learnings store); they are not set in the admin UI's repo form.
	Learnings []Learning `json:"learnings,omitempty"`
}

// Labels are label suggestions as the CLI expects them (its "labels"
// override): off, suggest (a line in the walkthrough) or apply (also set
// on the PR). Only labels the project already has and Allow matches are
// used; SuggestOnly ones are shown but never set. Patterns are globs
// ("type::*") or exact names, case-insensitive.
type Labels struct {
	Mode         string   `json:"mode,omitempty"`
	Allow        []string `json:"allow,omitempty"`
	SuggestOnly  []string `json:"suggest_only,omitempty"`
	Max          int      `json:"max,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
}

// Label modes.
const (
	LabelsOff     = "off"
	LabelsSuggest = "suggest"
	LabelsApply   = "apply"
)

// MaxLabels is the CLI's cap on labels per PR.
const MaxLabels = 10

// Empty reports whether no field is set.
func (l *Labels) Empty() bool {
	return l == nil || (l.Mode == "" && l.Allow == nil && l.SuggestOnly == nil && l.Max == 0 && l.Instructions == "")
}

// Merge writes the set fields of over onto a copy of l, as the CLI
// merges its levels.
func (l *Labels) Merge(over *Labels) *Labels {
	if over.Empty() {
		return l
	}
	out := Labels{}
	if l != nil {
		out = *l
	}
	if over.Mode != "" {
		out.Mode = over.Mode
	}
	if over.Allow != nil {
		out.Allow = over.Allow
	}
	if over.SuggestOnly != nil {
		out.SuggestOnly = over.SuggestOnly
	}
	if over.Max > 0 {
		out.Max = over.Max
	}
	if over.Instructions != "" {
		out.Instructions = over.Instructions
	}
	return &out
}

func (l *Labels) validate() error {
	switch l.Mode {
	case "", LabelsOff, LabelsSuggest, LabelsApply:
	default:
		return fmt.Errorf("labels mode %q: want off, suggest or apply", l.Mode)
	}
	if l.Max < 0 || l.Max > MaxLabels {
		return fmt.Errorf("labels: at most %d labels per pull request", MaxLabels)
	}
	if len(l.Allow)+len(l.SuggestOnly) > 200 {
		return fmt.Errorf("labels: at most 200 patterns")
	}
	for _, p := range append(append([]string{}, l.Allow...), l.SuggestOnly...) {
		if strings.TrimSpace(p) == "" || len(p) > 255 {
			return fmt.Errorf("labels: label pattern %q is empty or too long", p)
		}
	}
	if len(l.Instructions) > 4000 {
		return fmt.Errorf("labels: instructions are limited to 4000 characters")
	}
	return nil
}

// Learning is one team decision as the CLI expects it.
type Learning struct {
	Text string `json:"text"`
	Path string `json:"path,omitempty"`
}

// Validate checks values so the admin UI can reject them at once instead
// of failing the next review. profiles are the known model profile names.
func (o *Overrides) Validate(profiles map[string]bool) error {
	if o == nil {
		return nil
	}
	if o.Profile != nil {
		switch *o.Profile {
		case Chill, Balanced, Assertive:
		default:
			return fmt.Errorf("profile %q: want chill, balanced or assertive", *o.Profile)
		}
	}
	if o.MaxComments != nil && (*o.MaxComments < 0 || *o.MaxComments > 200) {
		return fmt.Errorf("max_comments must be between 0 and 200")
	}
	if o.MinSeverity != nil {
		switch *o.MinSeverity {
		case "", "critical", "major", "minor", "nitpick":
		default:
			return fmt.Errorf("min_severity %q: want critical, major, minor or nitpick", *o.MinSeverity)
		}
	}
	if o.Model != nil && *o.Model != "" && !profiles[*o.Model] {
		return fmt.Errorf("model %q is not a model profile", *o.Model)
	}
	if len(o.Instructions) > 8000 {
		return fmt.Errorf("instructions are limited to 8000 characters")
	}
	if o.Diagram != nil {
		switch *o.Diagram {
		case "off", "auto", "always":
		default:
			return fmt.Errorf("diagram %q: want off, auto or always", *o.Diagram)
		}
	}
	if o.Labels != nil {
		if err := o.Labels.validate(); err != nil {
			return err
		}
	}
	return nil
}
