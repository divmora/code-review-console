package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// durableServer is a server over st with one GitLab host and a debounce
// long enough that reviews stay waiting.
func durableServer(t *testing.T, st *storetest.Memory) *Server {
	t.Helper()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "s"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour // ignored for a loaded config: the 30s default applies
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func stop(s *Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.queue.Shutdown(ctx)
	s.Close()
}

func TestWaitingReviewsSurviveARestart(t *testing.T) {
	st := storetest.New()
	a := durableServer(t, st)
	tgt, err := a.targetFor("gitlab", "127.0.0.1:1", 0)
	if err != nil {
		t.Fatal(err)
	}
	ref := vcs.Ref{Platform: "gitlab", Host: "127.0.0.1:1", Repo: "grp/app", Number: 7}
	// Two pushes within the debounce: one waiting review, one saved copy.
	if err := a.enqueueReview(tgt, ref, reviewOpts{Auto: true}, true); err != nil {
		t.Fatal(err)
	}
	if err := a.enqueueReview(tgt, ref, reviewOpts{Full: true}, true); err != nil {
		t.Fatal(err)
	}
	saved := st.Queued()
	if len(saved) != 1 {
		t.Fatalf("one saved copy per waiting job: %+v", saved)
	}
	var p queuedPayload
	_ = json.Unmarshal(saved[0].Payload, &p)
	if p.Kind != "review" || !p.Full || p.Repo != "grp/app" || time.Until(saved[0].ReadyAt) < 20*time.Second {
		t.Errorf("the merged review is saved, with its due time: %+v %+v", p, saved[0])
	}

	// A restart: the old replica stops (dropping its in-memory queue) and a
	// new one takes the saved review over and queues it again.
	stop(a)
	b := durableServer(t, st)
	defer stop(b)
	waitFor(t, func() bool { p, _ := b.queue.Stats(); return p == 1 })
	snap := b.queue.Snapshot()
	if len(snap.Pending) != 1 || snap.Pending[0].Kind != "full review" || time.Until(snap.Pending[0].ReadyAt) < 20*time.Second {
		t.Errorf("queued again, still full and still debounced: %+v", snap.Pending)
	}
	if q := st.Queued(); len(q) != 1 || q[0].Owner != b.owner {
		t.Errorf("the new replica owns it: %+v", q)
	}
}

func TestFinishedJobsLeaveNothingSaved(t *testing.T) {
	st := storetest.New()
	s := durableServer(t, st)
	defer stop(s)
	ref := vcs.Ref{Platform: "gitlab", Host: "127.0.0.1:1", Repo: "grp/app", Number: 8}
	if err := s.ReviewNow(context.Background(), ref, false, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { snap := s.queue.Snapshot(); return len(snap.Recent) == 1 })
	waitFor(t, func() bool { return len(st.Queued()) == 0 })
}

func TestInterruptedAndLiveReplicaJobs(t *testing.T) {
	st := storetest.New()
	ctx := context.Background()
	payload, _ := json.Marshal(queuedPayload{Kind: "review", Platform: "gitlab", Host: "127.0.0.1:1", Repo: "grp/app", Number: 9, Trigger: "push"})
	// A job a stopped replica was running, and one a live replica has waiting.
	_ = st.PutQueued(ctx, store.QueuedJob{ID: "running-1", Key: "review:x#9", Owner: "gone", Kind: "review", Payload: payload, ReadyAt: time.Now()})
	_ = st.StartQueued(ctx, "running-1")
	_ = st.HeartbeatOwner(ctx, "alive")
	_ = st.PutQueued(ctx, store.QueuedJob{ID: "alive-1", Key: "review:x#10", Owner: "alive", Kind: "review", Payload: payload, ReadyAt: time.Now().Add(time.Hour)})

	s := durableServer(t, st)
	defer stop(s)
	waitFor(t, func() bool { return len(st.Queued()) == 1 })
	if q := st.Queued(); q[0].ID != "alive-1" || q[0].Owner != "alive" {
		t.Errorf("a live replica's job isn't taken: %+v", q)
	}
	if p, _ := s.queue.Stats(); p != 0 {
		t.Errorf("an interrupted job isn't run again on its own: %d waiting", p)
	}
	jobs, _ := st.RecentJobs(ctx, 10)
	if len(jobs) != 1 || jobs[0].State != string(Failed) || jobs[0].Repo != "grp/app" || jobs[0].Error == "" {
		t.Errorf("it is in the history as interrupted, for Re-run: %+v", jobs)
	}
}
