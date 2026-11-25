package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/divmora/code-review-console/internal/config"
)

// checkCLIFeatures refuses review settings the Executor's code-review-bot
// release can't take: older CLIs reject override keys they don't know, so
// every review would fail. A flow diagram (issue #161) needs 0.7.0, labels
// (issue #162) 0.8.0. Unknown versions (the local executor, latest, a
// digest) are allowed.
func (a *Admin) checkCLIFeatures(ctx context.Context, o *config.Overrides) error {
	if o == nil || a.Executor == nil {
		return nil
	}
	var need []struct{ what, min string }
	if o.Diagram != nil {
		need = append(need, struct{ what, min string }{"flow diagrams", config.DiagramCLIVersion})
	}
	if !o.Labels.Empty() {
		need = append(need, struct{ what, min string }{"labels", config.LabelsCLIVersion})
	}
	if len(need) == 0 {
		return nil
	}
	st, _, err := a.Executor.Get(ctx)
	if err != nil {
		return nil
	}
	tag, _, _ := strings.Cut(st.Version, "@")
	for _, n := range need {
		if ok, known := config.CLIAtLeast(tag, n.min); known && !ok {
			return fmt.Errorf("%s need code-review-bot %s or later; the Executor runs %s: change its version first", n.what, n.min, tag)
		}
	}
	return nil
}
