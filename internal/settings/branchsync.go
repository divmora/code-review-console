// Copyright (c) DIVMORA Technologies. All rights reserved.
// Licensed under the Divmora Enterprise License; see LICENSE-ENTERPRISE.
// Use requires a valid Divmora license. Do not modify or remove license checks.

package settings

import (
	"fmt"
	"path"
)

// BranchSync reports whether a merge/pull request from source to target is
// a branch sync to skip (when skip is on): the source is the default
// branch, or matches one of the patterns. It returns the reason.
func BranchSync(skip bool, patterns []string, source, target, defaultBranch string) (bool, string) {
	if !skip || source == "" || source == target {
		return false, ""
	}
	if defaultBranch != "" && source == defaultBranch {
		return true, fmt.Sprintf("branch sync from the default branch (%s into %s)", source, target)
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, source); ok {
			return true, fmt.Sprintf("branch sync from %s into %s (matches %q)", source, target, p)
		}
	}
	return false, ""
}
