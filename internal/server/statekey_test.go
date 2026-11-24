package server

import (
	"context"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
)

// Jobs get a state key derived per code host from the console's secret
// key (code-review-bot#21), and none without one.
func TestJobStateKey(t *testing.T) {
	box, err := secrets.New("a test passphrase that is long enough for a key")
	if err != nil {
		t.Fatal(err)
	}
	a := hosts.Host{Platform: hosts.GitLab, Host: "gitlab.example.com", Token: "t"}
	b := hosts.Host{Platform: hosts.GitLab, Host: "gitlab.other.com", Token: "t"}
	env := func(s *Server, h hosts.Host) map[string]string {
		return s.withStateKey(h, map[string]string{"GITLAB_TOKEN": h.Token})
	}
	s := &Server{box: box}
	k1, k2 := env(s, a)["CODEREVIEW_STATE_KEY"], env(s, b)["CODEREVIEW_STATE_KEY"]
	if len(k1) != 64 || k1 == k2 || env(s, a)["GITLAB_TOKEN"] != "t" {
		t.Errorf("a 32-byte hex key per host: %q %q", k1, k2)
	}
	if again := env(&Server{box: box}, a)["CODEREVIEW_STATE_KEY"]; again != k1 {
		t.Error("the same console key gives the same host key (across restarts)")
	}
	other, _ := secrets.New("another test passphrase, also long enough!!")
	if env(&Server{box: other}, a)["CODEREVIEW_STATE_KEY"] == k1 {
		t.Error("another console key, another host key")
	}
	if _, ok := env(&Server{}, a)["CODEREVIEW_STATE_KEY"]; ok {
		t.Error("no console key: no state key")
	}
	if env(s, hosts.Host{Platform: hosts.Bitbucket, Host: "gitlab.example.com"})["CODEREVIEW_STATE_KEY"] == k1 {
		t.Error("the platform is part of the label")
	}
}

// The job secrets of each code host carry it.
func TestJobSecretsCarryStateKey(t *testing.T) {
	s := testServer(t)
	s.box, _ = secrets.New("a test passphrase that is long enough for a key")
	for _, tgt := range []target{
		s.gitlabTarget(hosts.Host{Platform: hosts.GitLab, Host: "gitlab.example.com", Token: "t"}, ""),
		s.githubTarget(hosts.Host{Platform: hosts.GitHub, Host: "github.com", Token: "t"}, 0, ""),
		s.bitbucketTarget(hosts.Host{Platform: hosts.Bitbucket, Host: "bitbucket.org", Token: "t"}, ""),
	} {
		if m, err := tgt.secrets(context.Background()); err != nil || len(m["CODEREVIEW_STATE_KEY"]) != 64 {
			t.Errorf("%s: %v %v", tgt.host.Platform, m, err)
		}
	}
}

// The job config names the PR's host when the API URL implies another
// (code-review-bot#31), and only then.
func TestJobConfigPRHost(t *testing.T) {
	s := testServer(t)
	cfgFor := func(tgt target, prHost string) string {
		y, _, err := s.jobConfig(context.Background(), tgt, prHost)
		if err != nil {
			t.Fatal(err)
		}
		return string(y)
	}
	gl := s.gitlabTarget(hosts.Host{Platform: hosts.GitLab, Host: "gitlab-webservice.gitlab.svc:8181", BaseURL: "http://gitlab-webservice.gitlab.svc:8181", Token: "t"}, "")
	if y := cfgFor(gl, "gitlab.example.com"); !strings.Contains(y, "host: gitlab.example.com") {
		t.Errorf("internal GitLab URL, public MR host:\n%s", y)
	}
	if y := cfgFor(gl, "gitlab-webservice.gitlab.svc:8181"); strings.Contains(y, "host:") {
		t.Errorf("same host: no host key:\n%s", y)
	}
	gh := s.githubTarget(hosts.Host{Platform: hosts.GitHub, Host: "github.com", BaseURL: "https://api.github.com", Token: "t"}, 0, "")
	if y := cfgFor(gh, "github.com"); strings.Contains(y, "github:") {
		t.Errorf("github.com: nothing to write:\n%s", y)
	}
	ghe := s.githubTarget(hosts.Host{Platform: hosts.GitHub, Host: "api.octocorp.ghe.com", BaseURL: "https://api.octocorp.ghe.com", Token: "t"}, 0, "")
	if y := cfgFor(ghe, "octocorp.ghe.com"); strings.Contains(y, "host:") || !strings.Contains(y, "api_url: https://api.octocorp.ghe.com") {
		t.Errorf("ghe.com maps by itself:\n%s", y)
	}
	proxy := s.githubTarget(hosts.Host{Platform: hosts.GitHub, Host: "127.0.0.1:9999", BaseURL: "http://127.0.0.1:9999", Token: "t"}, 0, "")
	if y := cfgFor(proxy, "github.com"); !strings.Contains(y, "host: github.com") {
		t.Errorf("API under another name:\n%s", y)
	}
}
