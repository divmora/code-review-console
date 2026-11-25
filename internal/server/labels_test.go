package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
)

// TestRepoLabels: the label picker reads a repository's labels with the
// credential its reviews use: a GitLab token, or a GitHub App's
// installation found for the repository (issue #162).
func TestRepoLabels(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.EscapedPath(), "/projects/team%2Fapp/labels") {
			mu.Lock()
			auth = append(auth, r.Header.Get("PRIVATE-TOKEN"))
			mu.Unlock()
			_, _ = io.WriteString(w, `[{"name":"type::bug","description":"Something broke"},{"name":"backend"}]`)
			return
		}
		http.NotFound(w, r)
	}))
	defer gl.Close()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/app/installation":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				http.Error(w, "no JWT", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"id":42}`)
		case "/app/installations/42/access_tokens":
			_, _ = io.WriteString(w, `{"token":"inst-token","expires_at":"2099-01-01T00:00:00Z"}`)
		case "/repos/o/app/labels":
			mu.Lock()
			auth = append(auth, r.Header.Get("Authorization"))
			mu.Unlock()
			_, _ = io.WriteString(w, `[{"name":"bug"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: gl.URL, Token: "gl-token", WebhookSecret: "s"}}
	cfg.GitHub = config.GitHub{APIURL: gh.URL, AppID: 7, PrivateKey: pemKey}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	fx := &fakeExec{res: &executor.Result{Model: "fake:model", Labels: []string{"type::bug", "priority::high"}, LabelsApplied: []string{"type::bug"}, LabelsTokens: 430}}
	s, err := newHooked(t, cfg, fx, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	ctx := context.Background()
	glh := s.hosts.Enabled(hosts.GitLab)[0]
	ls, err := s.RepoLabels(ctx, hosts.GitLab, glh.Host, "team/app")
	if err != nil || len(ls) != 2 || ls[0].Description != "Something broke" {
		t.Fatalf("gitlab: %+v %v", ls, err)
	}
	ghh := s.hosts.Enabled(hosts.GitHub)[0]
	if ls, err := s.RepoLabels(ctx, hosts.GitHub, ghh.Host, "o/app"); err != nil || len(ls) != 1 || ls[0].Name != "bug" {
		t.Fatalf("github app: %+v %v", ls, err)
	}
	if _, err := s.RepoLabels(ctx, hosts.GitHub, ghh.Host, "o/other"); err == nil || !strings.Contains(err.Error(), "not installed on o/other") {
		t.Errorf("not installed: %v", err)
	}
	mu.Lock()
	if strings.Join(auth, ",") != "gl-token,Bearer inst-token" && strings.Join(auth, ",") != "gl-token,token inst-token" {
		t.Errorf("credentials: %v", auth)
	}
	mu.Unlock()
	if _, err := s.RepoLabels(ctx, hosts.Bitbucket, "bitbucket.org", "w/r"); err == nil {
		t.Error("no Bitbucket host")
	}

	// A review records the labels it picked and set.
	ref := vcs.Ref{Platform: vcs.GitLab, Host: glh.Host, Repo: "team/app", Number: 5}
	if err := s.enqueueReview(s.gitlabTarget(glh, ref.Repo), ref, reviewOpts{By: "admin@x"}, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(s.queue.Snapshot().Recent) == 1 })
	rec := s.queue.Snapshot().Recent[0]
	if strings.Join(rec.Labels, ",") != "type::bug,priority::high" || strings.Join(rec.LabelsApplied, ",") != "type::bug" || rec.LabelsTokens != 430 {
		t.Errorf("record: %+v", rec.Report)
	}
}
