package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
)

type repoLabelsReq struct {
	Platform string `json:"platform"`
	Host     string `json:"host"`
	Repo     string `json:"repo"`
}

// apiRepoLabels lists the labels a repository's pull requests can use,
// for the label picker in review settings (issue #162). Score labels the
// bot sets itself are left out: they are never suggested. An operator
// action: it calls the code host with the bot's token.
func (a *Admin) apiRepoLabels(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req repoLabelsReq
	if !decode(w, r, &req) {
		return
	}
	req.Repo = strings.Trim(strings.TrimSpace(req.Repo), "/")
	if !validPath(req.Repo) || !hostRe.MatchString(req.Host) || req.Platform == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick a code host and a repository path like group/project"})
		return
	}
	if a.Labels == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the label picker is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	labels, err := a.Labels.RepoLabels(ctx, req.Platform, req.Host, req.Repo)
	if errors.Is(err, hosts.ErrNoLabels) {
		// Bitbucket: only exact names in the patterns can be suggested.
		writeJSON(w, http.StatusOK, map[string]any{"listable": false, "labels": []vcs.Label{}})
		return
	}
	if err != nil {
		a.logger.Info("could not read repository labels", "by", s.Email, "repo", req.Platform+":"+req.Host+"/"+req.Repo, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not read the labels of " + req.Repo + ": " + err.Error()})
		return
	}
	out := make([]vcs.Label, 0, len(labels))
	for _, l := range labels {
		if !vcs.IsScoreLabel(l.Name) {
			out = append(out, l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"listable": true, "labels": out})
}
