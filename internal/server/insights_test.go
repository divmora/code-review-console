package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/dashboards"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestDashboardInsights: a dashboard's scores, trend and totals cover only
// its repositories, and per-repository rows are left out when it hides
// names (issue #155).
func TestDashboardInsights(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "s"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	st := storetest.New()
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ctx := context.Background()
	host := "127.0.0.1:1"
	fms, _ := st.CreateRepo(ctx, "gitlab", host, "ifmists/fms", "enabled", "a@x")
	_, _ = st.CreateRepo(ctx, "gitlab", host, "secret/payroll", "enabled", "a@x")
	now := time.Now()
	job := func(repo string, number int, ago time.Duration, q, sec, m int, comments int) {
		t.Helper()
		f := now.Add(-ago)
		res := `{"summary":"s","findings":[{"severity":"critical","category":"security"},{"severity":"minor","category":"style"}]}`
		if _, err := st.InsertJob(ctx, store.Job{Kind: "review", Platform: "gitlab", Host: host, Repo: repo, Number: number, URL: "https://x/" + repo, State: "done",
			Enqueued: f, Started: f, Finished: f, Comments: comments, Scores: &store.Scores{Quality: q, Security: sec, Maintainability: m}, Result: json.RawMessage(res)}); err != nil {
			t.Fatal(err)
		}
	}
	job("ifmists/fms", 1, 20*24*time.Hour, 90, 40, 80, 3) // 30 days only
	job("ifmists/fms", 2, time.Hour, 70, 60, 90, 2)       // this week
	job("ifmists/fms", 2, 30*time.Minute, 72, 62, 92, 1)  // the same MR again: the latest counts for "lowest"
	job("secret/payroll", 9, time.Hour, 10, 10, 10, 9)    // another dashboard's repository

	d, err := s.dashboards.Save(ctx, dashboards.Dashboard{Slug: "fms", Title: "FMS", Access: dashboards.Public, Enabled: true,
		Repos: []store.DashboardRepo{{ID: fms.ID}}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	get := func() (statusResponse, string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/dashboards/fms/status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status: %d %s", w.Code, w.Body)
		}
		var resp statusResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp, w.Body.String()
	}
	resp, body := get()
	in := resp.Insights
	if in == nil || strings.Contains(body, "payroll") {
		t.Fatalf("insights missing, or another repository leaks: %s", body)
	}
	if in.Month.Reviews != 3 || in.Week.Reviews != 2 || in.Month.Comments != 6 || in.Week.Comments != 3 || in.Month.Findings["critical"] != 3 || in.Week.Findings["minor"] != 2 {
		t.Errorf("totals: week %+v month %+v", in.Week, in.Month)
	}
	if in.Week.Quality != 71 || in.Week.Security != 61 || in.Month.Scored != 3 || in.Month.Quality != (90+70+72)/3 {
		t.Errorf("average scores: week %+v month %+v", in.Week, in.Month)
	}
	if len(in.Latest) != 1 || in.Latest[0].Repo != "ifmists/fms" || in.Latest[0].Scores.Quality != 72 {
		t.Errorf("latest: %+v", in.Latest)
	}
	if len(in.Lowest) != 2 || in.Lowest[0].Number != 1 || in.Lowest[1].Scores.Security != 62 {
		t.Errorf("lowest (MR 1's minimum is 40; MR 2's latest review counts): %+v", in.Lowest)
	}
	if len(in.Daily) < 2 {
		t.Errorf("daily: %+v", in.Daily)
	}

	// Hiding names drops the per-repository rows, keeps the totals.
	d.HideRepos = true
	if _, err := s.dashboards.Save(ctx, d, "a@x"); err != nil {
		t.Fatal(err)
	}
	resp, body = get()
	if resp.Insights == nil || resp.Insights.Latest != nil || resp.Insights.Lowest != nil || strings.Contains(body, "ifmists") || resp.Insights.Month.Reviews != 3 {
		t.Errorf("hidden names: %s", body)
	}
}
