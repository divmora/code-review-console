package admin

import (
	"fmt"
	"os"
	"testing"
)

// The admin tests serve the real UI, which isn't committed: say how to
// build it instead of failing test by test.
func TestMain(m *testing.M) {
	if _, err := uiFS.ReadFile("ui/index.html"); err != nil {
		fmt.Fprintln(os.Stderr, "admin tests need the admin UI: run `make web` (or `make test`) first")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
