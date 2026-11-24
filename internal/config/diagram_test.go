package config

import "testing"

func TestDiagramOverride(t *testing.T) {
	for v, ok := range map[string]bool{"off": true, "auto": true, "always": true, "sometimes": false, "": false} {
		d := v
		if err := (&Overrides{Diagram: &d}).Validate(nil); (err == nil) != ok {
			t.Errorf("%q: %v", v, err)
		}
	}
	for v, want := range map[string][2]bool{"0.7.0": {true, true}, "v0.8.1": {true, true}, "0.6.1": {false, true}, "latest": {true, false}, "0.0.0-dev": {true, false}} {
		if ok, known := CLIAtLeast(v, DiagramCLIVersion); ok != want[0] || known != want[1] {
			t.Errorf("%s: %v %v", v, ok, known)
		}
	}
}
