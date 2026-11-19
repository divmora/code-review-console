package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeExec records tasks and returns a canned result.
type fakeExec struct {
	mu    sync.Mutex
	tasks []executor.Task
	res   *executor.Result
	err   error
}

func (f *fakeExec) Name() string { return "fake" }
func (f *fakeExec) Run(_ context.Context, t executor.Task) (*executor.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, t)
	if f.err != nil {
		return nil, f.err
	}
	if f.res != nil {
		return f.res, nil
	}
	return &executor.Result{}, nil
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifyGitHub(t *testing.T) {
	body := []byte(`{"a":1}`)
	if !verifyGitHub("s3cret", body, sign("s3cret", body)) {
		t.Error("valid signature rejected")
	}
	for _, h := range []string{"", "sha256=00", sign("other", body), "sha1=abc"} {
		if verifyGitHub("s3cret", body, h) {
			t.Errorf("invalid signature %q accepted", h)
		}
	}
	if verifyGitHub("", body, sign("", body)) {
		t.Error("empty secret must never verify")
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		body, kind, q string
		ok            bool
	}{
		{"@codereview review", "review", "", true},
		{"@CodeReview full review please", "ask", "full review please", true},
		{"@codereview full review", "full", "", true},
		{"hey @codereview[bot] help", "help", "", true},
		{"@codereview why is line 4 unsafe?", "ask", "why is line 4 unsafe?", true},
		{"Looks good @codereview, is the lock needed here?", "ask", "Looks good , is the lock needed here?", true},
		{"mail me at x@codereview.dev", "", "", false},
		{"`@codereview review` is the command", "", "", false},
		{"@codereviewer review", "", "", false},
		{"no mention", "", "", false},
	}
	for _, c := range cases {
		cmd, ok := parseCommand("codereview", c.body)
		if ok != c.ok || (ok && (cmd.kind != c.kind || cmd.question != c.q)) {
			t.Errorf("parseCommand(%q) = %+v %v, want kind=%q q=%q ok=%v", c.body, cmd, ok, c.kind, c.q, c.ok)
		}
	}
}

func TestQueueMergesAndSerializes(t *testing.T) {
	q := NewQueue(4, 10, time.Minute, 50, quiet)
	defer q.Shutdown(context.Background())

	var running, maxRunning, runs atomic.Int32
	release := make(chan struct{})
	var mu sync.Mutex
	var seen []string
	mk := func(label string, block bool) *Job {
		return &Job{Key: "pr1", Label: label, Run: func(ctx context.Context, _ *Report) error {
			n := running.Add(1)
			for {
				m := maxRunning.Load()
				if n <= m || maxRunning.CompareAndSwap(m, n) {
					break
				}
			}
			mu.Lock()
			seen = append(seen, label)
			mu.Unlock()
			if block {
				<-release
			}
			running.Add(-1)
			runs.Add(1)
			return nil
		}}
	}
	if err := q.Submit(mk("first", true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return running.Load() == 1 })
	// While first runs, two more arrive for the same PR: they merge into one.
	_ = q.Submit(mk("second", false))
	_ = q.Submit(mk("third", false))
	close(release)
	waitFor(t, func() bool { return runs.Load() == 2 })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[1] != "third" || maxRunning.Load() != 1 {
		t.Errorf("seen=%v maxRunning=%d", seen, maxRunning.Load())
	}
}

func TestQueueFull(t *testing.T) {
	q := NewQueue(1, 1, time.Minute, 50, quiet)
	defer q.Shutdown(context.Background())
	block := make(chan struct{})
	defer close(block)
	_ = q.Submit(&Job{Key: "a", Delay: time.Hour, Run: func(context.Context, *Report) error { return nil }})
	if err := q.Submit(&Job{Key: "b", Run: func(context.Context, *Report) error { return nil }}); err != ErrQueueFull {
		t.Errorf("want ErrQueueFull, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub.WebhookSecret = "s3cret"
	cfg.GitHub.Token = "t"
	cfg.GitHub.APIURL = "http://127.0.0.1:1" // never reached in these tests
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour // keep jobs pending
	s, err := New(cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.queue.Shutdown(ctx)
	})
	return s
}

func post(t *testing.T, h http.Handler, event string, body []byte, sig string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestGitHubWebhookRouting(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	prOpened := []byte(`{"action":"opened","number":7,"repository":{"full_name":"o/r","html_url":"https://github.com/o/r"},"pull_request":{"number":7}}`)

	if w := post(t, h, "pull_request", prOpened, "sha256=bad"); w.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: got %d", w.Code)
	}
	if w := post(t, h, "pull_request", prOpened, sign("s3cret", prOpened)); w.Code != http.StatusAccepted {
		t.Errorf("pr opened: got %d %s", w.Code, w.Body)
	}
	// A second push before the debounce fires merges into the same job.
	sync := []byte(`{"action":"synchronize","number":7,"repository":{"full_name":"o/r","html_url":"https://github.com/o/r"}}`)
	post(t, h, "pull_request", sync, sign("s3cret", sync))
	if p, _ := s.queue.Stats(); p != 1 {
		t.Errorf("pending = %d, want 1 merged job", p)
	}
	labeled := []byte(`{"action":"labeled","number":7,"repository":{"full_name":"o/r"}}`)
	if w := post(t, h, "pull_request", labeled, sign("s3cret", labeled)); w.Code != http.StatusOK {
		t.Errorf("labeled should be ignored with 200, got %d", w.Code)
	}
	botComment := []byte(`{"action":"created","issue":{"number":7,"pull_request":{}},"repository":{"full_name":"o/r"},"comment":{"id":1,"body":"@codereview review","user":{"login":"x[bot]","type":"Bot"}},"sender":{"type":"Bot"}}`)
	w := post(t, h, "issue_comment", botComment, sign("s3cret", botComment))
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("bot")) {
		t.Errorf("bot comments must be ignored: %d %s", w.Code, w.Body)
	}
	own := []byte(`{"action":"created","issue":{"number":7,"pull_request":{}},"repository":{"full_name":"o/r"},"comment":{"id":2,"body":"> q\n\nanswer @codereview\n\n<!-- codereview:reply -->","user":{"login":"me","type":"User"}},"sender":{"type":"User"}}`)
	if w := post(t, h, "issue_comment", own, sign("s3cret", own)); !bytes.Contains(w.Body.Bytes(), []byte("ignored")) {
		t.Errorf("bot output must be ignored: %s", w.Body)
	}
	if w := post(t, h, "ping", []byte(`{}`), sign("s3cret", []byte(`{}`))); w.Code != http.StatusOK {
		t.Errorf("ping: %d", w.Code)
	}
}

func TestGitLabWebhookToken(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := New(cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	body := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":3,"action":"update","oldrev":""}}`)
	send := func(tok string, b []byte) int {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(b))
		req.Header.Set("X-Gitlab-Token", tok)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if c := send("wrong", body); c != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", c)
	}
	if c := send("glsecret", body); c != http.StatusOK {
		t.Errorf("metadata-only update should be ignored with 200: %d", c)
	}
	push := bytes.Replace(body, []byte(`"oldrev":""`), []byte(`"oldrev":"abc"`), 1)
	if c := send("glsecret", push); c != http.StatusAccepted {
		t.Errorf("push update should queue: %d", c)
	}
}

func TestDashboard(t *testing.T) {
	s := testServer(t)
	s.cfg.Server.Dashboard = config.Dashboard{Enabled: true, Token: "dash", Title: "Bots", History: 10}
	h := s.Handler()
	get := func(path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := get("/api/status", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("status without token: %d", w.Code)
	}
	if w := get("/dashboard?token=wrong", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("page with wrong token: %d", w.Code)
	}
	page := get("/dashboard?token=dash", "")
	if page.Code != http.StatusOK || !bytes.Contains(page.Body.Bytes(), []byte("<title>Bots</title>")) {
		t.Fatalf("page: %d", page.Code)
	}
	if csp := page.Header().Get("Content-Security-Policy"); !bytes.Contains([]byte(csp), []byte("script-src 'nonce-")) {
		t.Errorf("CSP missing nonce: %q", csp)
	}

	// One pending (debounced) review, one finished job.
	body := []byte(`{"action":"opened","number":7,"repository":{"full_name":"o/secret-repo","html_url":"https://github.com/o/secret-repo"}}`)
	post(t, h, "pull_request", body, sign("s3cret", body))
	done := make(chan struct{})
	_ = s.queue.Submit(&Job{Key: "x", Meta: Meta{Kind: "help", Repo: "o/secret-repo", Number: 3}, Run: func(context.Context, *Report) error {
		defer close(done)
		return errors.New("boom at o/secret-repo")
	}})
	<-done
	waitFor(t, func() bool { snap := s.queue.Snapshot(); return len(snap.Recent) == 1 })

	w := get("/api/status", "dash")
	var st statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Pending) != 1 || st.Pending[0].URL != "https://github.com/o/secret-repo/pull/7" || st.Pending[0].Trigger != "push" {
		t.Errorf("pending = %+v", st.Pending)
	}
	if len(st.Recent) != 1 || st.Recent[0].State != Failed || st.Totals.Failed != 1 {
		t.Errorf("recent = %+v", st.Recent)
	}

	s.cfg.Server.Dashboard.HideRepos = true
	w = get("/api/status", "dash")
	if bytes.Contains(w.Body.Bytes(), []byte("secret-repo")) {
		t.Errorf("hide_repos leaked a repo name: %s", w.Body)
	}
}

func TestDashboardDisabledByDefault(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("dashboard should be off by default, got %d", w.Code)
	}
}

func TestAllowlist(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "s3cret", "t", "http://127.0.0.1:1"
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	cfg.Repos.Allowlist = true

	if _, err := New(cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)}); err == nil {
		t.Fatal("allowlist without a store must fail")
	}
	st := storetest.New()
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	h := s.Handler()
	body := []byte(`{"action":"opened","number":7,"repository":{"full_name":"Org/API","html_url":"https://github.com/Org/API"}}`)

	w := post(t, h, "pull_request", body, sign("s3cret", body))
	if !bytes.Contains(w.Body.Bytes(), []byte("not enabled")) {
		t.Fatalf("unknown repo should be ignored: %d %s", w.Code, w.Body)
	}
	repo, err := st.FindRepo(context.Background(), "github", "github.com", "org/api")
	if err != nil || repo.Mode != store.ModeInherit {
		t.Fatalf("unknown repo should be recorded disabled: %+v %v", repo, err)
	}
	cmd := []byte(`{"action":"created","issue":{"number":7,"pull_request":{}},"repository":{"full_name":"Org/API","html_url":"https://github.com/Org/API"},"comment":{"id":9,"body":"@codereview review","user":{"login":"dev","type":"User"}},"sender":{"type":"User"}}`)
	if w := post(t, h, "issue_comment", cmd, sign("s3cret", cmd)); !bytes.Contains(w.Body.Bytes(), []byte("not enabled")) {
		t.Errorf("commands on disabled repos must be ignored too: %s", w.Body)
	}

	prof := config.Chill
	_, _ = st.UpdateRepo(context.Background(), repo.ID, store.ModeEnabled, config.Overrides{Profile: &prof}, "admin@x")
	if w := post(t, h, "pull_request", body, sign("s3cret", body)); w.Code != http.StatusAccepted {
		t.Fatalf("enabled repo should queue: %d %s", w.Code, w.Body)
	}
	allowed, ov := s.gate(context.Background(), vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "org/api"})
	if !allowed || ov == nil || *ov.Profile != config.Chill {
		t.Errorf("gate should pass overrides: %v %+v", allowed, ov)
	}
	// Group rules: an org-wide rule enables repos that inherit, and its
	// settings merge under the repository's own.
	maxC := 4
	on, off := true, false
	_, _ = st.CreateRule(context.Background(), store.Rule{Platform: "github", Host: "github.com", Pattern: "acme", Enabled: &on,
		Settings: config.Overrides{MaxComments: &maxC}}, "admin@x")
	if ok, ov := s.gate(context.Background(), vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "acme/new"}); !ok || ov == nil || *ov.MaxComments != 4 {
		t.Errorf("org rule should enable acme/new with its settings: %v %+v", ok, ov)
	}
	_, _ = st.CreateRule(context.Background(), store.Rule{Platform: "github", Host: "github.com", Pattern: "acme/legacy-*", Enabled: &off}, "admin@x")
	if ok, _ := s.gate(context.Background(), vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "acme/legacy-app"}); ok {
		t.Error("a more specific disable rule should win")
	}
	legacy, _ := st.FindRepo(context.Background(), "github", "github.com", "acme/legacy-app")
	_, _ = st.UpdateRepo(context.Background(), legacy.ID, store.ModeEnabled, config.Overrides{}, "admin@x")
	if ok, _ := s.gate(context.Background(), vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "acme/legacy-app"}); !ok {
		t.Error("an explicit repository mode should override rules")
	}
}

func TestJobHistoryPersists(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "s3cret", "t", "http://127.0.0.1:1"
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	st := storetest.New()
	_, _ = st.InsertJob(context.Background(), store.Job{Kind: "review", Platform: "github", Repo: "o/old", State: "done", Finished: time.Now()})
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	if snap := s.queue.Snapshot(); len(snap.Recent) != 1 || snap.Recent[0].Repo != "o/old" {
		t.Fatalf("history not loaded: %+v", snap.Recent)
	}
	done := make(chan struct{})
	_ = s.queue.Submit(&Job{Key: "k", Meta: Meta{Kind: "help", Platform: "github", Repo: "o/new"}, Run: func(context.Context, *Report) error {
		close(done)
		return nil
	}})
	<-done
	waitFor(t, func() bool { jobs, _ := st.RecentJobs(context.Background(), 5); return len(jobs) == 2 })
	jobs, _ := st.RecentJobs(context.Background(), 5)
	if jobs[0].Repo != "o/new" || jobs[0].State != "done" {
		t.Errorf("finished job not saved: %+v", jobs[0])
	}
}

func TestRuntimeSettingsWithoutRestart(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "s3cret", "t", "http://127.0.0.1:1"
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Repos.Allowlist = true
	st := storetest.New()
	svc := settings.New(cfg, st)
	svc.Lic = licensetest.Full(t)
	if _, _, err := svc.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, Settings: svc})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ref := vcs.Ref{Platform: vcs.GitHub, Host: "github.com", Repo: "o/r"}
	if ok, _ := s.gate(context.Background(), ref); ok {
		t.Fatal("allowlist on: unknown repo must not be reviewed")
	}
	off, max := false, 6
	if _, err := svc.Put(context.Background(), settings.Stored{Allowlist: &off, ReviewDefaults: config.Overrides{MaxComments: &max}}, "a@x"); err != nil {
		t.Fatal(err)
	}
	ok, ov := s.gate(context.Background(), ref)
	if !ok || ov == nil || *ov.MaxComments != 6 {
		t.Errorf("turning the allowlist off in the admin panel should apply at once, with review defaults: %v %+v", ok, ov)
	}
	if rec := httptest.NewRecorder(); true {
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/dashboard", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("dashboard off by default: %d", rec.Code)
		}
	}
	on := true
	_, _ = svc.Put(context.Background(), settings.Stored{Allowlist: &off, DashboardEnabled: &on}, "a@x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("dashboard switched on from the admin panel: %d", rec.Code)
	}
}
