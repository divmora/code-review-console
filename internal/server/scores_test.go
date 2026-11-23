package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestReviewScoresFlow: a review's scores are saved with the job, shown on
// the dashboard (unless repository names are hidden) and in /metrics.
func TestReviewScoresFlow(t *testing.T) {
	s := testServer(t)
	st := storetest.New()
	s.store = st
	s.cfg.Server.Dashboard = config.Dashboard{Enabled: true, Token: "dash", History: 10}
	done := make(chan struct{})
	_ = s.queue.Submit(&Job{Key: "sc", Meta: Meta{Kind: "review", Platform: "github", Host: "github.com", Repo: "o/scored", Number: 5},
		Run: func(_ context.Context, rep *Report) error {
			defer close(done)
			rep.Scores = &store.Scores{Quality: 85, Security: 40, Maintainability: 88}
			return nil
		}})
	<-done
	waitFor(t, func() bool { return len(s.queue.Snapshot().Recent) == 1 })
	waitFor(t, func() bool { js, _ := st.RecentJobs(context.Background(), 5); return len(js) == 1 })
	js, _ := st.RecentJobs(context.Background(), 5)
	if js[0].Scores == nil || js[0].Scores.Security != 40 {
		t.Fatalf("the scores are saved with the job: %+v", js[0])
	}

	status := func() statusResponse {
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.Header.Set("Authorization", "Bearer dash")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		var out statusResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return out
	}
	if r := status().Recent; len(r) != 1 || r[0].Scores == nil || r[0].Scores.Quality != 85 {
		t.Errorf("the dashboard shows the scores: %+v", r)
	}
	if samples := s.scoreSamples(); len(samples) != 3 || samples[0].Labels["repo"] != "o/scored" {
		t.Errorf("one series per repository and kind: %+v", samples)
	}

	s.cfg.Server.Dashboard.HideRepos = true
	if r := status().Recent; len(r) != 1 || r[0].Scores != nil {
		t.Errorf("hidden repository names hide the scores too: %+v", r[0].Scores)
	}
	samples := s.scoreSamples()
	if len(samples) != 3 {
		t.Fatalf("one average per kind: %+v", samples)
	}
	for _, x := range samples {
		if _, ok := x.Labels["repo"]; ok || strings.Contains(x.Labels["host"], "github") {
			t.Errorf("no repository labels while names are hidden: %+v", x)
		}
		if x.Labels["kind"] == "security" && x.Value != 40 {
			t.Errorf("average security: %+v", x)
		}
	}
}
