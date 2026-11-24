package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
)

// Waiting jobs are saved in the database (issue #88), so a restart, an
// upgrade or an evicted pod doesn't drop reviews that were still waiting.
// Each replica has an owner ID and a heartbeat; on start, and every
// minute, a replica takes over the saved jobs of replicas whose heartbeat
// stopped (its own previous run included) and queues them again. A job
// that was already running when its replica stopped is recorded as
// interrupted, to re-run from Reviews: running it again on its own could
// post the same comments twice.

const (
	heartbeatEvery = 30 * time.Second
	ownerStale     = 90 * time.Second
)

// queuedPayload is what it takes to queue a job again.
type queuedPayload struct {
	Kind         string `json:"kind"` // review, answer, help
	Platform     string `json:"platform"`
	Host         string `json:"host"`
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Installation int64  `json:"installation,omitempty"` // GitHub App
	// HostID is the code host that received the event: a restored job runs
	// with its credentials, whatever host the PR's URL names (issue #127).
	// 0 in jobs saved before this field: they're matched by Host.
	HostID    int64             `json:"host_id,omitempty"`
	Full      bool              `json:"full,omitempty"`
	Auto      bool              `json:"auto,omitempty"`
	By        string            `json:"by,omitempty"`
	Overrides *config.Overrides `json:"overrides,omitempty"`
	Question  string            `json:"question,omitempty"`
	// Meta is shown in history if the job was interrupted.
	Trigger string `json:"trigger,omitempty"`
}

func (p queuedPayload) ref() vcs.Ref {
	return vcs.Ref{Platform: p.Platform, Host: p.Host, Repo: p.Repo, Number: p.Number}
}

func payloadFor(kind string, tgt target, ref vcs.Ref) queuedPayload {
	return queuedPayload{Kind: kind, Platform: ref.Platform, Host: ref.Host, Repo: ref.Repo, Number: ref.Number, Installation: tgt.installation, HostID: tgt.host.ID}
}

// submit queues j and, with a database, saves it first so it survives a
// restart. id is the saved copy to reuse (a restored job), or "".
func (s *Server) submit(j *Job, p queuedPayload, id string) error {
	if s.store == nil {
		return s.queue.Submit(j)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if id == "" {
		id = uuid.NewString()
		body, _ := json.Marshal(p)
		if err := s.store.PutQueued(ctx, store.QueuedJob{ID: id, Key: j.Key, Owner: s.owner, Kind: p.Kind, Payload: body, ReadyAt: time.Now().Add(j.Delay)}); err != nil {
			// The job still runs; it just won't survive a restart.
			s.logger.Warn("could not save a queued job; it won't survive a restart", "job", j.Label, "error", err)
			id = ""
		}
	}
	j.Durable = id
	if err := s.queue.Submit(j); err != nil {
		if id != "" {
			_ = s.store.DeleteQueued(ctx, id)
		}
		return err
	}
	return nil
}

// updateSaved records a merged job's final options (it never re-creates a
// saved copy the job already removed by finishing).
func (s *Server) updateSaved(j *Job, p queuedPayload) {
	if s.store == nil || j.Durable == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, _ := json.Marshal(p)
	_ = s.store.UpdateQueued(ctx, j.Durable, body, time.Now().Add(j.Delay))
}

func (s *Server) savedStarted(r Record) {
	if r.durable != "" && s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.store.StartQueued(ctx, r.durable)
	}
}

func (s *Server) savedDone(id string) {
	if id != "" && s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.store.DeleteQueued(ctx, id)
	}
}

// durableLoop keeps this replica's heartbeat and takes over the saved jobs
// of replicas that stopped.
func (s *Server) durableLoop() {
	defer close(s.durableDone)
	beat := time.NewTicker(heartbeatEvery)
	defer beat.Stop()
	last := time.Time{}
	for {
		select {
		case <-s.stopPrune: // stopped before this beat: don't bring the owner back
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := s.store.HeartbeatOwner(ctx, s.owner); err != nil {
			s.logger.Warn("queue heartbeat failed", "error", err)
		} else if time.Since(last) >= time.Minute {
			last = time.Now()
			s.restoreSaved(ctx)
		}
		cancel()
		select {
		case <-s.stopPrune:
			return
		case <-beat.C:
		}
	}
}

// restoreSaved claims stopped replicas' saved jobs and queues them again.
func (s *Server) restoreSaved(ctx context.Context) {
	jobs, err := s.store.ClaimQueued(ctx, s.owner, ownerStale)
	if err != nil {
		s.logger.Warn("could not take over saved jobs", "error", err)
		return
	}
	requeued, interrupted := 0, 0
	for _, q := range jobs {
		var p queuedPayload
		if err := json.Unmarshal(q.Payload, &p); err != nil {
			s.logger.Warn("unreadable saved job dropped", "id", q.ID, "error", err)
			_ = s.store.DeleteQueued(ctx, q.ID)
			continue
		}
		if !q.StartedAt.IsZero() {
			s.recordInterrupted(q, p)
			_ = s.store.DeleteQueued(ctx, q.ID)
			interrupted++
			continue
		}
		if err := s.requeue(q, p); err != nil {
			s.logger.Warn("saved job not queued again", "job", p.Kind+" "+p.ref().String(), "error", err)
			s.recordDropped(q, p, err.Error())
			_ = s.store.DeleteQueued(ctx, q.ID)
			continue
		}
		requeued++
	}
	if requeued+interrupted > 0 {
		s.logger.Info("took over saved jobs", "queued_again", requeued, "interrupted", interrupted)
	}
}

// targetFor builds the code host connection a saved job needs.
func (s *Server) targetFor(platform, host string, installation int64, repo string) (target, error) {
	h, ok := s.hosts.Find(platform, host)
	if !ok {
		return target{}, fmt.Errorf("no enabled %s host %s any more", platform, host)
	}
	return s.targetOf(h, installation, repo)
}

// targetByID is the target of the enabled code host with this ID.
func (s *Server) targetByID(platform string, id, installation int64, repo string) (target, error) {
	for _, h := range s.hosts.Enabled(platform) {
		if h.ID == id {
			return s.targetOf(h, installation, repo)
		}
	}
	return target{}, fmt.Errorf("no enabled %s host #%d any more", platform, id)
}

func (s *Server) targetOf(h hosts.Host, installation int64, repo string) (target, error) {
	platform := h.Platform
	switch platform {
	case hosts.GitHub:
		return s.githubTarget(h, installation, repo), nil
	case hosts.GitLab:
		return s.gitlabTarget(h, repo), nil
	case hosts.Bitbucket:
		return s.bitbucketTarget(h, repo), nil
	}
	return target{}, fmt.Errorf("unsupported platform %q", platform)
}

func (s *Server) requeue(q store.QueuedJob, p queuedPayload) error {
	var tgt target
	var err error
	if p.HostID != 0 {
		tgt, err = s.targetByID(p.Platform, p.HostID, p.Installation, p.Repo)
	} else {
		tgt, err = s.targetFor(p.Platform, p.Host, p.Installation, p.Repo)
	}
	if err != nil {
		return err
	}
	delay := max(0, time.Until(q.ReadyAt))
	ref := p.ref()
	switch p.Kind {
	case "review":
		return s.enqueueReviewWith(tgt, ref, reviewOpts{Full: p.Full, Auto: p.Auto, By: p.By, Overrides: p.Overrides}, delay, q.ID)
	case "answer":
		return s.enqueueAnswerWith(tgt, ref, p.Question, p.Overrides, q.ID)
	case "help":
		return s.postHelpWith(tgt, ref, q.ID)
	case "diagram":
		return s.enqueueDiagram(tgt, ref, p.Overrides, q.ID)
	}
	return fmt.Errorf("unknown job kind %q", p.Kind)
}

func (s *Server) historyRecord(q store.QueuedJob, p queuedPayload, state State, note, errText string) Record {
	kind := p.Kind
	if kind == "review" && p.Full {
		kind = "full review"
	}
	r := Record{Meta: meta(p.ref(), kind, p.Trigger), State: state, Enqueued: q.CreatedAt, Started: q.StartedAt, Finished: time.Now(), Error: errText}
	r.RequestedBy, r.Note = p.By, note
	return r
}

// recordInterrupted puts a job its replica stopped in the middle of into
// the history as failed, so it shows on Reviews with Re-run.
func (s *Server) recordInterrupted(q store.QueuedJob, p queuedPayload) {
	r := s.historyRecord(q, p, Failed, "", "interrupted by a restart of the console: run it again with Re-run")
	s.metrics.observeJob(r)
	s.persistJob(r)
	s.logger.Warn("job interrupted by a restart", "job", p.Kind+" "+p.ref().String(), "started", q.StartedAt)
}

func (s *Server) recordDropped(q store.QueuedJob, p queuedPayload, why string) {
	r := s.historyRecord(q, p, Skipped, "not queued again after a restart: "+why, "")
	s.persistJob(r)
}
