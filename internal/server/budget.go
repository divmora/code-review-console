package server

import (
	"context"
	"fmt"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
)

// overBudget reports why a job must not run because today's token budget
// is used up ("" when it may run). Budgets count input and output tokens
// of finished jobs in the current UTC day, from the daily rollup; a job
// that starts under budget may finish over it.
func (s *Server) overBudget(ctx context.Context, ref vcs.Ref) string {
	if s.store == nil {
		return ""
	}
	eff := s.settings.Runtime(ctx)
	now := time.Now()
	check := func(budget int64, repo string, what string) string {
		if budget <= 0 {
			return ""
		}
		used, err := s.store.TokensUsed(ctx, now, ref.Platform, ref.Host, repo)
		if err != nil {
			s.logger.Warn("could not read token usage", "error", err)
			return "" // fail open: a metrics error should not stop reviews
		}
		if used >= budget {
			return fmt.Sprintf("the daily token budget %s is used up (%d of %d tokens today, UTC)", what, used, budget)
		}
		return ""
	}
	if why := check(eff.DailyTokenBudget, "", "for all repositories"); why != "" {
		return why
	}
	return check(eff.RepoDailyTokenBudget, ref.Repo, "for "+ref.Repo)
}
