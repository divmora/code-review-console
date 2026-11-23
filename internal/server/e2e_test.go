package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// End-to-end: webhook -> queue -> executor -> real codereview CLI -> fake
// GitHub + fake LLM. Enable with:
//
//	CRB_E2E_BIN=/path/to/codereview          local executor
//	CRB_E2E_IMAGE=code-review-bot:dev        docker executor (uses --network host)
//	CRB_E2E_KUBECONFIG=... CRB_E2E_IMAGE=... CRB_E2E_HOST=<ip reachable from pods>
func TestEndToEnd(t *testing.T) {
	type mode struct {
		name string
		exec func(t *testing.T) executor.Executor
		ok   bool
	}
	modes := []mode{
		{"local", func(t *testing.T) executor.Executor {
			return &executor.Local{Binary: os.Getenv("CRB_E2E_BIN"), Pass: executor.PassthroughNames()}
		}, os.Getenv("CRB_E2E_BIN") != ""},
		{"docker", func(t *testing.T) executor.Executor {
			return &executor.Docker{Binary: "docker", Image: os.Getenv("CRB_E2E_IMAGE"), Network: "host", Memory: "512m", Pass: executor.PassthroughNames()}
		}, os.Getenv("CRB_E2E_IMAGE") != "" && os.Getenv("CRB_E2E_KUBECONFIG") == ""},
		{"kubernetes", func(t *testing.T) executor.Executor {
			k, err := executor.NewKubernetes(config.KubeExecutor{Image: os.Getenv("CRB_E2E_IMAGE"), Namespace: "default", TTL: time.Minute,
				Kubeconfig: os.Getenv("CRB_E2E_KUBECONFIG"), CPU: "500m", Memory: "256Mi"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			return k
		}, os.Getenv("CRB_E2E_KUBECONFIG") != "" && os.Getenv("CRB_E2E_IMAGE") != ""},
	}
	ran := false
	for _, m := range modes {
		if !m.ok {
			continue
		}
		ran = true
		t.Run(m.name, func(t *testing.T) { runE2E(t, m.exec(t)) })
	}
	if !ran {
		t.Skip("set CRB_E2E_BIN, CRB_E2E_IMAGE or CRB_E2E_KUBECONFIG to run end-to-end tests")
	}
}

// listen serves h on all interfaces so containers/pods can reach it, and
// returns a URL using CRB_E2E_HOST (default 127.0.0.1).
func listen(t *testing.T, h http.Handler) string {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	host := os.Getenv("CRB_E2E_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, l.Addr().(*net.TCPAddr).Port)
}

const e2ePatch = "@@ -1,3 +1,5 @@\n package app\n \n-func A() {}\n+func A() {\n+\tpanic(\"todo\")\n+}\n"

func runE2E(t *testing.T, exec executor.Executor) {
	var mu sync.Mutex
	var reviews, notes []string
	var authSeen []string
	gh := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authSeen = append(authSeen, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			_, _ = io.WriteString(w, `{"title":"feat: A","body":"","html_url":"https://github.com/o/r/pull/7","user":{"login":"dev"},"head":{"ref":"f","sha":"abc123"},"base":{"ref":"main","sha":"base1"}}`)
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7/files":
			b, _ := json.Marshal(e2ePatch)
			_, _ = fmt.Fprintf(w, `[{"filename":"app.go","status":"modified","patch":%s,"changes":3}]`, b)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/o/r/contents/"):
			w.WriteHeader(404)
		case r.Method == "GET" && (r.URL.Path == "/repos/o/r/issues/7/comments" || r.URL.Path == "/repos/o/r/pulls/7/comments"):
			_, _ = io.WriteString(w, `[]`)
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls/7/reviews":
			reviews = append(reviews, string(body))
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/issues/7/comments":
			notes = append(notes, string(body))
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			w.WriteHeader(404)
		}
	})
	llmSrv := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		content := `{"summary":"Adds A.","cohorts":[],"effort":1,"poem":""}`
		if strings.Contains(req.Messages[0].Content, "doing a code review") {
			content = `{"files":[{"path":"app.go","summary":"A panics."}],"comments":[{"path":"app.go","start_line":4,"end_line":4,"severity":"major","category":"bug","title":"panic in library code","body":"Return an error.","confidence":0.9}]}`
		}
		b, _ := json.Marshal(content)
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}],"usage":{"prompt_tokens":42,"completion_tokens":7}}`, b)
	})
	ghURL, llmURL := listen(t, gh), listen(t, llmSrv)

	t.Setenv("CODEREVIEW_CONSOLE_CONFIG", "")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.WebhookSecret, cfg.GitHub.Token, cfg.GitHub.APIURL = "s3cret", "ghs_console_token", ghURL
	cfg.Models = map[string]config.ModelProfile{"fake": {"provider": "openai-compatible", "model": "m", "base_url": llmURL, "max_retries": -1}}
	cfg.Model = "fake"
	cfg.Server.Debounce = 0
	st := storetest.New()
	s, err := newHooked(t, cfg, exec, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())

	body := []byte(`{"action":"opened","number":7,"repository":{"full_name":"o/r","html_url":"https://github.com/o/r"}}`)
	if w := post(t, s.Handler(), "pull_request", body, sign("s3cret", body)); w.Code != http.StatusAccepted {
		t.Fatalf("webhook: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(150 * time.Second)
	var rec Record
	for {
		if snap := s.queue.Snapshot(); len(snap.Recent) == 1 {
			rec = snap.Recent[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("review did not finish")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if rec.State != Done {
		t.Fatalf("job %s: %s", rec.State, rec.Error)
	}
	if rec.Comments != 1 || rec.Files != 1 || rec.TokensIn == 0 {
		t.Errorf("record = %+v", rec)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reviews) != 1 || !strings.Contains(reviews[0], "panic in library code") {
		t.Errorf("inline review not posted: %v", reviews)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "codereview:summary") || !strings.Contains(notes[0], "Commands") {
		t.Errorf("summary not posted (with commands footer): %v", notes)
	}
	for _, a := range authSeen {
		if a != "Bearer ghs_console_token" {
			t.Errorf("job used the wrong GitHub credential: %q", a)
		}
	}
	jobs, _ := st.RecentJobs(context.Background(), 1)
	if len(jobs) != 1 {
		t.Fatalf("job not stored")
	}
	full, err := st.GetJob(context.Background(), jobs[0].ID)
	if err != nil || !strings.Contains(string(full.Result), "panic in library code") {
		t.Errorf("result not stored: %v %s", err, full.Result)
	}
}
