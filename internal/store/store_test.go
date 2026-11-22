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
	"testing"
	"time"

	"github.com/google/uuid"

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
	if _, err := p.pool.Exec(ctx, `TRUNCATE repos, jobs, sessions, audit_log, model_profiles, settings RESTART IDENTITY`); err != nil {
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
