package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// State of a job record.
type State string

const (
	Pending State = "pending"
	Running State = "running"
	Done    State = "done"
	Skipped State = "skipped"
	Failed  State = "failed"
)

// Meta describes a job for the dashboard.
type Meta struct {
	Kind     string `json:"kind"` // review, full review, answer, help
	Platform string `json:"platform"`
	Host     string `json:"host,omitempty"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	URL      string `json:"url,omitempty"`
	Trigger  string `json:"trigger"` // push, command, ui
	// RequestedBy is the admin who triggered the job from the UI.
	RequestedBy string `json:"requested_by,omitempty"`
}

// Report is filled in by a running job.
type Report struct {
	Note      string `json:"note,omitempty"`
	Skipped   bool   `json:"-"`
	Comments  int    `json:"comments"`
	Files     int    `json:"files"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Model     string `json:"model,omitempty"`
	// Result is the full review result JSON (stored, not shown in status).
	Result json.RawMessage `json:"-"`
}

// Record is one job's lifecycle, as shown on the dashboard.
type Record struct {
	ID int64 `json:"id"`
	Meta
	State    State     `json:"state"`
	Enqueued time.Time `json:"enqueued"`
	ReadyAt  time.Time `json:"ready_at"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Merged counts later events folded into this job (debounced pushes).
	Merged int `json:"merged"`
	// durable is the saved copy's ID (see Job.Durable).
	durable string
	Error   string `json:"error,omitempty"`
	Report
}

// Job is a unit of background work.
type Job struct {
	// Key groups jobs for one PR. Pending jobs with the same key merge,
	// and at most one job per key runs at a time.
	Key  string
	Meta Meta
	// State is caller data; Merge can read the older job's State.
	State any
	// Merge folds a pending older job into this newer one (e.g. a pending
	// full review stays full).
	Merge func(older *Job)
	Run   func(ctx context.Context, rep *Report) error
	Delay time.Duration
	Label string
	// Durable is the ID of the job's saved copy in the database, so a
	// waiting job survives a restart ("" = not saved).
	Durable string
}

// ErrQueueFull is returned when too many jobs are pending.
var ErrQueueFull = errors.New("review queue is full")

type pending struct {
	job     *Job
	readyAt time.Time
	rec     *Record
}

// Queue runs jobs on a fixed worker pool and keeps a short history.
type Queue struct {
	logger  *slog.Logger
	timeout time.Duration
	limit   int
	keep    int

	mu      sync.Mutex
	workers int // target number of workers
	live    int // workers running now
	shrink  chan struct{}
	nextID  int64
	pending map[string]*pending
	running map[string]*Record
	history []Record // newest last, capped at keep
	closed  bool
	ready   chan string
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time

	// OnFinish, when set, is called (outside the lock) with every finished
	// job, e.g. to persist it.
	OnFinish func(Record)
	// OnStart is called (outside the lock) when a job starts running;
	// OnDrop with the saved copy's ID of a job a newer one replaced.
	OnStart func(Record)
	OnDrop  func(durable string)
}

// NewQueue starts workers. history is how many finished jobs to keep.
func NewQueue(workers, limit int, timeout time.Duration, history int, logger *slog.Logger) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	if history <= 0 {
		history = 200
	}
	q := &Queue{
		logger: logger, timeout: timeout, limit: limit, keep: history,
		pending: map[string]*pending{}, running: map[string]*Record{},
		ready: make(chan string, readyCap), ctx: ctx, cancel: cancel, started: time.Now(),
		shrink: make(chan struct{}, 64),
	}
	q.mu.Lock()
	q.setWorkers(workers)
	q.mu.Unlock()
	return q
}

// readyCap is the ready channel's size: room for every pending job of the
// largest queue the admin panel allows (push retries when it is full).
const readyCap = 1024

// setWorkers starts or stops workers to reach n. mu must be held. A
// stopped worker finishes the job it runs first.
func (q *Queue) setWorkers(n int) {
	if n < 1 {
		n = 1
	}
	q.workers = n
	for q.live < n {
		q.live++
		q.wg.Add(1)
		go q.worker()
	}
	for extra := q.live - n; extra > 0; extra-- {
		select {
		case q.shrink <- struct{}{}:
		default:
		}
	}
}

// Resize changes the worker count, the pending limit, the job timeout and
// the history size while the queue runs. Running jobs keep their timeout.
func (q *Queue) Resize(workers, limit int, timeout time.Duration, history int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if limit > 0 {
		q.limit = limit
	}
	if timeout > 0 {
		q.timeout = timeout
	}
	if history > 0 {
		q.keep = history
		if len(q.history) > q.keep {
			q.history = q.history[len(q.history)-q.keep:]
		}
	}
	if workers > 0 && workers != q.workers {
		q.setWorkers(workers)
	}
}

// Workers is the configured number of workers.
func (q *Queue) Workers() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.workers
}

// retire stops this worker when there are more than wanted. mu must not be held.
func (q *Queue) retire() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.live > q.workers {
		q.live--
		return true
	}
	return false
}

// Submit enqueues a job, merging with a pending job of the same key.
func (q *Queue) Submit(j *Job) error {
	var dropped string
	defer func() { // after the unlock below
		if dropped != "" && q.OnDrop != nil {
			q.OnDrop(dropped)
		}
	}()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return errors.New("queue is shutting down")
	}
	now := time.Now()
	if p, ok := q.pending[j.Key]; ok {
		if j.Merge != nil {
			j.Merge(p.job)
		}
		if p.job.Durable != j.Durable {
			dropped = p.job.Durable
		}
		p.job = j
		p.rec.durable = j.Durable
		p.readyAt = now.Add(j.Delay)
		p.rec.Meta = j.Meta
		p.rec.ReadyAt = p.readyAt
		p.rec.Merged++
		q.schedule(j.Key, j.Delay)
		return nil
	}
	if len(q.pending) >= q.limit {
		return ErrQueueFull
	}
	q.nextID++
	rec := &Record{ID: q.nextID, Meta: j.Meta, State: Pending, Enqueued: now, ReadyAt: now.Add(j.Delay), durable: j.Durable}
	q.pending[j.Key] = &pending{job: j, readyAt: rec.ReadyAt, rec: rec}
	q.schedule(j.Key, j.Delay)
	return nil
}

func (q *Queue) schedule(key string, d time.Duration) {
	if d <= 0 {
		q.push(key)
		return
	}
	time.AfterFunc(d, func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if !q.closed {
			q.push(key)
		}
	})
}

// push must be called with mu held.
func (q *Queue) push(key string) {
	select {
	case q.ready <- key:
	default:
		// Channel full: retry shortly, unless the queue shuts down.
		q.logger.Warn("ready channel full; job delayed", "key", key)
		go func() {
			select {
			case <-q.ctx.Done():
				return
			case <-time.After(time.Second):
			}
			q.mu.Lock()
			defer q.mu.Unlock()
			if !q.closed {
				q.push(key)
			}
		}()
	}
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.shrink:
			if q.retire() {
				return
			}
		case key := <-q.ready:
			q.runKey(key)
			if q.retire() {
				return
			}
		}
	}
}

func (q *Queue) runKey(key string) {
	q.mu.Lock()
	p, ok := q.pending[key]
	if !ok || q.running[key] != nil || time.Now().Before(p.readyAt) {
		// Gone, already running (re-pushed when it finishes) or debounced
		// again (a later timer will push it).
		q.mu.Unlock()
		return
	}
	delete(q.pending, key)
	rec := p.rec
	rec.State = Running
	rec.Started = time.Now()
	q.running[key] = rec
	timeout := q.timeout
	started := *rec
	q.mu.Unlock()
	if q.OnStart != nil {
		q.OnStart(started)
	}

	ctx, cancel := context.WithTimeout(q.ctx, timeout)
	var rep Report
	err := safeRun(ctx, p.job, &rep)
	cancel()

	q.mu.Lock()
	rec.Finished = time.Now()
	rec.Report = rep
	switch {
	case err != nil:
		rec.State = Failed
		rec.Error = err.Error()
	case rep.Skipped:
		rec.State = Skipped
	default:
		rec.State = Done
	}
	delete(q.running, key)
	q.history = append(q.history, *rec)
	if len(q.history) > q.keep {
		q.history = q.history[len(q.history)-q.keep:]
	}
	if _, again := q.pending[key]; again && !q.closed {
		q.push(key) // a newer job arrived while this one ran
	}
	q.mu.Unlock()

	if q.OnFinish != nil {
		q.OnFinish(*rec)
	}
	dur := rec.Finished.Sub(rec.Started).Round(time.Millisecond)
	if err != nil {
		q.logger.Error("job failed", "job", p.job.Label, "duration", dur, "error", err)
	} else {
		q.logger.Info("job done", "job", p.job.Label, "duration", dur, "state", rec.State)
	}
}

func safeRun(ctx context.Context, j *Job, rep *Report) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("panic: " + toString(r))
		}
	}()
	return j.Run(ctx, rep)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "unknown"
}

// Cancel drops a pending job (not a running one: a review is never
// stopped mid-post) and records it as skipped with the reason. It reports
// whether a job was cancelled.
func (q *Queue) Cancel(key, reason string) bool {
	q.mu.Lock()
	p, ok := q.pending[key]
	if !ok {
		q.mu.Unlock()
		return false
	}
	delete(q.pending, key)
	rec := p.rec
	rec.State = Skipped
	rec.Finished = time.Now()
	rec.Skipped, rec.Note = true, reason
	q.history = append(q.history, *rec)
	if len(q.history) > q.keep {
		q.history = q.history[len(q.history)-q.keep:]
	}
	q.mu.Unlock()
	if q.OnFinish != nil {
		q.OnFinish(*rec)
	}
	q.logger.Info("job cancelled", "job", p.job.Label, "reason", reason)
	return true
}

// Seed loads finished jobs (oldest first), e.g. from the database.
func (q *Queue) Seed(rs []Record) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.history = append(append([]Record{}, rs...), q.history...)
	if len(q.history) > q.keep {
		q.history = q.history[len(q.history)-q.keep:]
	}
}

// Stats reports queue depth.
func (q *Queue) Stats() (pendingN, runningN int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending), len(q.running)
}

// Snapshot is the dashboard view of the queue.
type Snapshot struct {
	Now      time.Time `json:"now"`
	Since    time.Time `json:"since"`
	Running  []Record  `json:"running"`
	Pending  []Record  `json:"pending"`
	Recent   []Record  `json:"recent"` // newest first
	Totals   Totals    `json:"totals"`
	Capacity int       `json:"capacity"`
}

// Totals summarise the kept history.
type Totals struct {
	Done        int   `json:"done"`
	Skipped     int   `json:"skipped"`
	Failed      int   `json:"failed"`
	Comments    int   `json:"comments"`
	TokensIn    int   `json:"tokens_in"`
	TokensOut   int   `json:"tokens_out"`
	AvgDuration int64 `json:"avg_duration_ms"`
}

// Snapshot copies the current state.
func (q *Queue) Snapshot() Snapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Snapshot{Now: time.Now(), Since: q.started, Capacity: q.limit,
		Running: []Record{}, Pending: []Record{}, Recent: []Record{}}
	for _, r := range q.running {
		s.Running = append(s.Running, *r)
	}
	for _, p := range q.pending {
		s.Pending = append(s.Pending, *p.rec)
	}
	sort.Slice(s.Running, func(i, j int) bool { return s.Running[i].Started.Before(s.Running[j].Started) })
	sort.Slice(s.Pending, func(i, j int) bool { return s.Pending[i].ReadyAt.Before(s.Pending[j].ReadyAt) })
	var total time.Duration
	n := 0
	for i := len(q.history) - 1; i >= 0; i-- {
		r := q.history[i]
		s.Recent = append(s.Recent, r)
		switch r.State {
		case Done:
			s.Totals.Done++
		case Skipped:
			s.Totals.Skipped++
		case Failed:
			s.Totals.Failed++
		}
		s.Totals.Comments += r.Comments
		s.Totals.TokensIn += r.TokensIn
		s.Totals.TokensOut += r.TokensOut
		if r.State == Done && (r.Kind == "review" || r.Kind == "full review") {
			total += r.Finished.Sub(r.Started)
			n++
		}
	}
	if n > 0 {
		s.Totals.AvgDuration = (total / time.Duration(n)).Milliseconds()
	}
	return s
}

// Shutdown stops accepting jobs and waits for running ones up to ctx.
func (q *Queue) Shutdown(ctx context.Context) {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	for {
		q.mu.Lock()
		n := len(q.running)
		q.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-ctx.Done():
			q.cancel()
			q.wg.Wait()
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	q.cancel()
	q.wg.Wait()
}
