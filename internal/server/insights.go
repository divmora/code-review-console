package server

import (
	"context"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/dashboards"
	"github.com/divmora/code-review-console/internal/store"
)

// dashboardInsights are a dashboard's numbers from the database, for its
// repositories only (issue #155): the same on every replica, unlike the
// queue (each replica's own).
type dashboardInsights struct {
	Week  periodTotals `json:"week"`
	Month periodTotals `json:"month"`
	// Daily is the last 30 days, one point per day with activity.
	Daily []store.ImpactPoint `json:"daily"`
	// Latest is each repository's latest scores, and Lowest the merge
	// requests with the lowest scores reviewed in the last 30 days. Both
	// are left out when the dashboard hides repository names.
	Latest []store.ScoredPR `json:"latest,omitempty"`
	Lowest []store.ScoredPR `json:"lowest,omitempty"`
}

// periodTotals sum a period: reviews, comments, findings by severity, and
// the average scores (0 without scored reviews).
type periodTotals struct {
	Reviews         int            `json:"reviews"`
	Comments        int            `json:"comments"`
	Findings        map[string]int `json:"findings"`
	Scored          int            `json:"scored"`
	Quality         int            `json:"quality"`
	Security        int            `json:"security"`
	Maintainability int            `json:"maintainability"`
}

func totalsSince(daily []store.ImpactPoint, since time.Time) periodTotals {
	t := periodTotals{Findings: map[string]int{}}
	var q, sec, m int64
	for _, p := range daily {
		if p.Bucket.Before(since) {
			continue
		}
		t.Reviews += p.Reviews
		t.Comments += p.Comments
		t.Scored += p.Scored
		q, sec, m = q+p.QualitySum, sec+p.SecuritySum, m+p.MaintainabilitySum
		for k, v := range p.Findings {
			t.Findings[k] += v
		}
	}
	if t.Scored > 0 {
		t.Quality, t.Security, t.Maintainability = int(q/int64(t.Scored)), int(sec/int64(t.Scored)), int(m/int64(t.Scored))
	}
	return t
}

// insightsCache keeps each dashboard's insights for a minute: pages poll
// every few seconds, and the numbers are daily.
type insightsCache struct {
	mu      sync.Mutex
	entries map[string]insightsEntry
}

type insightsEntry struct {
	at time.Time
	v  *dashboardInsights
}

const insightsTTL = time.Minute

// insights returns d's insights (nil without a database or on error).
func (s *Server) insights(ctx context.Context, d dashboards.Dashboard, scope dashboards.Scope) *dashboardInsights {
	if s.store == nil {
		return nil
	}
	key := d.Slug + "|" + d.UpdatedAt.String()
	s.insightsCache.mu.Lock()
	if e, ok := s.insightsCache.entries[key]; ok && time.Since(e.at) < insightsTTL {
		s.insightsCache.mu.Unlock()
		return e.v
	}
	s.insightsCache.mu.Unlock()

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	f := store.InsightsFilter{From: today.AddDate(0, 0, -29), To: today.AddDate(0, 0, 1), Lowest: 5}
	if !scope.All() {
		repos, err := s.store.ListRepos(ctx)
		if err != nil {
			s.logger.Warn("dashboard insights unavailable", "dashboard", d.Slug, "error", err)
			return nil
		}
		f.Repos = []store.RepoRef{}
		for _, r := range repos {
			if scope.Covers(r.Platform, r.Host, r.Path) {
				f.Repos = append(f.Repos, store.RepoRef{Platform: r.Platform, Host: r.Host, Repo: r.Path})
			}
		}
	}
	in, err := s.store.Insights(ctx, f)
	if err != nil {
		s.logger.Warn("dashboard insights unavailable", "dashboard", d.Slug, "error", err)
		return nil
	}
	v := &dashboardInsights{Week: totalsSince(in.Daily, today.AddDate(0, 0, -6)), Month: totalsSince(in.Daily, f.From), Daily: in.Daily}
	if !d.HideRepos {
		v.Latest, v.Lowest = in.Latest, in.Lowest
	}
	s.insightsCache.mu.Lock()
	if s.insightsCache.entries == nil || len(s.insightsCache.entries) > 200 {
		s.insightsCache.entries = map[string]insightsEntry{}
	}
	s.insightsCache.entries[key] = insightsEntry{at: time.Now(), v: v}
	s.insightsCache.mu.Unlock()
	return v
}
