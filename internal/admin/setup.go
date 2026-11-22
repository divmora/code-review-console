package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
)

// setupStep is one step of the guided setup. Done is computed from live
// data each time, so the checklist never drifts from what is configured.
type setupStep struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Help     string `json:"help"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional,omitempty"`
	// Detail is the current state in a few words ("2 hosts", "last
	// delivery rejected: ...").
	Detail string `json:"detail,omitempty"`
	// Link is the admin section that completes the step ("" = none yet).
	Link string `json:"link,omitempty"`
}

type setupStatus struct {
	Steps []setupStep `json:"steps"`
	// Done and Total count the required steps; Complete is Done == Total.
	Done     int  `json:"done"`
	Total    int  `json:"total"`
	Complete bool `json:"complete"`
}

func (a *Admin) apiSetup(w http.ResponseWriter, r *http.Request, s *store.Session) {
	st, err := a.setup(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// setup checks each step against the database, the hosts and the license.
func (a *Admin) setup(ctx context.Context) (setupStatus, error) {
	var steps []setupStep

	// 1. License (optional: the free core reviews without one).
	ls := a.Lic.Status()
	lic := setupStep{ID: "license", Title: "Add a license", Optional: true, Link: "/license",
		Help: "The free core reviews without one. A Divmora license turns on paid features such as more hosts, rules, learnings and the Docker and Kubernetes executors."}
	switch ls.State {
	case license.Active:
		lic.Done, lic.Detail = true, "active"
		if !ls.Expires.IsZero() {
			lic.Detail += " until " + ls.Expires.Format("2 Jan 2006")
		}
	case license.InGrace:
		lic.Done, lic.Detail = true, "expired: in its grace period, renew it"
	case license.Expired:
		lic.Detail = "expired: running the free core"
	case license.Invalid:
		lic.Detail = "not valid: running the free core"
	default:
		lic.Detail = "free core"
	}
	steps = append(steps, lic)

	// 2. A code host, and 3. a webhook on it.
	var active []hosts.Host
	if a.Hosts != nil {
		active = a.Hosts.Enabled("")
	}
	host := setupStep{ID: "host", Title: "Add a code host", Link: "/hosts",
		Help: "Connect GitHub, GitLab or Bitbucket with a token the bot comments as. Use Test on the host to check the token."}
	if len(active) > 0 {
		host.Done, host.Detail = true, plural(len(active), "host")
	}
	steps = append(steps, host)

	hook := setupStep{ID: "webhook", Title: "Add a webhook", Link: "/hosts",
		Help: "On the host, open Manage webhooks and add one. Then create the webhook on the code host with the URL and secret the console shows."}
	hooks := 0
	for _, h := range active {
		hooks += len(a.Hosts.Endpoints(h.ID))
		if len(h.WebhookSecrets)+len(h.SigningTokens) > 0 {
			hooks++ // the shared URL is set up for this host
		}
	}
	if hooks > 0 {
		hook.Done, hook.Detail = true, plural(hooks, "webhook")
	}
	steps = append(steps, hook)

	// 4. A verified delivery: proves the code host reaches the console and
	// the secret matches.
	delivery := setupStep{ID: "delivery", Title: "Receive a webhook", Link: "/webhooks",
		Help: "Open or update a merge request, or use the code host's Test button on the webhook. The delivery shows up on the Webhooks page."}
	events, err := a.store.ListWebhookEvents(ctx, store.WebhookFilter{Limit: 500})
	if err != nil {
		return setupStatus{}, err
	}
	var lastFailed *store.WebhookEvent
	for i, e := range events { // newest first
		if e.Auth == "signature" || e.Auth == "token" {
			delivery.Done, delivery.Detail = true, "last verified "+ago(e.ReceivedAt)
			break
		}
		if lastFailed == nil && e.Auth == "failed" {
			lastFailed = &events[i]
		}
	}
	if !delivery.Done && lastFailed != nil {
		delivery.Detail = "last delivery rejected: " + lastFailed.Reason
	}
	steps = append(steps, delivery)

	// 5. A model.
	model := setupStep{ID: "model", Title: "Set up a model",
		Help: "Reviews need at least one model profile: add it under models: in the config file (provider, model and the env var holding the API key), set model: to it, and restart."}
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		return setupStatus{}, err
	}
	if n := len(a.profiles) + len(profiles); n > 0 {
		model.Done, model.Detail = true, plural(n, "model profile")
	}
	steps = append(steps, model)

	// 6. A repository that will be reviewed.
	repo := setupStep{ID: "repo", Title: "Enable a repository", Link: "/repositories",
		Help: "Repositories appear after their first webhook (or use Add repository). Turn one on to have its merge requests reviewed."}
	eff := a.settings().Runtime(ctx)
	if !eff.Allowlist {
		repo.Done, repo.Detail = true, "allowlist off: every repository is reviewed"
	} else {
		repos, err := a.store.ListRepos(ctx)
		if err != nil {
			return setupStatus{}, err
		}
		views, err := a.view(ctx, repos)
		if err != nil {
			return setupStatus{}, err
		}
		n := 0
		for _, v := range views {
			if v.Effective.Enabled {
				n++
			}
		}
		if n > 0 {
			repo.Done, repo.Detail = true, plural(n, "repository")+" reviewed"
		} else if len(repos) > 0 {
			repo.Detail = plural(len(repos), "repository") + " seen, none on"
		}
	}
	steps = append(steps, repo)

	// 7. A finished review.
	review := setupStep{ID: "review", Title: "See the first review", Link: "/reviews",
		Help: "Open a merge request (or push to one) in an enabled repository, or use Review now on the Repositories page."}
	jobs, err := a.store.ListJobs(ctx, store.JobFilter{Limit: 50})
	if err != nil {
		return setupStatus{}, err
	}
	for _, j := range jobs {
		if j.Kind == "review" && j.State == "done" {
			review.Done, review.Detail = true, "last on "+j.Repo+" "+ago(j.Finished)
			break
		}
	}
	if !review.Done && len(jobs) > 0 && jobs[0].State == "failed" {
		review.Detail = "last review failed: " + jobs[0].Error
	}
	steps = append(steps, review)

	out := setupStatus{Steps: steps}
	for _, st := range steps {
		if !st.Optional {
			out.Total++
			if st.Done {
				out.Done++
			}
		}
	}
	out.Complete = out.Done == out.Total
	return out, nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if noun == "repository" {
		return fmt.Sprintf("%d repositories", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
