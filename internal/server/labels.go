package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/hosts"
)

// RepoLabels are the labels a repository's pull requests can use (the
// project's and its groups' on GitLab, the repository's on GitHub), read
// with the credential its reviews use. The admin panel shows them while
// an admin picks label patterns (issue #162).
func (s *Server) RepoLabels(ctx context.Context, platform, host, repo string) ([]vcs.Label, error) {
	h, ok := s.hosts.Find(platform, host)
	if !ok {
		return nil, fmt.Errorf("no enabled %s host for %s: add it under Code hosts", platform, host)
	}
	if platform == hosts.Bitbucket {
		return nil, hosts.ErrNoLabels
	}
	var installation int64
	if app := s.ghApp(h.ID); platform == hosts.GitHub && app != nil && s.hosts.ForRepo(h, repo).Credential == 0 {
		id, err := repoInstallation(ctx, app, h.BaseURL, repo)
		if err != nil {
			return nil, err
		}
		installation = id
	}
	tgt, err := s.targetOf(h, installation, repo)
	if err != nil {
		return nil, err
	}
	l, ok := tgt.client.(vcs.Labeler)
	if !ok {
		return nil, hosts.ErrNoLabels
	}
	return l.AvailableLabels(ctx, &vcs.PR{Repo: repo})
}

// repoInstallation is the GitHub App installation that covers repo: webhooks
// carry it, but the admin panel asks without one.
func repoInstallation(ctx context.Context, app *vcs.GitHubApp, apiURL, repo string) (int64, error) {
	jwt, err := app.JWT(time.Now())
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/repos/"+repo+"/installation", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("the GitHub App is not installed on %s", repo)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("github: looking up the App installation for %s: HTTP %d", repo, resp.StatusCode)
	}
	var v struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.ID == 0 {
		return 0, fmt.Errorf("github: no App installation for %s", repo)
	}
	return v.ID, nil
}
