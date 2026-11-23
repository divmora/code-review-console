package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/store/storetest"
	"github.com/divmora/code-review-console/internal/tuning"
)

func TestMetricsEndpoint(t *testing.T) {
	s := testServer(t)
	st := storetest.New()
	s.tuning = tuning.New(s.cfg, st) // a database, so access can be chosen
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	if w := get(""); w.Code != http.StatusNotFound {
		t.Errorf("off by default: %d", w.Code)
	}
	ctx := context.Background()
	base := s.tuning.Get(ctx)
	base.MetricsAccess = tuning.AccessToken
	if _, err := s.tuning.Put(ctx, tuning.Update{Settings: base}, nil, "a@x"); err == nil {
		t.Error("token access needs a token")
	}
	if _, err := s.tuning.Put(ctx, tuning.Update{Settings: base, MetricsToken: "prometheus-scrape-token"}, nil, "a@x"); err != nil {
		t.Fatal(err)
	}
	if w := get(""); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no token: %d", w.Code)
	}
	if w := get("wrong-token-wrong-token"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}

	// A finished job and a webhook delivery move the counters.
	done := make(chan struct{})
	_ = s.queue.Submit(&Job{Key: "m", Meta: Meta{Kind: "full review", Repo: "o/secret-repo", Number: 3}, Run: func(_ context.Context, rep *Report) error {
		defer close(done)
		rep.TokensIn, rep.TokensOut, rep.Comments = 1200, 300, 4
		return nil
	}})
	<-done
	waitFor(t, func() bool {
		return s.metrics.jobs.Value(map[string]string{"kind": "full_review", "state": "done"}) == 1
	})
	_ = s.queue.Submit(&Job{Key: "f", Meta: Meta{Kind: "answer"}, Run: func(context.Context, *Report) error { return errors.New("boom") }})
	waitFor(t, func() bool { return s.metrics.jobs.Value(map[string]string{"kind": "answer", "state": "failed"}) == 1 })
	body := []byte(`{"action":"opened","number":7,"repository":{"full_name":"o/secret-repo"}}`)
	post(t, s.Handler(), "pull_request", body, "sha256=bad")

	w := get("prometheus-scrape-token")
	out := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("metrics: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`codereview_build_info{version="`,
		"codereview_queue_pending 0",
		"codereview_workers 4",
		`codereview_jobs_total{kind="full_review",state="done"} 1`,
		`codereview_jobs_total{kind="answer",state="failed"} 1`,
		`codereview_job_duration_seconds_count{kind="full_review"} 1`,
		`codereview_model_tokens_total{direction="input"} 1200`,
		"codereview_review_comments_total 4",
		`codereview_webhooks_total{outcome="rejected",platform="github"} 1`,
		`codereview_license_state{state="active"} 1`,
		"codereview_license_expiry_timestamp_seconds ",
		"# TYPE codereview_job_duration_seconds histogram",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-repo") {
		t.Error("metrics never carry repository names")
	}

	base = s.tuning.Get(ctx)
	base.MetricsAccess = tuning.AccessPublic
	if _, err := s.tuning.Put(ctx, tuning.Update{Settings: base}, nil, "a@x"); err != nil {
		t.Fatal(err)
	}
	if w := get(""); w.Code != 200 {
		t.Errorf("public: %d", w.Code)
	}
}

func TestHealthzShowsOnlyTheStatus(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Errorf("healthz: %d %s", w.Code, w.Body)
	}
}
