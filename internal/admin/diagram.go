package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/divmora/code-review-console/internal/config"
)

// checkDiagramCLI refuses a flow diagram setting (issue #161) when the
// Executor runs a code-review-bot release older than 0.7.0: those CLIs
// reject the "diagram" override key, so every review would fail. Unknown
// versions (the local executor, latest, a digest) are allowed.
func (a *Admin) checkDiagramCLI(ctx context.Context, o *config.Overrides) error {
	if o == nil || o.Diagram == nil || a.Executor == nil {
		return nil
	}
	st, _, err := a.Executor.Get(ctx)
	if err != nil {
		return nil
	}
	tag, _, _ := strings.Cut(st.Version, "@")
	if ok, known := config.CLIAtLeast(tag, config.DiagramCLIVersion); known && !ok {
		return fmt.Errorf("flow diagrams need code-review-bot %s or later; the Executor runs %s: change its version first", config.DiagramCLIVersion, tag)
	}
	return nil
}
