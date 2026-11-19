package config

import "testing"

func TestCLIVersionOK(t *testing.T) {
	for _, c := range []struct {
		v         string
		ok, known bool
	}{
		{"0.4.0", true, true},
		{"v0.4.1", true, true},
		{"1.0.0", true, true},
		{"0.10.0", true, true},
		{"0.3.0", false, true},
		{"v0.3.9", false, true},
		{"0.4.0-rc.1", true, true},
		{"0.0.0-dev", true, false},
		{"dev", true, false},
		{"", true, false},
	} {
		ok, known := CLIVersionOK(c.v)
		if ok != c.ok || known != c.known {
			t.Errorf("%q: got ok=%v known=%v, want %v %v", c.v, ok, known, c.ok, c.known)
		}
	}
}

func TestCheckImageTag(t *testing.T) {
	for img, bad := range map[string]bool{
		"ghcr.io/divmora/code-review-bot:v0.3.0":         true,
		"ghcr.io/divmora/code-review-bot:0.3.1":          true,
		"ghcr.io/divmora/code-review-bot:v0.4.0":         false,
		"ghcr.io/divmora/code-review-bot:latest":         false,
		"ghcr.io/divmora/code-review-bot":                false,
		"localhost:5000/code-review-bot":                 false,
		"localhost:5000/code-review-bot:v0.2.0":          true,
		"ghcr.io/divmora/code-review-bot@sha256:abcdef0": false,
	} {
		if err := checkImageTag("image", img); (err != nil) != bad {
			t.Errorf("%s: err=%v, want error=%v", img, err, bad)
		}
	}
}
