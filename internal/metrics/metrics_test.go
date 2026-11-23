package metrics

import (
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	var r Registry
	jobs := r.Counter("codereview_jobs_total", "Finished jobs.")
	jobs.Inc(Labels{"kind": "review", "state": "done"})
	jobs.Add(Labels{"kind": "review", "state": "done"}, 2)
	jobs.Inc(Labels{"kind": "answer", "state": "failed"})
	jobs.Add(Labels{"kind": "x"}, -1) // ignored
	h := r.Histogram("codereview_job_duration_seconds", "Job duration.", []float64{1, 10})
	h.Observe(Labels{"kind": "review"}, 0.5)
	h.Observe(Labels{"kind": "review"}, 5)
	h.Observe(Labels{"kind": "review"}, 50)
	r.Gauge("codereview_info", "Build.", func() []Sample { return []Sample{{Labels{"version": `1"2\n`}, 1}} })
	r.Gauge("codereview_queue_pending", "Waiting.", func() []Sample { return []Sample{{nil, 3}} })
	var b strings.Builder
	r.Write(&b)
	want := `# HELP codereview_jobs_total Finished jobs.
# TYPE codereview_jobs_total counter
codereview_jobs_total{kind="answer",state="failed"} 1
codereview_jobs_total{kind="review",state="done"} 3
# HELP codereview_job_duration_seconds Job duration.
# TYPE codereview_job_duration_seconds histogram
codereview_job_duration_seconds_bucket{kind="review",le="1"} 1
codereview_job_duration_seconds_bucket{kind="review",le="10"} 2
codereview_job_duration_seconds_bucket{kind="review",le="+Inf"} 3
codereview_job_duration_seconds_sum{kind="review"} 55.5
codereview_job_duration_seconds_count{kind="review"} 3
# HELP codereview_info Build.
# TYPE codereview_info gauge
codereview_info{version="1\"2\\n"} 1
# HELP codereview_queue_pending Waiting.
# TYPE codereview_queue_pending gauge
codereview_queue_pending 3
`
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
	if jobs.Value(Labels{"kind": "review", "state": "done"}) != 3 {
		t.Error("value")
	}
}
