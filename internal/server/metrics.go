package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/metrics"
	"github.com/divmora/code-review-console/internal/version"
)

// serverMetrics are the counters the server updates as work happens.
// Labels never carry repository names: they would leak what is reviewed
// and grow without bound.
type serverMetrics struct {
	reg      metrics.Registry
	jobs     *metrics.Counter
	duration *metrics.Histogram
	tokens   *metrics.Counter
	comments *metrics.Counter
	hooks    *metrics.Counter
}

// jobBuckets cover a help reply (well under a second) to a long review.
var jobBuckets = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200}

func (s *Server) initMetrics() {
	m := &serverMetrics{}
	s.metrics = m
	started := time.Now()
	m.reg.Gauge("codereview_build_info", "The console version; always 1.", func() []metrics.Sample {
		return []metrics.Sample{{Labels: metrics.Labels{"version": version.Version}, Value: 1}}
	})
	m.reg.Gauge("codereview_start_time_seconds", "When this replica started (Unix time).", func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(started.Unix())}}
	})
	m.reg.Gauge("codereview_queue_pending", "Jobs waiting in this replica's queue (debounced or behind others).", func() []metrics.Sample {
		p, _ := s.queue.Stats()
		return []metrics.Sample{{Value: float64(p)}}
	})
	m.reg.Gauge("codereview_queue_running", "Jobs running now on this replica.", func() []metrics.Sample {
		_, r := s.queue.Stats()
		return []metrics.Sample{{Value: float64(r)}}
	})
	m.reg.Gauge("codereview_queue_capacity", "Most jobs that can wait; more webhooks get HTTP 503.", func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(s.queue.Snapshot().Capacity)}}
	})
	m.reg.Gauge("codereview_workers", "Jobs that can run at once on this replica.", func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(s.queue.Workers())}}
	})
	m.jobs = m.reg.Counter("codereview_jobs_total", "Finished jobs by kind (review, full_review, answer, help, learn) and state (done, skipped, failed).")
	m.duration = m.reg.Histogram("codereview_job_duration_seconds", "How long finished jobs ran, by kind.", jobBuckets)
	m.tokens = m.reg.Counter("codereview_model_tokens_total", "Model tokens used by finished jobs, by direction (input, output).")
	m.comments = m.reg.Counter("codereview_review_comments_total", "Comments posted by reviews.")
	m.hooks = m.reg.Counter("codereview_webhooks_total", "Webhook deliveries by platform and outcome (queued, ignored, duplicate, rejected, error).")
	m.reg.Gauge("codereview_license_state", "The Divmora license state: 1 for the current one (none, active, grace, expired, invalid).", func() []metrics.Sample {
		st := s.lic().Status()
		return []metrics.Sample{{Labels: metrics.Labels{"state": string(st.State)}, Value: 1}}
	})
	m.reg.Gauge("codereview_license_expiry_timestamp_seconds", "When the license expires (Unix time); absent for none or a perpetual license.", func() []metrics.Sample {
		st := s.lic().Status()
		if st.Expires.IsZero() || st.State == license.None {
			return nil
		}
		return []metrics.Sample{{Value: float64(st.Expires.Unix())}}
	})
	m.reg.Gauge("codereview_host_token_expiry_timestamp_seconds", "When each code host's token expires (Unix time), for tokens the host reports an expiry for.", func() []metrics.Sample {
		var out []metrics.Sample
		for _, h := range s.hosts.Enabled("") {
			if e, ok := s.hosts.TokenExpiryOf(h.ID); ok && e.Known && !e.Expires.IsZero() {
				out = append(out, metrics.Sample{Labels: metrics.Labels{"platform": h.Platform, "host": h.Host}, Value: float64(e.Expires.Unix())})
			}
		}
		return out
	})
}

// observeJob records a finished job.
func (m *serverMetrics) observeJob(r Record) {
	kind := strings.ReplaceAll(r.Kind, " ", "_")
	m.jobs.Inc(metrics.Labels{"kind": kind, "state": string(r.State)})
	if !r.Started.IsZero() && !r.Finished.IsZero() {
		m.duration.Observe(metrics.Labels{"kind": kind}, r.Finished.Sub(r.Started).Seconds())
	}
	m.tokens.Add(metrics.Labels{"direction": "input"}, float64(r.TokensIn))
	m.tokens.Add(metrics.Labels{"direction": "output"}, float64(r.TokensOut))
	m.comments.Add(nil, float64(r.Comments))
}

// onFinish is the queue's hook for every finished job.
func (s *Server) onFinish(r Record) {
	s.metrics.observeJob(r)
	if s.store != nil {
		s.persistJob(r)
	}
}

// handleMetrics serves /metrics, as chosen under Settings: off (404), for
// a bearer token, or open.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	t := s.tuning.Get(ctx)
	if t.Metrics() == "off" {
		http.NotFound(w, r)
		return
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !t.MetricsOpen(tok) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		http.Error(w, "metrics token required (Authorization: Bearer ...)", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	s.metrics.reg.Write(w)
}
