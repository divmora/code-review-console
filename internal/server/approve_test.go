package server

import (
	"context"
	"testing"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestReviewJobApproval: the approval rule from Settings reaches the CLI,
// and a rule can switch approval off for its repositories.
func TestReviewJobApproval(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := durableServer(t, st)
	defer stop(s)
	fx := s.exec.(*fakeExec)
	on, off, score := true, false, 75
	if _, err := s.settings.Put(ctx, settings.Stored{Approve: &on, ApproveBlock: "minor", ApproveMinScore: &score, ApproveWithdraw: &off}, "a@x"); err != nil {
		t.Fatal(err)
	}
	run := func(repo string, n int) executor.Task {
		t.Helper()
		before := len(fx.tasks)
		if err := s.ReviewNow(ctx, vcs.Ref{Platform: "gitlab", Host: "127.0.0.1:1", Repo: repo, Number: n}, false, "a@x"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { fx.mu.Lock(); defer fx.mu.Unlock(); return len(fx.tasks) > before })
		fx.mu.Lock()
		defer fx.mu.Unlock()
		return fx.tasks[len(fx.tasks)-1]
	}
	if a := run("grp/app", 1).Approval; a == nil || *a != (executor.Approval{Block: "minor", MinScore: 75, Withdraw: false}) {
		t.Errorf("the settings' rule: %+v", a)
	}
	if _, err := st.CreateRule(ctx, store.Rule{Platform: "gitlab", Host: "127.0.0.1:1", Pattern: "grp/payments", Approve: &off}, "a@x"); err != nil {
		t.Fatal(err)
	}
	if a := run("grp/payments", 2).Approval; a != nil {
		t.Errorf("a rule switches approval off: %+v", a)
	}
}
