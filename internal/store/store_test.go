package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/divmora/code-review-console/internal/clientip"
	"github.com/divmora/code-review-console/internal/config"
)

// Integration test: set CODEREVIEW_TEST_DATABASE_URL to a disposable
// database, e.g. postgres://postgres:test@127.0.0.1:55432/codereview.
func openTest(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("CODEREVIEW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CODEREVIEW_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	p, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `TRUNCATE repos, jobs, sessions, audit_log, model_profiles, settings, dashboards, dashboard_repos, dashboard_patterns RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestMigrateIsIdempotentAndConcurrent(t *testing.T) {
	p := openTest(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- p.migrate(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var v int
	_ = p.pool.QueryRow(context.Background(), `SELECT max(version) FROM codereview_schema`).Scan(&v)
	if v != len(migrations) {
		t.Errorf("schema version %d, want %d", v, len(migrations))
	}
}

func TestReposAndAudit(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()

	r, err := p.TouchRepo(ctx, "GitHub", "GitHub.com", "/org/api/")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeInherit || r.Platform != "github" || r.Host != "github.com" || r.Path != "org/api" || r.LastSeen == nil {
		t.Fatalf("touched repo = %+v", r)
	}
	again, _ := p.TouchRepo(ctx, "github", "github.com", "org/api")
	if again.ID != r.ID {
		t.Errorf("touch should upsert, got new id %d", again.ID)
	}

	prof := config.Assertive
	maxC := 7
	up, err := p.UpdateRepo(ctx, r.ID, ModeEnabled, config.Overrides{Profile: &prof, MaxComments: &maxC}, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if up.Mode != ModeEnabled || *up.Settings.Profile != config.Assertive || up.UpdatedBy != "alice@example.com" {
		t.Errorf("update = %+v", up)
	}
	found, err := p.FindRepo(ctx, "github", "GITHUB.COM", "org/api")
	if err != nil || found.Mode != ModeEnabled || *found.Settings.MaxComments != 7 {
		t.Errorf("find = %+v %v", found, err)
	}
	if _, err := p.FindRepo(ctx, "gitlab", "gitlab.com", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}

	gl, err := p.CreateRepo(ctx, "gitlab", "gitlab.example.com", "group/sub/proj", ModeEnabled, "bob@example.com")
	if err != nil || gl.Mode != ModeEnabled {
		t.Fatalf("create = %+v %v", gl, err)
	}
	list, _ := p.ListRepos(ctx)
	if len(list) != 2 {
		t.Errorf("list = %d", len(list))
	}
	if err := p.DeleteRepo(ctx, gl.ID, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteRepo(ctx, gl.ID, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}

	_ = p.Audit(ctx, "alice@example.com", "login", "admin", map[string]string{"ip": "10.0.0.1"})
	entries, err := p.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); got != "login,repo.delete,repo.create,repo.update" {
		t.Errorf("audit actions = %s", got)
	}
	var detail struct {
		Before struct{ Mode string } `json:"before"`
		After  struct{ Mode string } `json:"after"`
	}
	_ = json.Unmarshal(entries[3].Detail, &detail)
	if detail.Before.Mode != ModeInherit || detail.After.Mode != ModeEnabled {
		t.Errorf("update audit should record before/after: %s", entries[3].Detail)
	}
}

func TestJobsAndSessions(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 3; i++ {
		_, err := p.InsertJob(ctx, Job{Kind: "review", Platform: "github", Repo: "o/r", Number: i, State: "done",
			Enqueued: now, Started: now, Finished: now.Add(time.Duration(i) * time.Second), Comments: i, TokensIn: 100})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _ = p.InsertJob(ctx, Job{Kind: "help", Platform: "gitlab", Repo: "g/p", State: "failed", Enqueued: now, Finished: now, Error: "boom"})
	jobs, err := p.RecentJobs(ctx, 2)
	// Newest inserted first (jobs are inserted when they finish).
	if err != nil || len(jobs) != 2 || jobs[0].Kind != "help" || jobs[1].Number != 2 {
		t.Fatalf("recent = %+v %v", jobs, err)
	}

	id := sha256.Sum256([]byte("session-secret"))
	if err := p.CreateSession(ctx, id[:], Session{Subject: "s1", Email: "a@x.com", CSRF: "c", Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s, err := p.GetSession(ctx, id[:])
	if err != nil || s.Email != "a@x.com" || s.CSRF != "c" {
		t.Fatalf("session = %+v %v", s, err)
	}
	expired := sha256.Sum256([]byte("old"))
	_ = p.CreateSession(ctx, expired[:], Session{Subject: "s2", Email: "b@x.com", CSRF: "c", Expires: time.Now().Add(-time.Minute)})
	if _, err := p.GetSession(ctx, expired[:]); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session must not load: %v", err)
	}
	_ = p.DeleteSession(ctx, id[:])
	if _, err := p.GetSession(ctx, id[:]); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session must not load: %v", err)
	}
}

func TestJobResultsFiltersModelsSettings(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	now := time.Now()
	id, err := p.InsertJob(ctx, Job{Kind: "review", Platform: "github", Repo: "Org/API", Number: 1, State: "done", Enqueued: now, Finished: now,
		Result: json.RawMessage(`{"summary":"ok","findings":[{"title":"x"}]}`), RequestedBy: "a@x.com"})
	if err != nil || id == 0 {
		t.Fatalf("insert: %d %v", id, err)
	}
	_, _ = p.InsertJob(ctx, Job{Kind: "review", Platform: "gitlab", Repo: "g/p", Number: 2, State: "failed", Enqueued: now, Finished: now})
	j, err := p.GetJob(ctx, id)
	var res struct{ Summary string }
	if err != nil || j.RequestedBy != "a@x.com" || json.Unmarshal(j.Result, &res) != nil || res.Summary != "ok" {
		t.Fatalf("get: %+v %v", j, err)
	}
	if _, err := p.GetJob(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job: %v", err)
	}
	list, _ := p.ListJobs(ctx, JobFilter{Repo: "org/api"})
	if len(list) != 1 || list[0].Result != nil {
		t.Errorf("repo filter (case-insensitive, no result): %+v", list)
	}
	list, _ = p.ListJobs(ctx, JobFilter{State: "failed"})
	if len(list) != 1 || list[0].Platform != "gitlab" {
		t.Errorf("state filter: %+v", list)
	}
	list, _ = p.ListJobs(ctx, JobFilter{BeforeID: id + 1, Limit: 10})
	if len(list) != 1 || list[0].ID != id {
		t.Errorf("pagination: %+v", list)
	}

	err = p.PutModelProfile(ctx, "fast", config.ModelProfile{"provider": "bedrock", "model": "m", "region": "us-east-1", "api_key": "SECRET"}, "a@x.com")
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := p.ListModelProfiles(ctx)
	if len(ms) != 1 || ms[0].Config.String("region") != "us-east-1" || ms[0].Config["api_key"] != nil {
		t.Errorf("model profiles (API key must never be stored): %+v", ms)
	}
	var raw string
	_ = p.pool.QueryRow(ctx, `SELECT config::text FROM model_profiles`).Scan(&raw)
	if strings.Contains(raw, "SECRET") {
		t.Errorf("secret persisted: %s", raw)
	}
	if err := p.DeleteModelProfile(ctx, "fast", "a@x.com"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteModelProfile(ctx, "fast", "a@x.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}

	var v struct{ Enabled bool }
	if found, _ := p.GetSetting(ctx, "public_dashboard", &v); found {
		t.Errorf("unset setting found")
	}
	_ = p.PutSetting(ctx, "public_dashboard", map[string]bool{"enabled": true}, "a@x.com")
	if found, err := p.GetSetting(ctx, "public_dashboard", &v); !found || err != nil || !v.Enabled {
		t.Errorf("setting: %v %v %+v", found, err, v)
	}
	// A value the audit log must not copy (a license key).
	if err := p.PutSettingAudited(ctx, "license", map[string]string{"key": "DIV1.secret"}, map[string]string{"id": "lic-1"}, "a@x.com"); err != nil {
		t.Fatal(err)
	}
	var lic struct{ Key string }
	if found, _ := p.GetSetting(ctx, "license", &lic); !found || lic.Key != "DIV1.secret" {
		t.Errorf("stored value: %+v", lic)
	}
	entries, _ := p.ListAudit(ctx, 5)
	if len(entries) == 0 || entries[0].Target != "license" || strings.Contains(fmt.Sprint(entries[0].Detail), "DIV1.secret") {
		t.Errorf("audit must hold the detail, not the value: %+v", entries[0])
	}
}

func TestRules(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	prof := config.Chill
	on, off := true, false
	r, err := p.CreateRule(ctx, Rule{Platform: "GitLab", Host: "GitLab.com", Pattern: "/Acme/", Enabled: &on, ScoreLabels: &on, Settings: config.Overrides{Profile: &prof}}, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if r.Pattern != "acme" || r.Platform != "gitlab" || r.Host != "gitlab.com" || *r.Settings.Profile != config.Chill {
		t.Fatalf("created rule = %+v", r)
	}
	if r.ScoreLabels == nil || !*r.ScoreLabels || r.RequireReviewer != nil {
		t.Errorf("optional settings must round-trip (unset stays nil): %+v", r)
	}
	if _, err := p.CreateRule(ctx, Rule{Platform: "gitlab", Host: "gitlab.com", Pattern: "*"}, "a"); err == nil {
		t.Error("a lone * must be rejected")
	}
	if h, err := p.CreateRule(ctx, Rule{Platform: "gitlab", Host: "gitlab.com", Pattern: "", RequireReviewer: &on}, "a"); err != nil || h.Pattern != "" || h.Enabled != nil {
		t.Errorf("host-wide rule: %+v %v", h, err)
	} else {
		_ = p.DeleteRule(ctx, h.ID, "a")
	}
	up, err := p.UpdateRule(ctx, r.ID, Rule{Enabled: &off}, "bob@example.com")
	if err != nil || up.Enabled == nil || *up.Enabled || up.ScoreLabels != nil || up.UpdatedBy != "bob@example.com" || up.Settings.Profile != nil {
		t.Fatalf("update = %+v %v", up, err)
	}
	list, err := p.ListRules(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if err := p.DeleteRule(ctx, r.ID, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteRule(ctx, r.ID, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	entries, _ := p.ListAudit(ctx, 10)
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); !strings.HasPrefix(got, "rule.delete,rule.update,rule.delete,rule.create,rule.create") {
		t.Errorf("audit actions = %s", got)
	}
}

func TestWebhookEvents(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	old := time.Now().Add(-30 * 24 * time.Hour)
	err := p.InsertWebhookEvents(ctx, []WebhookEvent{
		{ReceivedAt: old, Platform: "gitlab", Project: "grp/old", Outcome: HookIgnored},
		{Platform: "gitlab", Host: "gl.example.com", Project: "grp/app", Number: 5, Kind: "merge_request", Action: "update", Outcome: HookIgnored, Reason: "merge request action update", Auth: "signature"},
		{Platform: "gitlab", Project: "grp/app", Outcome: HookQueued, Reason: "review"},
		{Platform: "github", Project: "o/r_x", Outcome: HookRejected},
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := p.ListWebhookEvents(ctx, WebhookFilter{})
	if err != nil || len(all) != 4 || all[0].Project != "o/r_x" {
		t.Fatalf("list = %+v %v", all, err)
	}
	if got, _ := p.ListWebhookEvents(ctx, WebhookFilter{Project: "APP", Outcome: HookIgnored}); len(got) != 1 || got[0].Number != 5 || got[0].Auth != "signature" {
		t.Errorf("filter = %+v", got)
	}
	if got, _ := p.ListWebhookEvents(ctx, WebhookFilter{Project: "r_"}); len(got) != 1 {
		t.Errorf("_ must be matched literally: %+v", got)
	}
	if got, _ := p.ListWebhookEvents(ctx, WebhookFilter{BeforeID: all[1].ID}); len(got) != 2 {
		t.Errorf("pagination = %d", len(got))
	}
	counts, _ := p.CountWebhookEvents(ctx, time.Now().Add(-time.Hour))
	if counts[HookIgnored] != 1 || counts[HookQueued] != 1 || counts[HookRejected] != 1 {
		t.Errorf("counts = %v", counts)
	}
	n, err := p.PruneWebhookEvents(ctx, time.Now().Add(-14*24*time.Hour), 2)
	if err != nil || n != 2 {
		t.Fatalf("prune removed %d (want the old one plus one over the cap): %v", n, err)
	}
	if left, _ := p.ListWebhookEvents(ctx, WebhookFilter{}); len(left) != 2 || left[0].Project != "o/r_x" {
		t.Errorf("prune should keep the newest: %+v", left)
	}
}

func TestJobMetricsAndPrune(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	add := func(at time.Time, state string, comments int) {
		if _, err := p.InsertJob(ctx, Job{Kind: "review", Platform: "gitlab", Host: "gl.example.com", Repo: "Acme/API", Number: 1,
			State: state, Enqueued: at, Started: at.Add(-time.Minute), Finished: at, Comments: comments, TokensIn: 100, TokensOut: 10}); err != nil {
			t.Fatal(err)
		}
	}
	add(day, "done", 3)
	add(day.Add(10*time.Minute), "failed", 0)
	add(day.Add(2*time.Hour), "skipped", 0)
	add(day.AddDate(0, 0, 1), "done", 1)
	daily, err := p.Metrics(ctx, MetricsFilter{Granularity: Daily, From: day.AddDate(0, 0, -1), To: day.AddDate(0, 0, 3)})
	if err != nil || len(daily) != 2 {
		t.Fatalf("daily = %+v %v", daily, err)
	}
	d0 := daily[0]
	if d0.Jobs != 3 || d0.Done != 1 || d0.Failed != 1 || d0.Skipped != 1 || d0.Comments != 3 || d0.TokensIn != 300 || d0.DurationMS != 180000 {
		t.Errorf("first day rollup = %+v", d0)
	}
	hourly, _ := p.Metrics(ctx, MetricsFilter{Granularity: Hourly, From: day.Add(-time.Hour), To: day.Add(3 * time.Hour), Repo: "acme/api"})
	if len(hourly) != 2 || hourly[0].Jobs != 2 {
		t.Errorf("hourly, filtered by repo (case-insensitive) = %+v", hourly)
	}
	monthly, _ := p.Metrics(ctx, MetricsFilter{Granularity: Monthly, From: day.AddDate(0, -1, 0), To: day.AddDate(0, 1, 0), Host: "other"})
	if len(monthly) != 0 {
		t.Errorf("host filter: %+v", monthly)
	}
	if _, err := p.Metrics(ctx, MetricsFilter{Granularity: "week"}); err == nil {
		t.Error("unknown granularity must fail")
	}
	got, _ := p.ListJobs(ctx, JobFilter{Host: "GL.example.com", RepoLike: "api", State: "done", From: day.Add(time.Hour)})
	if len(got) != 1 || got[0].Comments != 1 {
		t.Errorf("filtered jobs = %+v", got)
	}
	n, err := p.PruneJobs(ctx, day.Add(time.Hour), day.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("prune: %d %v", n, err)
	}
	daily, _ = p.Metrics(ctx, MetricsFilter{Granularity: Daily, From: day.AddDate(0, 0, -1), To: day.AddDate(0, 0, 3)})
	if len(daily) != 2 || daily[0].Jobs != 3 {
		t.Errorf("daily rollups survive pruning: %+v", daily)
	}
	hourly, _ = p.Metrics(ctx, MetricsFilter{Granularity: Hourly, From: day.Add(-time.Hour), To: day.Add(3 * time.Hour)})
	if len(hourly) != 1 {
		t.Errorf("hourly rollups older than the cut are pruned: %+v", hourly)
	}
}

func TestLearnings(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	l, err := p.CreateLearning(ctx, Learning{Platform: "GitLab", Host: "GL.example.com", Scope: "acme/pay", Text: "Zeros are dropped on purpose.",
		Status: LearningActive, Source: "reply", SourceRef: "gitlab|gl|note|1", SourceAuthor: "dev"}, "bot")
	if err != nil || l.Platform != "gitlab" || l.Host != "gl.example.com" || l.ID == 0 {
		t.Fatalf("create: %+v %v", l, err)
	}
	if _, err := p.CreateLearning(ctx, Learning{Platform: "gitlab", Host: "gl.example.com", Text: "again", Status: LearningActive, SourceRef: "gitlab|gl|note|1"}, "bot"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same source comment twice: %v", err)
	}
	if _, err := p.CreateLearning(ctx, Learning{Platform: "gitlab", Host: "gl.example.com", Text: "manual one", Status: LearningActive}, "a@x"); err != nil {
		t.Errorf("manual learnings have no source ref: %v", err)
	}
	up, err := p.UpdateLearning(ctx, Learning{ID: l.ID, Scope: "acme", Text: "Zeros are dropped on purpose everywhere.", Status: LearningDisabled}, "a@x")
	if err != nil || up.Scope != "acme" || up.Status != LearningDisabled || up.Source != "reply" {
		t.Fatalf("update: %+v %v", up, err)
	}
	if _, err := p.UpdateLearning(ctx, Learning{ID: l.ID, Status: "bogus", Text: "x"}, "a"); err == nil {
		t.Error("invalid status must be rejected by the database")
	}
	list, _ := p.ListLearnings(ctx)
	if len(list) != 2 {
		t.Errorf("list = %d", len(list))
	}
	if err := p.DeleteLearning(ctx, l.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteLearning(ctx, l.ID, "a@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestTokensUsed(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	now := time.Now()
	before, _ := p.TokensUsed(ctx, now, "", "", "") // other tests may have run jobs today
	for _, j := range []Job{
		{Platform: "gitlab", Host: "gl", Repo: "g/a", TokensIn: 100, TokensOut: 10, Finished: now},
		{Platform: "gitlab", Host: "gl", Repo: "G/A", TokensIn: 50, TokensOut: 5, Finished: now},
		{Platform: "gitlab", Host: "gl", Repo: "g/b", TokensIn: 1000, Finished: now},
		{Platform: "gitlab", Host: "gl", Repo: "g/a", TokensIn: 9999, Finished: now.AddDate(0, 0, -2)},
	} {
		j.Kind, j.State, j.Enqueued = "review", "done", j.Finished
		if _, err := p.InsertJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := p.TokensUsed(ctx, now, "gitlab", "GL", "g/a"); err != nil || n != 165 {
		t.Errorf("repo today = %d %v", n, err)
	}
	if n, _ := p.TokensUsed(ctx, now, "", "", ""); n-before != 1165 {
		t.Errorf("all today = %d", n)
	}
}

func TestDashboardsStore(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	host := fmt.Sprintf("dash-%d.example.com", time.Now().UnixNano())
	r1, err := p.CreateRepo(ctx, "gitlab", host, "ifmists/fms", "enabled", "a@x")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := p.CreateRepo(ctx, "gitlab", host, "ifmists/common-lib", "enabled", "a@x")
	slug := fmt.Sprintf("fms-%d", time.Now().UnixNano())
	exp := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	d, err := p.PutDashboard(ctx, Dashboard{Slug: slug, Title: "FMS", Access: "token", Enabled: true, TokenHash: "abc123hash", TokenExpires: exp, TokenChange: "set",
		Repos: []DashboardRepo{{ID: r1.ID}, {ID: r2.ID}}, Patterns: []DashboardPattern{{Platform: "gitlab", Host: host, Pattern: "ifmists/fms"}}}, "a@x")
	if err != nil || d.ID == 0 || len(d.Repos) != 2 || len(d.Patterns) != 1 || !d.TokenExpires.Equal(exp) || d.Repos[0].Path != "ifmists/common-lib" {
		t.Fatalf("create: %+v %v", d, err)
	}
	if _, err := p.PutDashboard(ctx, Dashboard{Slug: slug, Title: "x", Access: "admins"}, "a@x"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate slug: %v", err)
	}
	d.Title, d.Repos, d.TokenChange = "FMS team", []DashboardRepo{{ID: r1.ID}, {ID: 999999999}}, ""
	up, err := p.PutDashboard(ctx, *d, "b@x")
	if err != nil || up.Title != "FMS team" || len(up.Repos) != 1 || up.TokenHash != "abc123hash" || up.CreatedBy != "a@x" || up.UpdatedBy != "b@x" {
		t.Fatalf("update (an unknown repository is skipped): %+v %v", up, err)
	}
	// Deleting a repository detaches it.
	if err := p.DeleteRepo(ctx, r1.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	list, _ := p.ListDashboards(ctx)
	for _, x := range list {
		if x.ID == d.ID && len(x.Repos) != 0 {
			t.Errorf("deleted repository still attached: %+v", x.Repos)
		}
	}
	entries, _ := p.ListAudit(ctx, 30)
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Target == slug {
			seen[e.Action] = true
		}
		if strings.Contains(string(e.Detail), "abc123hash") {
			t.Errorf("the audit log has the token hash: %s", e.Detail)
		}
	}
	if !seen["dashboard.create"] || !seen["dashboard.update"] || !seen["dashboard.token.rotate"] {
		t.Errorf("audit: %v", seen)
	}
	if err := p.DeleteDashboard(ctx, d.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateRepo(ctx, "gitlab", host, "ifmists/after", "enabled", "a@x"); err != nil {
		t.Errorf("repositories are unaffected by deleting a dashboard: %v", err)
	}
}

func TestHostCredentialsStore(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	h, err := p.PutHost(ctx, HostRow{Platform: "gitlab", Host: fmt.Sprintf("cred-%d.example.com", time.Now().UnixNano()), BaseURL: "https://gl.example.com"}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	v0, _ := p.HostsVersion(ctx)
	c, err := p.PutHostCredential(ctx, HostCredential{HostID: h.ID, Name: "Acme", Pattern: "acme", Enabled: true, Config: json.RawMessage(`{"bot_name":"acme-bot"}`),
		Secrets: "v1:sealed", SecretsChanged: []string{"token:set"}}, "a@x")
	if err != nil || c.ID == 0 || c.CreatedBy != "a@x" {
		t.Fatalf("create: %+v %v", c, err)
	}
	if v1, _ := p.HostsVersion(ctx); v1 <= v0 {
		t.Errorf("a credential write bumps the hosts version: %d -> %d", v0, v1)
	}
	if _, err := p.PutHostCredential(ctx, HostCredential{HostID: h.ID, Pattern: "acme", Secrets: "x"}, "a@x"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("same pattern twice: %v", err)
	}
	if _, err := p.PutHostCredential(ctx, HostCredential{HostID: 0, Pattern: "x", Secrets: "x"}, "a@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}
	c.Name, c.Enabled, c.SecretsChanged = "Acme group", false, nil
	up, err := p.PutHostCredential(ctx, *c, "b@x")
	if err != nil || up.Name != "Acme group" || up.Enabled || up.UpdatedBy != "b@x" || up.CreatedBy != "a@x" {
		t.Fatalf("update: %+v %v", up, err)
	}
	list, _ := p.ListHostCredentials(ctx)
	found := false
	for _, x := range list {
		if x.ID == c.ID {
			found = x.Secrets == "v1:sealed" && string(x.Config) == `{"bot_name": "acme-bot"}`
		}
	}
	if !found {
		t.Errorf("list: %+v", list)
	}
	entries, _ := p.ListAudit(ctx, 20)
	var put bool
	for _, e := range entries {
		if e.Action == "credential.put" && e.Target == fmt.Sprint(c.ID) && strings.Contains(string(e.Detail), "token:set") {
			put = true
		}
		if strings.Contains(string(e.Detail), "v1:sealed") {
			t.Errorf("the audit log has the sealed secret: %s", e.Detail)
		}
	}
	if !put {
		t.Errorf("credential.put audited with secrets_changed: %+v", entries)
	}
	// Deleting the host deletes its credentials.
	if err := p.DeleteHost(ctx, h.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteHostCredential(ctx, c.ID, "a@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("credential should be gone with its host: %v", err)
	}
}

func TestWebhookEndpointsStore(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	h, err := p.PutHost(ctx, HostRow{Platform: "gitlab", Host: fmt.Sprintf("ep-%d.example.com", time.Now().UnixNano()), BaseURL: "https://gl.example.com"}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.PutHost(ctx, HostRow{Platform: "github", Host: fmt.Sprintf("ep-%d.example.org", time.Now().UnixNano()), BaseURL: "https://ghe.example.org/api/v3"}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	e, err := p.PutWebhookEndpoint(ctx, WebhookEndpoint{ID: id, HostID: h.ID, Name: "group a", Enabled: true, Secrets: "v1:sealed"}, "a@x")
	if err != nil || e.ID != id || e.CreatedBy != "a@x" || e.Secrets != "v1:sealed" {
		t.Fatalf("create: %+v %v", e, err)
	}
	if e, err = p.PutWebhookEndpoint(ctx, WebhookEndpoint{ID: id, HostID: h.ID, Name: "group b", Enabled: false, Secrets: "v1:other"}, "b@x"); err != nil ||
		e.Name != "group b" || e.Enabled || e.CreatedBy != "a@x" || e.UpdatedBy != "b@x" {
		t.Fatalf("update: %+v %v", e, err)
	}
	if _, err := p.PutWebhookEndpoint(ctx, WebhookEndpoint{ID: id, HostID: other.ID, Name: "moved", Secrets: "v1:x"}, "c@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an endpoint cannot move to another host: %v", err)
	}
	list, _ := p.ListWebhookEndpoints(ctx)
	found := false
	for _, x := range list {
		found = found || (x.ID == id && x.Name == "group b")
	}
	if !found {
		t.Errorf("list: %+v", list)
	}
	if err := p.InsertWebhookEvents(ctx, []WebhookEvent{{Platform: "gitlab", Project: "ep/test", Endpoint: id, Outcome: HookQueued}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.ListWebhookEvents(ctx, WebhookFilter{Project: "ep/test"}); len(got) != 1 || got[0].Endpoint != id {
		t.Errorf("webhook event endpoint: %+v", got)
	}
	// Deleting the host deletes its endpoints.
	if err := p.DeleteHost(ctx, h.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteWebhookEndpoint(ctx, id, "a@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("endpoint should be gone with its host: %v", err)
	}
	id2 := uuid.NewString()
	if _, err := p.PutWebhookEndpoint(ctx, WebhookEndpoint{ID: id2, HostID: other.ID, Name: "x", Secrets: "v1:x"}, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteWebhookEndpoint(ctx, id2, "a@x"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := p.DeleteWebhookEndpoint(ctx, "not-a-uuid", "a@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a malformed ID is just not found: %v", err)
	}
}

func TestHookStats(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	host := fmt.Sprintf("stats-%d.example.com", time.Now().UnixNano())
	if err := p.InsertWebhookEvents(ctx, []WebhookEvent{
		{Platform: "gitlab", Host: host, Event: "System Hook", Kind: "merge_request", Auth: "signature"},
		{Platform: "gitlab", Host: host, Event: "Note Hook", Kind: "note", Auth: "token"},
		{Platform: "gitlab", Host: host, Event: "Note Hook", Kind: "note", Auth: "failed"},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := p.HookStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Host == host {
			if s.Verified != 2 || s.Comments != 1 || s.SystemHook != 1 {
				t.Errorf("stats = %+v", s)
			}
			return
		}
	}
	t.Errorf("host missing from %+v", list)
}

func TestEndpointHookStats(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	ep := uuid.NewString()
	old := time.Now().Add(-48 * time.Hour)
	if err := p.InsertWebhookEvents(ctx, []WebhookEvent{
		{Platform: "gitlab", Host: "h", Endpoint: ep, Auth: "signature", Outcome: HookQueued, ReceivedAt: old},
		{Platform: "gitlab", Host: "h", Endpoint: ep, Auth: "signature", Outcome: HookIgnored},
		{Platform: "gitlab", Host: "h", Endpoint: ep, Auth: "failed", Outcome: HookRejected, Reason: "invalid token"},
		{Platform: "gitlab", Host: "h", Endpoint: ep, Auth: "signature", Outcome: HookError},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := p.EndpointHookStats(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Endpoint == ep {
			if s.Accepted != 1 || s.Rejected != 2 || s.Last == nil || s.Last.Outcome != HookError {
				t.Errorf("stats = %+v (last %+v)", s, s.Last)
			}
			events, err := p.ListWebhookEvents(ctx, WebhookFilter{Endpoint: ep})
			if err != nil || len(events) != 4 {
				t.Errorf("filtered by endpoint: %d, %v", len(events), err)
			}
			return
		}
	}
	t.Errorf("endpoint missing from %+v", list)
}

// TestConnectDoesNotMigrate: check-config connects without migrating, so a
// newer binary never changes the schema under a serving console.
func TestConnectDoesNotMigrate(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	var before int
	_ = p.pool.QueryRow(ctx, `SELECT max(version) FROM codereview_schema`).Scan(&before)
	if _, err := p.pool.Exec(ctx, `DELETE FROM codereview_schema WHERE version = $1`, before); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = p.pool.Exec(ctx, `INSERT INTO codereview_schema (version) VALUES ($1) ON CONFLICT DO NOTHING`, before)
	})
	_ = p.PutSetting(ctx, "probe", map[string]int{"n": 1}, "a@x")

	c, err := Connect(ctx, os.Getenv("CODEREVIEW_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var after int
	_ = c.pool.QueryRow(ctx, `SELECT max(version) FROM codereview_schema`).Scan(&after)
	if after != before-1 {
		t.Errorf("Connect must not migrate: schema version %d, want %d", after, before-1)
	}
	var v map[string]int
	if found, err := c.GetSetting(ctx, "probe", &v); !found || err != nil || v["n"] != 1 {
		t.Errorf("Connect reads: %v %v %v", found, err, v)
	}
}

func TestSessionClaimsListAndRevoke(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	h := func(s string) []byte { sum := sha256.Sum256([]byte(s)); return sum[:] }
	for _, s := range []struct {
		key    string
		email  string
		groups []string
	}{{"a", "a@x.com", []string{"admins", "ops"}}, {"b", "b@x.com", nil}, {"c", "c@x.com", nil}} {
		if err := p.CreateSession(ctx, h(s.key), Session{Subject: s.key, Email: s.email, Groups: s.groups, Verified: s.key != "c", CSRF: "x", Expires: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := p.GetSession(ctx, h("a"))
	if err != nil || a.ID == 0 || strings.Join(a.Groups, ",") != "admins,ops" || !a.Verified || a.Created.IsZero() {
		t.Fatalf("claims kept: %+v %v", a, err)
	}
	if c, _ := p.GetSession(ctx, h("c")); c.Verified {
		t.Error("verified=false is kept")
	}
	list, err := p.ListSessions(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	b, _ := p.GetSession(ctx, h("b"))
	if gone, err := p.DeleteSessionByID(ctx, b.ID); err != nil || gone.Email != "b@x.com" {
		t.Errorf("revoke: %+v %v", gone, err)
	}
	if _, err := p.DeleteSessionByID(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked twice: %v", err)
	}
	if n, err := p.DeleteOtherSessions(ctx, h("a")); err != nil || n != 1 {
		t.Errorf("end others: %d %v", n, err)
	}
	if list, _ := p.ListSessions(ctx); len(list) != 1 || list[0].Email != "a@x.com" {
		t.Errorf("only a is left: %+v", list)
	}

	// A session from before the migration has no groups (NULL).
	if _, err := p.pool.Exec(ctx, `UPDATE sessions SET groups = NULL WHERE id_hash = $1`, h("a")); err != nil {
		t.Fatal(err)
	}
	if a, err := p.GetSession(ctx, h("a")); err != nil || len(a.Groups) != 0 {
		t.Errorf("legacy session: %+v %v", a, err)
	}
}

func TestMarkSeen(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	_, _ = p.pool.Exec(ctx, `TRUNCATE webhook_seen`)
	if first, err := p.MarkSeen(ctx, "k1", time.Hour); err != nil || !first {
		t.Fatalf("new key: %v %v", first, err)
	}
	if first, _ := p.MarkSeen(ctx, "k1", time.Hour); first {
		t.Error("a key seen within ttl is not first")
	}
	// Recorded longer ago than ttl: first again.
	_, _ = p.pool.Exec(ctx, `UPDATE webhook_seen SET seen_at = now() - interval '2 hours' WHERE key = 'k1'`)
	if first, _ := p.MarkSeen(ctx, "k1", time.Hour); !first {
		t.Error("an expired key is first again")
	}
	// Two consoles racing for one key: exactly one wins.
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first, err := p.MarkSeen(ctx, "race", time.Hour)
			wins <- err == nil && first
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Errorf("one winner, got %d", n)
	}
	_, _ = p.pool.Exec(ctx, `UPDATE webhook_seen SET seen_at = now() - interval '8 days' WHERE key = 'race'`)
	if n, err := p.PruneSeen(ctx, time.Now().Add(-7*24*time.Hour)); err != nil || n != 1 {
		t.Errorf("prune: %d %v", n, err)
	}
}

func TestClientRecorded(t *testing.T) {
	p := openTest(t)
	ctx := clientip.With(context.Background(), clientip.Client{IP: "198.51.100.7", UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0"})
	if err := p.Audit(ctx, "a@x", "login", "admin", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := p.PutSetting(ctx, "k", 1, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.Audit(context.Background(), "console", "seed", "x", nil); err != nil {
		t.Fatal(err)
	}
	list, err := p.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].IP != "" || list[1].IP != "198.51.100.7" || list[2].UserAgent == "" {
		t.Errorf("audit entries carry the request's client: %+v", list)
	}

	hash := []byte("session-hash-for-client-test")
	if err := p.CreateSession(ctx, hash, Session{Subject: "s", Email: "a@x", CSRF: "c", Expires: time.Now().Add(time.Hour), IP: "198.51.100.7", UserAgent: "UA"}); err != nil {
		t.Fatal(err)
	}
	s, err := p.GetSession(ctx, hash)
	if err != nil || s.IP != "198.51.100.7" || s.UserAgent != "UA" || s.LastSeen.IsZero() {
		t.Fatalf("session: %+v %v", s, err)
	}
	// Touched at most once a minute: a fresh session isn't rewritten.
	if err := p.TouchSession(ctx, hash, "203.0.113.5"); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.GetSession(ctx, hash); s.IP != "198.51.100.7" {
		t.Errorf("touch within a minute is skipped: %s", s.IP)
	}
	if _, err := p.pool.Exec(ctx, `UPDATE sessions SET last_seen = now() - interval '5 minutes' WHERE id_hash = $1`, hash); err != nil {
		t.Fatal(err)
	}
	if err := p.TouchSession(ctx, hash, "203.0.113.5"); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.GetSession(ctx, hash); s.IP != "203.0.113.5" || time.Since(s.LastSeen) > time.Minute {
		t.Errorf("touch updates the IP and time: %+v", s)
	}

	if err := p.InsertWebhookEvents(ctx, []WebhookEvent{{Platform: "gitlab", Host: "g", Outcome: "rejected", SourceIP: "203.0.113.9"}}); err != nil {
		t.Fatal(err)
	}
	ev, err := p.ListWebhookEvents(ctx, WebhookFilter{Outcome: "rejected"})
	if err != nil || len(ev) == 0 || ev[0].SourceIP != "203.0.113.9" {
		t.Errorf("webhook source: %+v %v", ev, err)
	}
}

func TestAuditBeforeAfterAndSearch(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `TRUNCATE hosts, webhook_endpoints CASCADE`); err != nil {
		t.Fatal(err)
	}
	h, err := p.PutHost(ctx, HostRow{Platform: "gitlab", Host: "g.example", BaseURL: "https://g.example", Name: "Old", Enabled: true, Secrets: "x", SecretsChanged: []string{"token:set"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PutHost(ctx, HostRow{Platform: "gitlab", Host: "g.example", BaseURL: "https://g.example", Name: "New", Enabled: true, Secrets: "y", SecretsChanged: []string{"signing_tokens:+1"}}, "b@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.PutSetting(ctx, "console", map[string]any{"allowlist": true}, "a@x"); err != nil {
		t.Fatal(err)
	}
	if err := p.PutSetting(ctx, "console", map[string]any{"allowlist": false}, "b@x"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = p.Audit(ctx, "c@x", "login", "admin", map[string]any{"n": i})
	}
	hostPuts, err := p.SearchAudit(ctx, AuditFilter{Action: "host.*"})
	if err != nil || len(hostPuts) != 2 {
		t.Fatalf("host.*: %v %v", hostPuts, err)
	}
	var d struct {
		Before  map[string]any `json:"before"`
		After   map[string]any `json:"after"`
		Secrets []string       `json:"secrets_changed"`
	}
	_ = json.Unmarshal(hostPuts[0].Detail, &d)
	if d.Before["name"] != "Old" || d.After["name"] != "New" || len(d.Secrets) != 1 || d.Secrets[0] != "signing_tokens:+1" {
		t.Errorf("host.put before/after: %s", hostPuts[0].Detail)
	}
	if strings.Contains(string(hostPuts[0].Detail), `"y"`) {
		t.Error("never the secrets")
	}
	_ = json.Unmarshal(hostPuts[1].Detail, &d)
	if d.Before != nil {
		t.Errorf("a new host has no before: %s", hostPuts[1].Detail)
	}
	sets, _ := p.SearchAudit(ctx, AuditFilter{Action: "setting.put", Target: "console"})
	var sd struct{ Before, After map[string]any }
	if len(sets) == 2 {
		_ = json.Unmarshal(sets[0].Detail, &sd)
	}
	if len(sets) != 2 || sd.Before["allowlist"] != true || sd.After["allowlist"] != false {
		t.Errorf("setting.put before/after: %s", sets[0].Detail)
	}
	if got, _ := p.SearchAudit(ctx, AuditFilter{Actor: "B@X"}); len(got) != 2 {
		t.Errorf("actor, case-insensitive: %d", len(got))
	}
	if got, _ := p.SearchAudit(ctx, AuditFilter{Query: "signing_tokens"}); len(got) != 1 {
		t.Errorf("search in details: %d", len(got))
	}
	if got, _ := p.SearchAudit(ctx, AuditFilter{Query: "100%_"}); len(got) != 0 {
		t.Errorf("LIKE wildcards are literal: %d", len(got))
	}
	page1, _ := p.SearchAudit(ctx, AuditFilter{Action: "login", Limit: 3})
	page2, _ := p.SearchAudit(ctx, AuditFilter{Action: "login", Limit: 3, BeforeID: page1[2].ID})
	if len(page1) != 3 || len(page2) != 2 || page2[0].ID >= page1[2].ID {
		t.Errorf("paging: %d then %d", len(page1), len(page2))
	}
	if got, _ := p.SearchAudit(ctx, AuditFilter{To: time.Now().Add(-time.Hour)}); len(got) != 0 {
		t.Errorf("date range: %d", len(got))
	}
	_ = h
}

// Issue #126: a request is claimed once, its body kept while it's
// applied, and only an applying request can be finished.
func TestClaimChangeRequest(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	c, err := p.CreateChangeRequest(ctx, ChangeRequest{Method: "PUT", Path: "/admin/api/settings", Summary: "s", Body: "sealed", RequestedBy: "op@x"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var won atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := p.ClaimChangeRequest(ctx, c.ID, "admin@x"); err == nil && got.Status == ChangeApplying && got.Body == "sealed" {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("claimed %d times", won.Load())
	}
	if _, err := p.DecideChangeRequest(ctx, c.ID, ChangeCancelled, "op@x", 0, "cancelled"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no cancel while applying: %v", err)
	}
	if _, err := p.FinishChangeRequest(ctx, 999999, ChangeApplied, "admin@x", 200, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("finish needs an applying request: %v", err)
	}
	d, err := p.FinishChangeRequest(ctx, c.ID, ChangeApplied, "admin@x", 200, "applied (200)")
	if err != nil || d.Status != ChangeApplied || d.Body != "" {
		t.Fatalf("finish: %v %+v", err, d)
	}
	if _, err := p.FinishChangeRequest(ctx, c.ID, ChangeFailed, "admin@x", 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("finished once: %v", err)
	}
	if got, _ := p.SearchAudit(ctx, AuditFilter{Action: "change.applied"}); len(got) != 1 {
		t.Errorf("audited once: %d", len(got))
	}
}

func TestChangeRequests(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `TRUNCATE change_requests`); err != nil {
		t.Fatal(err)
	}
	c, err := p.CreateChangeRequest(ctx, ChangeRequest{Method: "PUT", Path: "/admin/api/settings", Summary: "Change settings", Preview: json.RawMessage(`{"allowlist":true}`), Body: "sealed", RequestedBy: "op@x"})
	if err != nil || c.Status != ChangePending || c.ID == 0 {
		t.Fatalf("create: %+v %v", c, err)
	}
	got, _ := p.GetChangeRequest(ctx, c.ID)
	if got.Body != "sealed" || got.Summary != "Change settings" {
		t.Errorf("get: %+v", got)
	}
	d, err := p.DecideChangeRequest(ctx, c.ID, ChangeApplied, "admin@x", 200, "applied (200)")
	if err != nil || d.Status != ChangeApplied || d.Body != "" || d.DecidedAt.IsZero() {
		t.Fatalf("decide clears the body: %+v %v", d, err)
	}
	if _, err := p.DecideChangeRequest(ctx, c.ID, ChangeRejected, "admin@x", 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("decided once: %v", err)
	}
	if l, _ := p.ListChangeRequests(ctx, ChangePending, 10); len(l) != 0 {
		t.Errorf("pending: %v", l)
	}
	if l, _ := p.ListChangeRequests(ctx, "", 10); len(l) != 1 {
		t.Errorf("all: %v", l)
	}
	if a, _ := p.SearchAudit(ctx, AuditFilter{Action: "change.*"}); len(a) != 2 {
		t.Errorf("audited: %d", len(a))
	}
}

func TestQueuedJobsAndClaims(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `TRUNCATE queued_jobs, queue_owners`); err != nil {
		t.Fatal(err)
	}
	pay := json.RawMessage(`{"kind":"review"}`)
	_ = p.HeartbeatOwner(ctx, "alive")
	for _, q := range []QueuedJob{
		{ID: "a1", Key: "review:a", Owner: "alive", Kind: "review", Payload: pay, ReadyAt: time.Now()},
		{ID: "g1", Key: "review:g", Owner: "gone", Kind: "review", Payload: pay, ReadyAt: time.Now()},
		{ID: "g2", Key: "answer:g", Owner: "gone", Kind: "answer", Payload: pay, ReadyAt: time.Now()},
	} {
		if err := p.PutQueued(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.StartQueued(ctx, "g2"); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateQueued(ctx, "g1", json.RawMessage(`{"kind":"review","full":true}`), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateQueued(ctx, "missing", pay, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = p.HeartbeatOwner(ctx, "me")
	got, err := p.ClaimQueued(ctx, "me", 90*time.Second)
	if err != nil || len(got) != 2 {
		t.Fatalf("claims the stopped replica's jobs only: %+v %v", got, err)
	}
	for _, q := range got {
		if q.Owner != "me" || (q.ID == "g2") == q.StartedAt.IsZero() {
			t.Errorf("claimed: %+v", q)
		}
		if q.ID == "g1" && !strings.Contains(string(q.Payload), `"full": true`) && !strings.Contains(string(q.Payload), `"full":true`) {
			t.Errorf("update kept: %s", q.Payload)
		}
	}
	if again, _ := p.ClaimQueued(ctx, "other", 90*time.Second); len(again) != 0 {
		t.Errorf("a live replica's jobs (now mine) aren't claimed twice: %+v", again)
	}
	_ = p.RemoveOwner(ctx, "me")
	if handed, _ := p.ClaimQueued(ctx, "next", 90*time.Second); len(handed) != 2 {
		t.Errorf("a stopped replica hands its jobs over at once: %+v", handed)
	}
	_ = p.DeleteQueued(ctx, "g1")
	var n int
	_ = p.pool.QueryRow(ctx, `SELECT count(*) FROM queued_jobs`).Scan(&n)
	if n != 2 {
		t.Errorf("deleted: %d left", n)
	}
	var cnt int
	if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM queue_owners WHERE owner = 'missing'`).Scan(&cnt); err != nil || cnt != 0 {
		t.Errorf("owners: %d %v", cnt, err)
	}
}

func TestReviewScores(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	add := func(repo string, n int, at time.Time, sc *Scores) {
		t.Helper()
		if _, err := p.InsertJob(ctx, Job{Kind: "review", Platform: "gitlab", Host: "scores.example.com", Repo: repo, Number: n, State: "done",
			Enqueued: at, Started: at, Finished: at, Scores: sc}); err != nil {
			t.Fatal(err)
		}
	}
	add("Team/App", 1, at.AddDate(0, 0, -1), &Scores{Quality: 70, Security: 90, Maintainability: 80})
	add("Team/App", 2, at, &Scores{Quality: 90, Security: 50, Maintainability: 60})
	add("Team/App", 3, at, nil) // an answer, or an older CLI: no scores
	add("team/lib", 4, at, &Scores{Quality: 95, Security: 99, Maintainability: 97})

	jobs, err := p.ListJobs(ctx, JobFilter{Host: "scores.example.com"})
	byNum := map[int]*Scores{}
	for _, j := range jobs {
		byNum[j.Number] = j.Scores
	}
	if err != nil || len(jobs) != 4 || byNum[2] == nil || *byNum[2] != (Scores{Quality: 90, Security: 50, Maintainability: 60}) || byNum[3] != nil {
		t.Fatalf("scores are stored per job (nil, not zeros, without them): %v %+v", err, byNum)
	}
	low, _ := p.ListJobs(ctx, JobFilter{Host: "scores.example.com", ScoreKind: "security", ScoreBelow: 60})
	if len(low) != 1 || low[0].Number != 2 {
		t.Errorf("security below 60: %+v", low)
	}
	anyLow, _ := p.ListJobs(ctx, JobFilter{Host: "scores.example.com", ScoreBelow: 75})
	if len(anyLow) != 2 {
		t.Errorf("any score below 75: %+v", anyLow)
	}

	// The approval outcome is read from the result and rolled up (issue #115).
	for i, a := range []string{"approved", "already_approved", "withdrawn"} {
		if _, err := p.InsertJob(ctx, Job{Kind: "review", Platform: "gitlab", Host: "approvals.example.com", Repo: "g/a", Number: 10 + i, State: "done",
			Enqueued: at, Started: at, Finished: at, Result: json.RawMessage(`{"approval":"` + a + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := p.ListJobs(ctx, JobFilter{Host: "approvals.example.com", Approval: "approved"}); len(got) != 1 || got[0].Approval != "approved" || got[0].Number != 10 {
		t.Errorf("approval filter: %+v", got)
	}
	if im, err := p.Impact(ctx, ImpactFilter{Granularity: Daily, From: at.Add(-48 * time.Hour), To: at.Add(48 * time.Hour)}); err != nil || im.Approvals != 1 {
		t.Errorf("approvals counted once, not for already_approved: %v %+v", err, im)
	}

	// Findings filters, as Impact counts them (questions aren't findings).
	res := json.RawMessage(`{"findings":[{"severity":"Critical","category":" Security "},{"severity":"minor","category":"style","kind":"question"}]}`)
	if _, err := p.InsertJob(ctx, Job{Kind: "review", Platform: "gitlab", Host: "findings.example.com", Repo: "g/a", Number: 1, State: "done",
		Enqueued: at, Started: at, Finished: at, Result: res}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		f    JobFilter
		want int
	}{
		{JobFilter{Severity: "critical"}, 1}, {JobFilter{Category: "security"}, 1}, {JobFilter{Severity: "critical", Category: "security"}, 1},
		{JobFilter{Severity: "any"}, 1}, {JobFilter{Severity: "minor"}, 0}, {JobFilter{Category: "style"}, 0}, {JobFilter{Severity: "major"}, 0},
	} {
		c.f.Host = "findings.example.com"
		if got, err := p.ListJobs(ctx, c.f); err != nil || len(got) != c.want {
			t.Errorf("%+v: %d jobs (%v), want %d", c.f, len(got), err, c.want)
		}
	}

	pts, err := p.Metrics(ctx, MetricsFilter{Granularity: Monthly, From: at.AddDate(0, -2, 0), To: at.Add(time.Hour), Host: "scores.example.com"})
	var scored int
	var q int64
	for _, pt := range pts {
		scored, q = scored+pt.Scored, q+pt.QualitySum
	}
	if err != nil || scored != 3 || q != 70+90+95 {
		t.Errorf("rollups sum the scores: %v scored %d quality %d", err, scored, q)
	}

	repos, err := p.RepoScores(ctx, at.AddDate(0, 0, -30))
	var app *RepoScore
	for i := range repos {
		if repos[i].Host == "scores.example.com" && repos[i].Repo == "team/app" {
			app = &repos[i]
		}
	}
	if err != nil || app == nil || app.Number != 2 || app.Latest.Security != 50 || len(app.Trend) < 1 || len(app.Trend) > 2 {
		t.Fatalf("latest scores and a trend per repository: %v %+v", err, app)
	}

	// Results stored before migration 17 get their scores filled in.
	var id int64
	if err := p.pool.QueryRow(ctx, `INSERT INTO jobs (kind, platform, host, repo, number, url, trigger, state, enqueued, finished, result)
		VALUES ('review', 'gitlab', 'backfill.example.com', 'old/repo', 9, '', 'push', 'done', $1, $1, $2) RETURNING id`, at,
		[]byte(`{"summary":"x","scores":{"quality":81,"security":82,"maintainability":83}}`)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `INSERT INTO jobs (kind, platform, host, repo, number, url, trigger, state, enqueued, finished, result)
		VALUES ('review', 'gitlab', 'backfill.example.com', 'old/repo', 10, '', 'push', 'done', $1, $1, $2)`, at,
		[]byte(`{"scores":{"quality":"high","security":0,"maintainability":500}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, backfillScores); err != nil {
		t.Fatal(err)
	}
	j, err := p.GetJob(ctx, id)
	if err != nil || j.Scores == nil || *j.Scores != (Scores{Quality: 81, Security: 82, Maintainability: 83}) {
		t.Errorf("backfilled from the stored result: %v %+v", err, j)
	}
	bad, _ := p.ListJobs(ctx, JobFilter{Host: "backfill.example.com"})
	for _, b := range bad {
		if b.Number == 10 && b.Scores != nil {
			t.Errorf("out-of-range or non-numeric scores are not backfilled: %+v", b.Scores)
		}
	}
}

func TestHostsVersion(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	v := func() int64 {
		t.Helper()
		n, err := p.HostsVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	v0 := v()
	h, err := p.PutHost(ctx, HostRow{Platform: "gitlab", Host: "version.example.com", BaseURL: "https://version.example.com", Enabled: true, Config: json.RawMessage(`{}`)}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	v1 := v()
	if v1 <= v0 {
		t.Errorf("a host write bumps the version: %d -> %d", v0, v1)
	}
	if _, err := p.PutWebhookEndpoint(ctx, WebhookEndpoint{ID: "6f1c0d5e-0000-4000-8000-00000000a101", HostID: h.ID, Name: "x", Enabled: true}, "a@x"); err != nil {
		t.Fatal(err)
	}
	v2 := v()
	if v2 <= v1 {
		t.Errorf("a webhook write bumps the version: %d -> %d", v1, v2)
	}
	if v() != v2 {
		t.Error("reading doesn't bump it")
	}
	if err := p.DeleteHost(ctx, h.ID, "a@x"); err != nil { // its webhooks go with it
		t.Fatal(err)
	}
	if v3 := v(); v3 <= v2 {
		t.Errorf("a delete bumps the version: %d -> %d", v2, v3)
	}
}

func TestImpact(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	// A year of its own, so other tests' jobs don't count.
	day := time.Date(2031, 3, 10, 9, 0, 0, 0, time.UTC)
	result := func(findings ...string) json.RawMessage {
		return json.RawMessage(`{"findings":[` + strings.Join(findings, ",") + `]}`)
	}
	add := func(kind, state, repo string, n int, at time.Time, wait time.Duration, res json.RawMessage, sc *Scores) {
		t.Helper()
		if _, err := p.InsertJob(ctx, Job{Kind: kind, Platform: "gitlab", Host: "impact.example.com", Repo: repo, Number: n, State: state,
			Enqueued: at.Add(-wait), Started: at.Add(-time.Second), Finished: at, Comments: 2, Result: res, Scores: sc}); err != nil {
			t.Fatal(err)
		}
	}
	sec := `{"severity":"critical","category":"security"}`
	bug := `{"severity":"major","category":"bug"}`
	q := `{"severity":"minor","category":"bug","kind":"question"}`
	add("review", "done", "Team/App", 1, day, 40*time.Second, result(sec, bug, q), &Scores{Quality: 70, Security: 40, Maintainability: 80})
	add("full review", "done", "Team/App", 1, day.Add(2*time.Hour), time.Second, result(bug), nil) // same MR again
	add("review", "done", "team/lib", 2, day.AddDate(0, 0, 1), 20*time.Second, result(`{"severity":"weird","category":"Style!"}`), nil)
	add("review", "skipped", "team/lib", 3, day, 0, nil, nil)                                  // not a review run
	add("answer", "done", "team/lib", 2, day, 0, nil, nil)                                     // not a review run
	add("review", "done", "team/old", 9, day.AddDate(0, -2, 0), time.Minute, result(bug), nil) // before the period

	from, to := time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)
	im, err := p.Impact(ctx, ImpactFilter{From: from, To: to, Granularity: Daily, Top: 5})
	if err != nil {
		t.Fatal(err)
	}
	if im.ReviewRuns != 3 || im.PRsReviewed != 2 || im.ActiveRepos != 2 {
		t.Errorf("runs %d, merge requests %d, repos %d", im.ReviewRuns, im.PRsReviewed, im.ActiveRepos)
	}
	if im.Findings != 4 || im.BySeverity["critical"] != 1 || im.BySeverity["major"] != 2 || im.BySeverity["other"] != 1 || im.SecurityFindings != 1 {
		t.Errorf("findings (questions left out, odd values as other): %d %+v security %d", im.Findings, im.BySeverity, im.SecurityFindings)
	}
	if len(im.ByCategory) != 3 || im.ByCategory[0] != (FindingCount{Category: "bug", Count: 2}) {
		t.Errorf("by category: %+v", im.ByCategory)
	}
	if im.FirstReviews != 2 || im.MedianFirstReviewMS != 30000 {
		t.Errorf("first reviews: %d, median %dms (want 2, 30000)", im.FirstReviews, im.MedianFirstReviewMS)
	}
	if im.Scored != 1 || im.SecuritySum != 40 {
		t.Errorf("scores: %d %d", im.Scored, im.SecuritySum)
	}
	if len(im.Trend) != 2 || im.Trend[0].Reviews != 2 || im.Trend[0].Findings["major"] != 2 || im.Trend[1].Findings["other"] != 1 {
		t.Errorf("daily trend: %+v", im.Trend)
	}
	if len(im.TopByReviews) != 2 || im.TopByReviews[0].Repo != "team/app" || im.TopByReviews[0].Count != 2 ||
		len(im.TopByFindings) != 2 || im.TopByFindings[0].Count != 3 {
		t.Errorf("top repositories: %+v / %+v", im.TopByReviews, im.TopByFindings)
	}
	monthly, err := p.Impact(ctx, ImpactFilter{From: from, To: to, Granularity: Monthly})
	if err != nil || monthly.ReviewRuns != 3 || monthly.Findings != 4 || len(monthly.Trend) != 1 || len(monthly.TopByReviews) != 0 {
		t.Errorf("monthly: %v %+v", err, monthly)
	}

	// Reviews stored before migration 19 are filled in by the backfill.
	bf := time.Date(2032, 6, 5, 12, 0, 0, 0, time.UTC)
	if _, err := p.pool.Exec(ctx, `INSERT INTO jobs (kind, platform, host, repo, number, url, trigger, state, enqueued, finished, result)
		VALUES ('review', 'gitlab', 'impact.example.com', 'Old/Repo', 4, '', 'push', 'done', $1, $2, $3)`,
		bf.Add(-10*time.Second), bf, []byte(`{"findings":[{"severity":"minor","category":"tests"},{"severity":"major","category":"bug","kind":"question"}]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // twice: it only adds what is missing
		if _, err := p.pool.Exec(ctx, impactBackfill); err != nil {
			t.Fatal(err)
		}
	}
	im, err = p.Impact(ctx, ImpactFilter{From: time.Date(2032, 6, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2032, 7, 1, 0, 0, 0, 0, time.UTC), Granularity: Daily})
	if err != nil || im.PRsReviewed != 1 || im.Findings != 1 || im.BySeverity["minor"] != 1 || im.FirstReviews != 1 || im.MedianFirstReviewMS != 10000 {
		t.Errorf("backfilled: %v %+v", err, im)
	}
}

func TestRuleApprove(t *testing.T) {
	p := openTest(t)
	ctx := context.Background()
	on, off := true, false
	r, err := p.CreateRule(ctx, Rule{Platform: "gitlab", Host: "approve.example.com", Pattern: "acme"}, "a@x")
	if err != nil || r.Approve != nil {
		t.Fatalf("unset stays nil: %v %+v", err, r)
	}
	if r, err = p.UpdateRule(ctx, r.ID, Rule{Approve: &on}, "a@x"); err != nil || r.Approve == nil || !*r.Approve {
		t.Fatalf("on: %v %+v", err, r)
	}
	if r, err = p.UpdateRule(ctx, r.ID, Rule{Approve: &off}, "a@x"); err != nil || r.Approve == nil || *r.Approve {
		t.Fatalf("off: %v %+v", err, r)
	}
	rules, _ := p.ListRules(ctx)
	for _, x := range rules {
		if x.ID == r.ID && (x.Approve == nil || *x.Approve) {
			t.Errorf("listed: %+v", x)
		}
	}
	_ = p.DeleteRule(ctx, r.ID, "a@x")
}
