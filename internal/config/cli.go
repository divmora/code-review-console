package config

import (
	"fmt"
	"strconv"
	"strings"
)

// MinCLIVersion is the oldest codereview CLI this console works with. Raise
// it whenever the console starts relying on a newer CLI feature (an
// override key, a command, an output field), and pin the Dockerfile's bot
// image to at least this release.
//
// 0.4.0: CODEREVIEW_OVERRIDES "learnings" and "codereview learn".
const MinCLIVersion = "0.4.0"

// DiagramCLIVersion is the first CLI with flow diagrams (the "diagram"
// override and --diagram-only). Older CLIs reject the override key, so the
// console sends it only when set, and the admin panel refuses it when the
// Executor's version is older.
const DiagramCLIVersion = "0.7.0"

// LabelsCLIVersion is the first CLI with label suggestions (the "labels"
// override). Older CLIs reject the key, like "diagram".
const LabelsCLIVersion = "0.8.0"

// CLIAtLeast reports whether v is at least min. known is false for
// versions that are not a release (development builds, latest, digests).
func CLIAtLeast(v, min string) (ok, known bool) {
	have, ok1 := parseVersion(v)
	want, _ := parseVersion(min)
	if !ok1 {
		return true, false
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i], true
		}
	}
	return true, true
}

// CLIVersionOK reports whether v (as printed by the CLI, "v" prefix
// optional) is at least MinCLIVersion. known is false for versions that are
// not a release (development builds), which are accepted.
func CLIVersionOK(v string) (ok, known bool) {
	have, ok1 := parseVersion(v)
	want, _ := parseVersion(MinCLIVersion)
	if !ok1 {
		return true, false
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i], true
		}
	}
	return true, true
}

// parseVersion reads "1.2.3" or "v1.2.3" (any pre-release suffix is
// ignored). Development builds ("0.0.0-dev", "dev") are not releases.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 || core == "0.0.0" || strings.Contains(pre, "dev") {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// checkImageTag rejects a CLI image whose tag is a release older than
// MinCLIVersion. Other tags (latest, a digest, a custom build) are left to
// "console check-config", which asks the CLI itself.
func checkImageTag(key, image string) error {
	ref, _, _ := strings.Cut(image, "@")
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i:], "/") {
		return nil
	}
	if ok, known := CLIVersionOK(ref[i+1:]); known && !ok {
		return fmt.Errorf("%s %q: this console needs code-review-bot v%s or later", key, image, MinCLIVersion)
	}
	return nil
}

// CheckImageTag is checkImageTag for other packages (the admin panel's
// executor settings).
func CheckImageTag(key, image string) error { return checkImageTag(key, image) }
