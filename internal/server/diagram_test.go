package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
)

// TestDiagramCommand: "@bot diagram" runs the CLI's --diagram-only job,
// records the diagram and its tokens, and an admin's "off" refuses it
// without running anything (issue #161).
func TestDiagramCommand(t *testing.T) {
	if cmd, ok := parseCommand("codereview", "@codereview diagram"); !ok || cmd.kind != "diagram" {
		t.Errorf("parse: %+v %v", cmd, ok)
	}
	if cmd, _ := parseCommand("codereview", "@codereview flow diagram."); cmd.kind != "diagram" {
		t.Errorf("parse flow diagram: %+v", cmd)
	}
	var mu sync.Mutex
	var notes []string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/notes") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			notes = append(notes, string(b))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":1}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer gl.Close()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: gl.URL, Token: "t", WebhookSecret: "s"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	fx := &fakeExec{res: &executor.Result{Diagram: true, DiagramTokens: 790, TokensIn: 700, TokensOut: 90, Model: "fake:model"}}
	s, err := newHooked(t, cfg, fx, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	h := s.hosts.Enabled(hosts.GitLab)[0]
	ref := vcs.Ref{Platform: vcs.GitLab, Host: h.Host, Repo: "g/app", Number: 4}
	tgt := s.gitlabTarget(h, ref.Repo)

	if err := s.dispatch(tgt, ref, command{kind: "diagram"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.queue.Snapshot().Recent) == 1 })
	rec := s.queue.Snapshot().Recent[0]
	if rec.Kind != "diagram" || rec.State != Done || !rec.Diagram || rec.DiagramTokens != 790 || rec.TokensIn != 700 {
		t.Errorf("record: %+v", rec)
	}
	fx.mu.Lock()
	if len(fx.tasks) != 1 || fx.tasks[0].Kind != "diagram" || !fx.tasks[0].Post {
		t.Errorf("task: %+v", fx.tasks)
	}
	fx.mu.Unlock()
	if args, _ := executor.Args(executor.Task{Kind: "diagram", URL: "https://g/app/-/merge_requests/4", Post: true}); strings.Join(args, " ") != "review https://g/app/-/merge_requests/4 --diagram-only --format json --post" {
		t.Errorf("args: %v", args)
	}

	// The admin turned diagrams off: refused with a note, nothing runs.
	off := "off"
	if err := s.dispatch(tgt, ref, command{kind: "diagram"}, &config.Overrides{Diagram: &off}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.queue.Snapshot().Recent) == 2 })
	fx.mu.Lock()
	ran := len(fx.tasks)
	fx.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if ran != 1 || len(notes) != 1 || !strings.Contains(notes[0], "turned off for this repository") {
		t.Errorf("off: %d tasks, notes %v", ran, notes)
	}

	// No diagram drawn: says why.
	fx.mu.Lock()
	fx.res = &executor.Result{DiagramNote: "the model found no flow to draw"}
	fx.mu.Unlock()
	mu.Unlock()
	if err := s.dispatch(tgt, ref, command{kind: "diagram"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.queue.Snapshot().Recent) == 3 })
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	if len(notes) != 2 || !strings.Contains(notes[1], "No flow diagram: the model found no flow to draw") {
		t.Errorf("no diagram note: %v", notes)
	}
	if !strings.Contains(helpText("codereview"), "@codereview diagram") {
		t.Error("help lists diagram")
	}
}
