package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func TestLearningsForScopes(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "s"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	st := storetest.New()
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ctx := context.Background()
	add := func(host, scope, text, status string) {
		_, _ = st.CreateLearning(ctx, store.Learning{Platform: "gitlab", Host: host, Scope: scope, Text: text, Status: status}, "a")
	}
	add("127.0.0.1:1", "acme/pay/api", "repo rule", store.LearningActive)
	add("127.0.0.1:1", "acme/pay", "group rule", store.LearningActive)
	add("127.0.0.1:1", "", "host rule", store.LearningActive)
	add("127.0.0.1:1", "acme/pay", "disabled rule", store.LearningDisabled)
	add("other.example", "", "other host", store.LearningActive)
	add("127.0.0.1:1", "acme/web", "sibling repo", store.LearningActive)
	ref := vcs.Ref{Platform: "gitlab", Host: "127.0.0.1:1", Repo: "acme/pay/api", Number: 1}
	var got []string
	for _, l := range s.learningsFor(ctx, ref) {
		got = append(got, l.Text)
	}
	if strings.Join(got, ",") != "host rule,group rule,repo rule" {
		t.Fatalf("learnings for acme/pay/api: %v", got)
	}
	ov := s.withLearnings(ctx, ref, &config.Overrides{Instructions: "x"})
	if len(ov.Learnings) != 3 || ov.Instructions != "x" {
		t.Errorf("overrides with learnings: %+v", ov)
	}
	off := false
	_, _ = s.settings.Put(ctx, settingsWith(func(st *settingsStored) { st.LearningsEnabled = &off }), "a")
	if len(s.learningsFor(ctx, ref)) != 0 {
		t.Error("learnings switched off in settings must not be sent")
	}
}

func TestParseRemember(t *testing.T) {
	for in, want := range map[string]string{
		"@codereview remember account numbers drop leading zeros": "account numbers drop leading zeros",
		"@codereview learn: legacy code is frozen":                "legacy code is frozen",
		"@codereview note we pin go 1.27":                         "we pin go 1.27",
	} {
		c, ok := parseCommand("codereview", in)
		if !ok || c.kind != "remember" || c.question != want {
			t.Errorf("%q: %+v", in, c)
		}
	}
	if c, _ := parseCommand("codereview", "@codereview what does remember() do?"); c.kind != "ask" {
		t.Errorf("a question mentioning remember is still a question: %+v", c)
	}
}

// fakeGitLabAPI serves a merge request discussion started by the bot and
// records reactions.
type fakeGitLabAPI struct {
	mu        sync.Mutex
	reactions []string
	notes     []string
	starter   string
}

func (g *fakeGitLabAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/discussions/"):
			body := "**ltrim drops zeros**"
			if g.starter == "gitlab-bot" {
				body = "<!-- codereview:fp=abc -->\\n" + body // the bot's comments carry its marker
			}
			_, _ = io.WriteString(w, `{"notes":[{"body":"`+body+`","author":{"username":"`+g.starter+`"},"position":{"new_path":"app/Pay.php"}},{"body":"reply","author":{"username":"dev"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/award_emoji"):
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			g.reactions = append(g.reactions, b["name"])
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/notes"):
			body, _ := io.ReadAll(r.Body)
			g.notes = append(g.notes, string(body))
			_, _ = io.WriteString(w, `{"id":1}`)
		case r.URL.Path == "/api/v4/user":
			_, _ = io.WriteString(w, `{"username":"gitlab-bot"}`)
		default:
			w.WriteHeader(404)
		}
	})
}

func TestLearnFromReplyAndRemember(t *testing.T) {
	api := &fakeGitLabAPI{starter: "gitlab-bot"}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: srv.URL, Token: "t", WebhookSecret: "glsecret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	st := storetest.New()
	exec := &fakeExec{res: &executor.Result{Raw: json.RawMessage(`{"learning":true,"text":"Account numbers are compared without leading zeros.","path":"","reason":"convention","usage":{"InputTokens":400,"OutputTokens":60}}`)}}
	s, err := New(cfg, exec, quiet, Deps{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	host := strings.TrimPrefix(srv.URL, "http://")
	send := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Gitlab-Token", "glsecret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	reply := `{"object_kind":"note","user":{"username":"dev"},"project":{"path_with_namespace":"acme/pay","web_url":"` + srv.URL + `/acme/pay"},"merge_request":{"iid":7},` +
		`"object_attributes":{"id":55,"note":"This is intentional: we compare without leading zeros everywhere.","noteable_type":"MergeRequest","discussion_id":"d1"}}`
	if w := send(reply); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "checked for learnings") {
		t.Fatalf("reply: %d %s", w.Code, w.Body)
	}
	waitFor(t, func() bool { ls, _ := st.ListLearnings(context.Background()); return len(ls) == 1 })
	ls, _ := st.ListLearnings(context.Background())
	if l := ls[0]; l.Scope != "acme/pay" || l.Host != host || l.Source != "reply" || l.SourceAuthor != "dev" || l.Status != store.LearningActive {
		t.Errorf("learning = %+v", l)
	}
	exec.mu.Lock()
	var in learnInput
	_ = json.Unmarshal([]byte(exec.tasks[0].Env["CODEREVIEW_LEARN_INPUT"]), &in)
	kind := exec.tasks[0].Kind
	exec.mu.Unlock()
	if kind != "learn" || in.Path != "app/Pay.php" || !strings.Contains(in.Finding, "ltrim drops zeros") || in.Author != "dev" {
		t.Errorf("learn task: %s %+v", kind, in)
	}
	waitFor(t, func() bool { api.mu.Lock(); defer api.mu.Unlock(); return len(api.reactions) == 2 })
	api.mu.Lock()
	if strings.Join(api.reactions, ",") != "eyes,white_check_mark" {
		t.Errorf("reactions: %v", api.reactions)
	}
	api.mu.Unlock()

	// The same reply redelivered does not create a second learning.
	send(reply)
	time.Sleep(200 * time.Millisecond)
	if ls, _ := st.ListLearnings(context.Background()); len(ls) != 1 {
		t.Errorf("duplicate learning from a redelivery: %d", len(ls))
	}

	// A reply in someone else's thread is not a learning candidate.
	api.mu.Lock()
	api.starter = "alice"
	api.mu.Unlock()
	other := strings.Replace(strings.Replace(reply, `"id":55`, `"id":56`, 1), `"d1"`, `"d2"`, 1)
	send(other)
	time.Sleep(200 * time.Millisecond)
	exec.mu.Lock()
	n := len(exec.tasks)
	exec.mu.Unlock()
	if n != 1 {
		t.Errorf("replies to people must not run the learn job: %d tasks", n)
	}

	// "@bot remember" saves directly and confirms.
	remember := `{"object_kind":"note","user":{"username":"dev"},"project":{"path_with_namespace":"acme/pay"},"merge_request":{"iid":7},` +
		`"object_attributes":{"id":60,"note":"@gitlab-bot remember Legacy controllers are frozen.","noteable_type":"MergeRequest"}}`
	if w := send(remember); w.Code != http.StatusAccepted {
		t.Fatalf("remember: %d %s", w.Code, w.Body)
	}
	waitFor(t, func() bool { ls, _ := st.ListLearnings(context.Background()); return len(ls) == 2 })
	waitFor(t, func() bool { api.mu.Lock(); defer api.mu.Unlock(); return len(api.notes) == 1 })
	api.mu.Lock()
	if !strings.Contains(api.notes[0], "Noted for future reviews") {
		t.Errorf("confirmation: %s", api.notes[0])
	}
	api.mu.Unlock()
}

type settingsStored = settings.Stored

func settingsWith(f func(*settingsStored)) settingsStored {
	var st settingsStored
	f(&st)
	return st
}
