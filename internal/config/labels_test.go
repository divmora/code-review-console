package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLabelsOverride(t *testing.T) {
	for _, c := range []struct {
		l  Labels
		ok bool
	}{
		{Labels{Mode: "suggest", Allow: []string{"type::*"}}, true},
		{Labels{Mode: "apply", Max: 10}, true},
		{Labels{Allow: []string{"backend"}}, true}, // patterns only: mode inherited
		{Labels{Mode: "sometimes"}, false},
		{Labels{Max: 11}, false},
		{Labels{Allow: []string{" "}}, false},
		{Labels{Instructions: strings.Repeat("x", 4001)}, false},
	} {
		l := c.l
		if err := (&Overrides{Labels: &l}).Validate(nil); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.l, err)
		}
	}
	// Set fields win, the rest is kept (as the CLI merges its levels).
	base := &Labels{Mode: "suggest", Allow: []string{"type::*"}, Max: 3}
	got := base.Merge(&Labels{Mode: "apply", SuggestOnly: []string{"priority::*"}})
	if got.Mode != "apply" || got.Allow[0] != "type::*" || got.SuggestOnly[0] != "priority::*" || got.Max != 3 || base.Mode != "suggest" {
		t.Errorf("merge: %+v (base %+v)", got, base)
	}
	if (*Labels)(nil).Merge(&Labels{}) != nil || base.Merge(nil) != base {
		t.Error("empty levels change nothing")
	}
	// Not set: no "labels" key, which CLIs before 0.8.0 would reject.
	b, _ := json.Marshal(Overrides{})
	if strings.Contains(string(b), "labels") {
		t.Errorf("sent without labels: %s", b)
	}
	if ok, known := CLIAtLeast("0.7.0", LabelsCLIVersion); ok || !known {
		t.Error("0.7.0 has no labels")
	}
}
