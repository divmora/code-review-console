package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/store"
)

// maxStoredHooks caps the webhook history whatever the retention.
const maxStoredHooks = 100_000

// hookMeta is filled in by a webhook handler as it learns what the
// delivery is; the recorder turns it into a store.WebhookEvent.
type hookMeta struct {
	Platform, Host, Project, Event, Kind, Action, Sender, Delivery, Auth string
	Number                                                               int
	// Reason overrides the reason taken from the response (e.g. the exact
	// verification failure, which the response keeps generic).
	Reason string
}

type hookMetaKey struct{}

// meta returns the request's hookMeta, or a throwaway one.
func hookMetaOf(ctx context.Context) *hookMeta {
	if m, ok := ctx.Value(hookMetaKey{}).(*hookMeta); ok {
		return m
	}
	return &hookMeta{}
}

// captureWriter records the status and the start of the response body.
type captureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if room := 1024 - c.body.Len(); room > 0 {
		c.body.Write(b[:min(room, len(b))])
	}
	return c.ResponseWriter.Write(b)
}

// outcome classifies a webhook response.
func outcome(status int, body []byte) (string, string) {
	var v struct{ Status, Reason, Job, Error string }
	_ = json.Unmarshal(body, &v)
	text := strings.TrimSpace(string(body))
	switch {
	case status == http.StatusAccepted:
		return store.HookQueued, v.Job
	case status == http.StatusOK && v.Status == "duplicate delivery":
		return store.HookDuplicate, "already processed this delivery"
	case status == http.StatusOK && v.Status == "pong":
		return store.HookIgnored, "ping"
	case status == http.StatusOK:
		return store.HookIgnored, v.Reason
	case status == http.StatusServiceUnavailable || status >= 500:
		return store.HookError, firstNonEmpty(v.Error, text)
	default:
		return store.HookRejected, firstNonEmpty(v.Error, text)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// recordHooks wraps a webhook handler so every delivery lands in the
// webhook history, whatever the handler decided.
func (s *Server) recordHooks(platform string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.hooks == nil {
			h(w, r)
			return
		}
		m := &hookMeta{Platform: platform}
		cw := &captureWriter{ResponseWriter: w}
		start := time.Now()
		h(cw, r.WithContext(context.WithValue(r.Context(), hookMetaKey{}, m)))
		out, reason := outcome(cw.status, cw.body.Bytes())
		if m.Reason != "" {
			reason = m.Reason
		}
		s.hooks.add(store.WebhookEvent{
			ReceivedAt: start, Platform: m.Platform, Host: m.Host, Project: m.Project, Number: m.Number,
			Event: m.Event, Kind: m.Kind, Action: m.Action, Sender: m.Sender, Delivery: m.Delivery, Auth: m.Auth,
			Outcome: out, Reason: truncate(reason, 500), Status: cw.status, DurationMS: int(time.Since(start).Milliseconds()),
		})
	}
}

// hookWriter saves webhook events in batches off the request path, and
// prunes old ones. A slow database never delays a webhook response; if the
// buffer is full, events are dropped and counted.
type hookWriter struct {
	st        store.Store
	logger    *slog.Logger
	retention func() time.Duration
	ch        chan store.WebhookEvent
	done      chan struct{}
	stopOnce  sync.Once
	mu        sync.Mutex
	dropped   int
}

func newHookWriter(st store.Store, retention func() time.Duration, logger *slog.Logger) *hookWriter {
	w := &hookWriter{st: st, logger: logger, retention: retention, ch: make(chan store.WebhookEvent, 2000), done: make(chan struct{})}
	go w.loop()
	return w
}

func (w *hookWriter) add(e store.WebhookEvent) {
	select {
	case w.ch <- e:
	default:
		w.mu.Lock()
		w.dropped++
		w.mu.Unlock()
	}
}

func (w *hookWriter) loop() {
	defer close(w.done)
	flush := time.NewTicker(time.Second)
	prune := time.NewTicker(time.Hour)
	defer flush.Stop()
	defer prune.Stop()
	var batch []store.WebhookEvent
	save := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := w.st.InsertWebhookEvents(ctx, batch); err != nil {
			w.logger.Warn("could not save webhook history", "events", len(batch), "error", err)
		}
		cancel()
		batch = batch[:0]
		w.mu.Lock()
		if w.dropped > 0 {
			w.logger.Warn("webhook history buffer full; events not saved", "dropped", w.dropped)
			w.dropped = 0
		}
		w.mu.Unlock()
	}
	w.prune()
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				save()
				return
			}
			batch = append(batch, e)
			if len(batch) >= 200 {
				save()
			}
		case <-flush.C:
			save()
		case <-prune.C:
			w.prune()
		}
	}
}

func (w *hookWriter) prune() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if n, err := w.st.PruneWebhookEvents(ctx, time.Now().Add(-w.retention()), maxStoredHooks); err != nil {
		w.logger.Warn("could not prune webhook history", "error", err)
	} else if n > 0 {
		w.logger.Info("pruned webhook history", "deleted", n)
	}
}

// close flushes pending events and stops the writer.
func (w *hookWriter) close() {
	w.stopOnce.Do(func() { close(w.ch) })
	<-w.done
}
