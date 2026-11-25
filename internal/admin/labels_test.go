package admin

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/execsettings"
	"github.com/divmora/code-review-console/internal/hosts"
)

type fakeLabels struct {
	got    []string
	labels []vcs.Label
	err    error
}

func (f *fakeLabels) RepoLabels(_ context.Context, platform, host, repo string) ([]vcs.Label, error) {
	f.got = append(f.got, platform+":"+host+"/"+repo)
	return f.labels, f.err
}

// TestLabelSettings: label settings need code-review-bot 0.8.0 on the
// Executor, like flow diagrams need 0.7.0 (issue #162).
func TestLabelSettings(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	repo, _ := e.st.CreateRepo(context.Background(), "gitlab", "g.example", "team/app", "enabled", "a@x")
	path := "/admin/api/repos/" + strconv.FormatInt(repo.ID, 10)
	labels := map[string]any{"mode": "apply", "allow": []string{"type::*"}, "suggest_only": []string{"priority::*"}, "max": 3}
	e.adm.Executor = execsettings.New(config.Executor{Type: "docker", Docker: config.DockerExecutor{Image: "ghcr.io/divmora/code-review-bot:0.7.0"}}, e.st)
	if code, body := e.do(t, "PUT", path, csrf, map[string]any{"mode": "enabled", "settings": map[string]any{"labels": labels}}); code != 400 || !bytes.Contains(body, []byte("labels need code-review-bot 0.8.0")) {
		t.Errorf("0.7.0: %d %s", code, body)
	}
	// A diagram alone is fine on 0.7.0.
	if code, body := e.do(t, "PUT", path, csrf, map[string]any{"mode": "enabled", "settings": map[string]any{"diagram": "auto"}}); code != 200 {
		t.Errorf("diagram on 0.7.0: %d %s", code, body)
	}
	e.adm.Executor = execsettings.New(config.Executor{Type: "docker", Docker: config.DockerExecutor{Image: "ghcr.io/divmora/code-review-bot:0.8.0"}}, e.st)
	code, body := e.do(t, "PUT", path, csrf, map[string]any{"mode": "enabled", "settings": map[string]any{"labels": labels}})
	if code != 200 || !bytes.Contains(body, []byte(`"labels":{"mode":"apply","allow":["type::*"],"suggest_only":["priority::*"],"max":3}`)) {
		t.Errorf("0.8.0: %d %s", code, body)
	}
	if code, _ := e.do(t, "PUT", path, csrf, map[string]any{"mode": "enabled", "settings": map[string]any{"labels": map[string]any{"mode": "always"}}}); code != 400 {
		t.Errorf("invalid mode: %d", code)
	}
}

// TestRepoLabels: the label picker reads a repository's labels through the
// server, without the score labels the bot sets itself.
func TestRepoLabels(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}, ViewerEmails: []string{"view@example.com"}})
	as := func(email string) string {
		e.idp.as(map[string]any{"sub": email, "email": email, "email_verified": true})
		_ = e.login(t).Body.Close()
		return e.csrf(t)
	}
	csrf := as("alice@example.com")
	req := map[string]any{"platform": "gitlab", "host": "g.example", "repo": "team/app"}
	if code, _ := e.do(t, "POST", "/admin/api/repo-labels", csrf, req); code != 503 {
		t.Errorf("no server: %d", code)
	}
	fl := &fakeLabels{labels: []vcs.Label{{Name: "type::bug", Description: "Something broke"}, {Name: "Quality Score::85"}, {Name: "backend"}}}
	e.adm.Labels = fl
	code, body := e.do(t, "POST", "/admin/api/repo-labels", csrf, req)
	if code != 200 || !bytes.Contains(body, []byte(`"listable":true`)) || !bytes.Contains(body, []byte(`"description":"Something broke"`)) ||
		bytes.Contains(body, []byte("Quality Score")) || !bytes.Contains(body, []byte(`"backend"`)) {
		t.Errorf("labels: %d %s", code, body)
	}
	for _, bad := range []map[string]any{
		{"platform": "gitlab", "host": "g.example", "repo": "../etc"},
		{"platform": "gitlab", "host": "g.example", "repo": "app"},
		{"platform": "gitlab", "host": "g.example/x", "repo": "team/app"},
	} {
		if code, _ := e.do(t, "POST", "/admin/api/repo-labels", csrf, bad); code != 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if len(fl.got) != 1 || fl.got[0] != "gitlab:g.example/team/app" {
		t.Errorf("asked for %v", fl.got)
	}
	fl.err = hosts.ErrNoLabels
	if _, body := e.do(t, "POST", "/admin/api/repo-labels", csrf, req); !bytes.Contains(body, []byte(`"listable":false`)) {
		t.Errorf("Bitbucket: %s", body)
	}
	fl.err = errors.New("HTTP 404")
	if code, body := e.do(t, "POST", "/admin/api/repo-labels", csrf, req); code != 502 || !bytes.Contains(body, []byte("HTTP 404")) {
		t.Errorf("host error: %d %s", code, body)
	}
	// It calls the code host with the bot's token: not for viewers.
	csrf = as("view@example.com")
	if code, _ := e.do(t, "POST", "/admin/api/repo-labels", csrf, req); code != 403 {
		t.Errorf("viewer: %d", code)
	}
}
