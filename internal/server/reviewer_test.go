package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
)

func TestRequireReviewerGitLab(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	cfg.Repos.RequireReviewer = true
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	s.selfLogins["127.0.0.1:1"] = "gitlab-bot"

	send := func(iid int, payload map[string]any) (int, string) {
		payload["object_kind"] = "merge_request"
		payload["project"] = map[string]any{"path_with_namespace": "g/p"}
		attrs := payload["object_attributes"].(map[string]any)
		attrs["iid"] = iid
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(b))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	users := func(names ...string) []map[string]string {
		out := []map[string]string{}
		for _, n := range names {
			out = append(out, map[string]string{"username": n})
		}
		return out
	}
	cases := []struct {
		name    string
		iid     int
		payload map[string]any
		want    int
	}{
		{"opened without the bot", 1, map[string]any{"object_attributes": map[string]any{"action": "open"}, "reviewers": users("alice")}, http.StatusOK},
		{"opened with the bot", 2, map[string]any{"object_attributes": map[string]any{"action": "open"}, "reviewers": users("alice", "GitLab-Bot")}, http.StatusAccepted},
		{"push without the bot", 3, map[string]any{"object_attributes": map[string]any{"action": "update", "oldrev": "abc"}}, http.StatusOK},
		{"push with the bot", 4, map[string]any{"object_attributes": map[string]any{"action": "update", "oldrev": "abc"}, "reviewers": users("gitlab-bot")}, http.StatusAccepted},
		{"bot requested as reviewer", 5, map[string]any{"object_attributes": map[string]any{"action": "update"}, "reviewers": users("gitlab-bot"),
			"changes": map[string]any{"reviewers": map[string]any{"previous": users(), "current": users("gitlab-bot")}}}, http.StatusAccepted},
		{"other reviewer changes", 6, map[string]any{"object_attributes": map[string]any{"action": "update"}, "reviewers": users("gitlab-bot", "ops"),
			"changes": map[string]any{"reviewers": map[string]any{"previous": users("gitlab-bot"), "current": users("gitlab-bot", "ops")}}}, http.StatusOK},
		{"bot removed as reviewer", 7, map[string]any{"object_attributes": map[string]any{"action": "update"}, "reviewers": users(),
			"changes": map[string]any{"reviewers": map[string]any{"previous": users("gitlab-bot"), "current": users()}}}, http.StatusOK},
	}
	for _, c := range cases {
		if code, body := send(c.iid, c.payload); code != c.want {
			t.Errorf("%s: got %d %s, want %d", c.name, code, body, c.want)
		}
	}
	if _, body := send(8, map[string]any{"object_attributes": map[string]any{"action": "open"}}); !bytes.Contains([]byte(body), []byte("@gitlab-bot is not a reviewer")) {
		t.Errorf("reason should name the bot: %s", body)
	}

	// Without require_reviewer, reviewer changes alone never start a review.
	s.cfg.Repos.RequireReviewer = false
	if code, _ := send(9, cases[4].payload); code != http.StatusOK {
		t.Errorf("reviewer change without require_reviewer should be ignored: %d", code)
	}
	if code, _ := send(10, map[string]any{"object_attributes": map[string]any{"action": "open"}}); code != http.StatusAccepted {
		t.Errorf("without require_reviewer an opened MR is reviewed: %d", code)
	}
}

func TestRequireReviewerGitHub(t *testing.T) {
	s := testServer(t)
	s.cfg.Repos.RequireReviewer = true
	s.selfLogins["127.0.0.1:1"] = "review-bot" // identity is keyed by the configured host (the fake API host here)
	h := s.Handler()
	send := func(action string, extra string) int {
		body := []byte(`{"action":"` + action + `","number":7,"repository":{"full_name":"o/r","html_url":"https://github.com/o/r"}` + extra + `}`)
		return post(t, h, "pull_request", body, sign("s3cret", body)).Code
	}
	if c := send("opened", `,"pull_request":{"number":7,"requested_reviewers":[{"login":"alice"}]}`); c != http.StatusOK {
		t.Errorf("opened without the bot: %d", c)
	}
	if c := send("opened", `,"pull_request":{"number":7,"requested_reviewers":[{"login":"Review-Bot"}]}`); c != http.StatusAccepted {
		t.Errorf("opened with the bot: %d", c)
	}
	if c := send("review_requested", `,"requested_reviewer":{"login":"review-bot"},"pull_request":{"number":8}`); c != http.StatusAccepted {
		t.Errorf("bot requested: %d", c)
	}
	if c := send("review_requested", `,"requested_reviewer":{"login":"alice"},"pull_request":{"number":9}`); c != http.StatusOK {
		t.Errorf("someone else requested: %d", c)
	}
}

func TestPerInstanceIdentity(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{
		{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "x1", Identity: config.Identity{BotName: "xbot", Reviewer: "x-reviewer"}},
		{BaseURL: "http://127.0.0.2:1", Token: "t", WebhookSecret: "y1"},
	}
	cfg.BotName = "codereview"
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	cfg.Repos.RequireReviewer = true
	cfg.Repos = config.Repos{RequireReviewer: true}
	// Load applied defaults before we replaced GitLab; apply them again.
	for i := range cfg.GitLab {
		if cfg.GitLab[i].BotName == "" {
			cfg.GitLab[i].BotName = cfg.BotName
		}
	}
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	s.selfLogins["127.0.0.2:1"] = "y-token-user"

	x, y := s.gitlabTarget(mustHost(t, s, "127.0.0.1:1")), s.gitlabTarget(mustHost(t, s, "127.0.0.2:1"))
	if x.botName != "xbot" || s.reviewerFor(x, "127.0.0.1:1") != "x-reviewer" {
		t.Errorf("instance x identity: %q %q", x.botName, s.reviewerFor(x, "127.0.0.1:1"))
	}
	if y.botName != "codereview" || s.reviewerFor(y, "127.0.0.2:1") != "y-token-user" {
		t.Errorf("instance y should default to bot_name and the token user: %q %q", y.botName, s.reviewerFor(y, "127.0.0.2:1"))
	}
	yml, _, err := s.jobConfig(context.Background(), x)
	if err != nil || !bytes.Contains(yml, []byte("bot_name: xbot")) {
		t.Errorf("job config should carry the host's handle: %s %v", yml, err)
	}
	send := func(secret, host, reviewer string) int {
		b, _ := json.Marshal(map[string]any{"object_kind": "merge_request", "project": map[string]any{"path_with_namespace": "g/p", "web_url": "http://" + host + "/g/p"},
			"object_attributes": map[string]any{"iid": 1, "action": "open"}, "reviewers": []map[string]string{{"username": reviewer}}})
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(b))
		req.Header.Set("X-Gitlab-Token", secret)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if c := send("x1", "127.0.0.1:1", "x-reviewer"); c != http.StatusAccepted {
		t.Errorf("x: its configured reviewer should trigger: %d", c)
	}
	if c := send("y1", "127.0.0.2:1", "x-reviewer"); c != http.StatusOK {
		t.Errorf("y: x's reviewer must not count on y: %d", c)
	}
	if c := send("y1", "127.0.0.2:1", "y-token-user"); c != http.StatusAccepted {
		t.Errorf("y: its token user should trigger: %d", c)
	}
}

func TestClosedMergeRequests(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour // keep reviews waiting
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	send := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	mr := func(action, state, extra string) string {
		return `{"object_kind":"merge_request","project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":1,"action":"` + action +
			`","state":"` + state + `"` + extra + `}}`
	}
	// The !1677 case: reviewer added in the same second the MR was closed.
	if c, b := send(mr("update", "closed", `,"oldrev":"abc"`)); c != http.StatusOK || !bytes.Contains([]byte(b), []byte("merge request is closed")) {
		t.Errorf("events for a closed MR must not queue: %d %s", c, b)
	}
	if c, _ := send(mr("open", "opened", "")); c != http.StatusAccepted {
		t.Fatalf("open should queue: %d", c)
	}
	if p, _ := s.queue.Stats(); p != 1 {
		t.Fatalf("want 1 waiting review, got %d", p)
	}
	if c, b := send(mr("close", "closed", "")); c != http.StatusOK || !bytes.Contains([]byte(b), []byte("cancelled its waiting review")) {
		t.Errorf("close should cancel the waiting review: %d %s", c, b)
	}
	if p, _ := s.queue.Stats(); p != 0 {
		t.Errorf("waiting review not cancelled: %d pending", p)
	}
	if snap := s.queue.Snapshot(); len(snap.Recent) != 1 || snap.Recent[0].State != Skipped {
		t.Errorf("cancelled review should show as skipped: %+v", snap.Recent)
	}
	if c, _ := send(mr("reopen", "opened", "")); c != http.StatusAccepted {
		t.Errorf("reopen should queue again: %d", c)
	}
}

func TestDistinctEventsSameSecondAreNotDuplicates(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	send := func(body string) string {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Body.String()
	}
	base := `{"object_kind":"merge_request","project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":1,"action":"approved","updated_at":"2026-11-19 10:26:45 UTC"},"user":{"username":"%s"}}`
	a := send(strings.Replace(base, "%s", "alice", 1))
	b := send(strings.Replace(base, "%s", "bob", 1))
	again := send(strings.Replace(base, "%s", "bob", 1))
	if strings.Contains(a, "duplicate") || strings.Contains(b, "duplicate") {
		t.Errorf("approvals by two people in the same second are different events: %s / %s", a, b)
	}
	if !strings.Contains(again, "duplicate") {
		t.Errorf("an identical payload is the same event: %s", again)
	}
}

func TestBranchSyncSkipped(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	cfg.Repos.RequireReviewer = true
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	s.selfLogins["127.0.0.1:1"] = "gitlab-bot"
	send := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	mr := func(iid, src, dst string) string {
		return `{"object_kind":"merge_request","project":{"path_with_namespace":"acme/finance","default_branch":"master"},` +
			`"reviewers":[{"username":"gitlab-bot"}],"object_attributes":{"iid":` + iid + `,"action":"open","state":"opened",` +
			`"source_branch":"` + src + `","target_branch":"` + dst + `"}}`
	}
	if c, b := send(mr("1234", "master", "release-2026-11")); c != http.StatusOK || !strings.Contains(b, "branch sync from the default branch (master into release-2026-11)") {
		t.Errorf("master -> release must be skipped even with the bot as reviewer: %d %s", c, b)
	}
	if c, _ := send(mr("1235", "TICKET-1337", "master")); c != http.StatusAccepted {
		t.Errorf("a feature branch into master is reviewed: %d", c)
	}
	note := `{"object_kind":"note","user":{"username":"dev"},"project":{"path_with_namespace":"acme/finance","default_branch":"master"},"merge_request":{"iid":1234},` +
		`"object_attributes":{"id":5,"note":"@gitlab-bot review","noteable_type":"MergeRequest"}}`
	if c, _ := send(note); c != http.StatusAccepted {
		t.Errorf("an explicit @gitlab-bot review still reviews a branch sync: %d", c)
	}
}

func TestMentionDefaultsToTokenUser(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.BotName = ""
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	s.selfLogins["127.0.0.1:1"] = "gitlab-bot"
	tgt := s.gitlabTarget(mustHost(t, s, "127.0.0.1:1"))
	if tgt.botName != "gitlab-bot" {
		t.Errorf("mention should default to the token user: %q", tgt.botName)
	}
	yml, _, _ := s.jobConfig(context.Background(), tgt)
	if !bytes.Contains(yml, []byte("bot_name: gitlab-bot")) {
		t.Errorf("the CLI should get the same handle for its command hints: %s", yml)
	}
	send := func(note string) int {
		b := `{"object_kind":"note","user":{"username":"dev"},"project":{"path_with_namespace":"g/p"},"merge_request":{"iid":3},` +
			`"object_attributes":{"id":` + strconv.Itoa(len(note)) + `,"note":"` + note + `","noteable_type":"MergeRequest"}}`
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(b)))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if c := send("@gitlab-bot review"); c != http.StatusAccepted {
		t.Errorf("@gitlab-bot review should run: %d", c)
	}
	if c := send("@codereview please review"); c != http.StatusOK {
		t.Errorf("@codereview is not this bot's name here: %d", c)
	}
}

func mustHost(t *testing.T, s *Server, host string) hosts.Host {
	t.Helper()
	h, ok := s.hosts.Find(hosts.GitLab, host)
	if !ok {
		t.Fatalf("no gitlab host %s", host)
	}
	return h
}

func TestReviewNow(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	cfg.Repos.RequireReviewer = true // an explicit request ignores this
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ref := vcs.Ref{Platform: "gitlab", Host: "127.0.0.1:1", Repo: "g/p", Number: 3}
	if err := s.ReviewNow(context.Background(), ref, true, "admin@x"); err != nil {
		t.Fatal(err)
	}
	snap := s.queue.Snapshot()
	if len(snap.Running)+len(snap.Pending) != 1 {
		t.Errorf("review should be queued: %+v", snap)
	}
	if err := s.ReviewNow(context.Background(), vcs.Ref{Platform: "gitlab", Host: "elsewhere", Repo: "g/p", Number: 1}, false, "a"); err == nil {
		t.Error("unknown host must fail")
	}
}
