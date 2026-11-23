package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestQueueResize grows and shrinks the worker pool while jobs run.
func TestQueueResize(t *testing.T) {
	q := NewQueue(1, 10, time.Minute, 10, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer q.Shutdown(context.Background())
	var now, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	submit := func(i int) {
		if err := q.Submit(&Job{Key: fmt.Sprint("k", i), Run: func(ctx context.Context, rep *Report) error {
			n := now.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			now.Add(-1)
			return nil
		}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 4 {
		submit(i)
	}
	waitFor(t, func() bool { return now.Load() == 1 })
	q.Resize(3, 10, time.Minute, 10)
	waitFor(t, func() bool { return now.Load() == 3 })
	if q.Workers() != 3 {
		t.Errorf("workers = %d", q.Workers())
	}
	// Shrinking lets running jobs finish, then runs at most one at a time.
	q.Resize(1, 10, time.Minute, 10)
	once.Do(func() { close(release) })
	waitFor(t, func() bool { p, r := q.Stats(); return p == 0 && r == 0 })
	release = make(chan struct{})
	once = sync.Once{}
	peak.Store(0)
	for i := 10; i < 13; i++ {
		submit(i)
	}
	waitFor(t, func() bool { return now.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if peak.Load() != 1 {
		t.Errorf("after shrinking to 1, %d ran at once", peak.Load())
	}
	once.Do(func() { close(release) })
}

func TestQueueResizeLimitAndTimeout(t *testing.T) {
	q := NewQueue(1, 1, time.Minute, 10, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer q.Shutdown(context.Background())
	q.Resize(1, 1, 20*time.Millisecond, 10)
	done := make(chan error, 1)
	_ = q.Submit(&Job{Key: "slow", Run: func(ctx context.Context, rep *Report) error {
		<-ctx.Done()
		done <- ctx.Err()
		return ctx.Err()
	}})
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Errorf("the new timeout applies to the next job: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout not applied")
	}
	if err := q.Submit(&Job{Key: "a", Delay: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit(&Job{Key: "b", Delay: time.Hour}); err != ErrQueueFull {
		t.Errorf("limit 1: %v", err)
	}
	q.Resize(1, 5, time.Minute, 10)
	if err := q.Submit(&Job{Key: "b", Delay: time.Hour}); err != nil {
		t.Errorf("a larger limit takes more: %v", err)
	}
}
