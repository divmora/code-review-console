package config

import "fmt"

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
	// Learnings are team decisions attached by the console per job (from
	// its learnings store); they are not set in the admin UI's repo form.
	Learnings []Learning `json:"learnings,omitempty"`
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
	return nil
}
