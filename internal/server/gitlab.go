package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/settings"
)

type glUser struct {
	Username string `json:"username"`
}

func hasUser(users []glUser, login string) bool {
	for _, u := range users {
		if login != "" && strings.EqualFold(u.Username, login) {
			return true
		}
	}
	return false
}

func usernames(users []glUser) string {
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}
	return strings.Join(names, ",")
}

type glPayload struct {
	ObjectKind string   `json:"object_kind"`
	Reviewers  []glUser `json:"reviewers"`
	Changes    struct {
		Reviewers *struct {
			Previous []glUser `json:"previous"`
			Current  []glUser `json:"current"`
		} `json:"reviewers"`
	} `json:"changes"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
		DefaultBranch     string `json:"default_branch"`
	} `json:"project"`
	ObjectAttributes struct {
		UpdatedAt  string `json:"updated_at"`
		LastCommit struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		State        string `json:"state"` // opened, closed, merged, locked
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		IID          int    `json:"iid"`
		ID           int64  `json:"id"`
		Action       string `json:"action"`
		OldRev       string `json:"oldrev"`
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
		DiscussionID string `json:"discussion_id"`
		System       bool   `json:"system"`
	} `json:"object_attributes"`
	MergeRequest struct {
		IID int `json:"iid"`
	} `json:"merge_request"`
}

// glEventKey identifies an event independently of the hook that delivered
// it. Each hook's delivery has its own webhook-id, but the same event has
// the same payload in every hook, so the payload itself is the key.
// (Field-based keys were too coarse: GitLab's update times have one-second
// resolution, so distinct quick changes collided.)
func glEventKey(host string, body []byte) string {
	sum := sha256.Sum256(body)
	return host + "|" + hex.EncodeToString(sum[:])
}

// gitlabCandidates are the GitLab hosts a delivery may come from, decided
// without reading the body: the webhook URL's host, the host GitLab names
// in X-Gitlab-Instance, or every enabled GitLab host (the delivery's
// secret then picks one). why is set when there is none.
func (s *Server) gitlabCandidates(r *http.Request, endpoint hosts.Host, viaEndpoint bool) (cands []hosts.Host, why string) {
	if viaEndpoint {
		return []hosts.Host{endpoint}, ""
	}
	if raw := r.Header.Get("X-Gitlab-Instance"); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			if h, ok := s.hosts.Find(hosts.GitLab, u.Host); ok {
				return []hosts.Host{h}, ""
			}
		}
		return nil, "unknown GitLab instance " + truncate(raw, 200)
	}
	return s.hosts.Enabled(hosts.GitLab), ""
}

// verifyGitLabHosts verifies a delivery against each candidate host and
// returns the one whose token or signing token matches.
func verifyGitLabHosts(h http.Header, body []byte, cands []hosts.Host, now time.Time) (inst hosts.Host, method, delivery string, err error) {
	for _, c := range cands {
		method, delivery, err = verifyGitLab(h, body, config.GitLab{WebhookSecrets: c.WebhookSecrets,
			SigningTokens: c.SigningTokens, RequireSignature: c.RequireSignature}, now)
		if err == nil {
			return c, method, delivery, nil
		}
	}
	if len(cands) > 1 {
		err = fmt.Errorf("no configured GitLab host's webhook secret or signing token matches (%d hosts tried; GitLab didn't send X-Gitlab-Instance)", len(cands))
	}
	return hosts.Host{}, "", "", err
}

func (s *Server) handleGitLab(w http.ResponseWriter, r *http.Request) {
	if len(s.hosts.Enabled(hosts.GitLab)) == 0 {
		http.Error(w, "gitlab is not configured", http.StatusNotFound)
		return
	}
	// Nothing from the body is read, parsed or recorded before the
	// delivery is verified: rejected deliveries keep transport facts only.
	hm := hookMetaOf(r.Context())
	hm.Event, hm.Delivery = truncate(r.Header.Get("X-Gitlab-Event"), 100), truncate(firstNonEmpty(r.Header.Get("Webhook-Id"), r.Header.Get("Idempotency-Key")), 100)
	signed := r.Header.Get("Webhook-Signature") != ""
	if !signed && r.Header.Get("X-Gitlab-Token") == "" {
		hm.Auth, hm.Reason = "failed", "no X-Gitlab-Token or Webhook-Signature header (set a secret token on the GitLab webhook)"
		http.Error(w, "invalid signature or token", http.StatusUnauthorized)
		return
	}
	endpoint, viaEndpoint, ok := s.endpointHost(w, r, hosts.GitLab)
	if !ok {
		return
	}
	cands, why := s.gitlabCandidates(r, endpoint, viaEndpoint)
	if why != "" {
		hm.Auth, hm.Reason = "failed", why
		s.logger.Warn("gitlab webhook from an unknown instance", "instance", truncate(r.Header.Get("X-Gitlab-Instance"), 200))
		http.Error(w, "unknown gitlab instance", http.StatusUnauthorized)
		return
	}
	if len(cands) == 1 {
		hm.Host = cands[0].Host
	}
	// A secret token is checked before the body is read; a signature
	// covers the body, so it is read first.
	var body []byte
	var err error
	if signed {
		if body, err = io.ReadAll(io.LimitReader(r.Body, maxWebhookBody)); err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
	}
	inst, method, delivery, err := verifyGitLabHosts(r.Header, body, cands, time.Now())
	if err != nil {
		hm.Auth, hm.Reason = "failed", err.Error()
		s.logger.Warn("gitlab webhook rejected", "hosts", len(cands), "reason", err.Error())
		http.Error(w, "invalid signature or token", http.StatusUnauthorized)
		return
	}
	hm.Host = inst.Host
	if !signed {
		if body, err = io.ReadAll(io.LimitReader(r.Body, maxWebhookBody)); err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
	}
	var p glPayload
	if err := json.Unmarshal(body, &p); err != nil {
		hm.Auth = method
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	hm.Project, hm.Kind, hm.Action, hm.Sender = p.Project.PathWithNamespace, p.ObjectKind, p.ObjectAttributes.Action, p.User.Username
	hm.Number = firstNonZero(p.MergeRequest.IID, p.ObjectAttributes.IID)
	if p.ObjectKind == "note" {
		hm.Number = p.MergeRequest.IID
	}
	hm.Auth = method
	if delivery != "" && !s.glDeliveries.first(delivery, time.Now()) {
		s.logger.Info("gitlab webhook already processed", "delivery", delivery)
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate delivery"})
		return
	}
	host := inst.Host
	tgt := s.gitlabTarget(inst)
	event := r.Header.Get("X-Gitlab-Event")
	log := s.logger.With("event", event, "project", p.Project.PathWithNamespace)
	log.Info("gitlab webhook received", "kind", p.ObjectKind, "action", p.ObjectAttributes.Action,
		"new_commits", p.ObjectAttributes.OldRev != "", "auth", method, "reviewers", usernames(p.Reviewers))
	s.seen(r.Context(), vcs.GitLab, host, p.Project.PathWithNamespace)
	// One event can arrive through a system hook and a group or project
	// hook; each delivery has its own webhook-id, so dedupe on the event.
	if fp := glEventKey(host, body); !s.glEvents.first(fp, time.Now()) {
		log.Info("same event already received through another hook")
		hm.Reason = "same event already received through another hook"
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate delivery"})
		return
	}

	switch p.ObjectKind {
	case "merge_request":
		a := p.ObjectAttributes
		ref := vcs.Ref{Platform: vcs.GitLab, Host: host, Repo: p.Project.PathWithNamespace, Number: a.IID}
		if a.Action == "close" || a.Action == "merge" {
			if s.queue.Cancel("review:"+ref.String(), "merge request "+a.State+" before the review started") {
				ignored(w, "merge request "+a.State+"; cancelled its waiting review")
				return
			}
			ignored(w, "merge request action "+a.Action)
			return
		}
		if a.State != "" && a.State != "opened" {
			ignored(w, "merge request is "+a.State)
			return
		}
		acc := s.access(r.Context(), ref)
		if sync, why := settings.BranchSync(acc.SkipBranchSync, s.settings.Runtime(r.Context()).SyncSourceBranches,
			a.SourceBranch, a.TargetBranch, p.Project.DefaultBranch); sync {
			ignored(w, why)
			return
		}
		requireReviewer := acc.RequireReviewer
		self := s.reviewerFor(tgt, host)
		requested := false // the bot was just added as a reviewer
		if c := p.Changes.Reviewers; c != nil {
			requested = hasUser(c.Current, self) && !hasUser(c.Previous, self)
		}
		// "update" fires for title/label/reviewer edits too; only new commits
		// set oldrev. Being requested as a reviewer also starts a review.
		trigger := a.Action == "open" || a.Action == "reopen" || (a.Action == "update" && a.OldRev != "") ||
			(requireReviewer && a.Action == "update" && requested)
		if !trigger {
			ignored(w, "merge request action "+a.Action)
			return
		}
		if requireReviewer {
			if self == "" {
				log.Warn("require_reviewer is on but the bot user is unknown: set reviewer for this instance or check the token")
				ignored(w, "bot user unknown; cannot check reviewers")
				return
			}
			if !hasUser(p.Reviewers, self) && !requested {
				ignored(w, "@"+self+" is not a reviewer on this merge request")
				return
			}
		}
		if !acc.Enabled {
			ignored(w, "repository is not enabled in the admin panel")
			return
		}
		if err := s.enqueueReview(tgt, ref, reviewOpts{Auto: true, Overrides: s.overrides(acc)}, true); err != nil {
			submitErr(w, err)
			return
		}
		log.Info("review queued", "mr", ref.String())
		accepted(w, "review")

	case "note":
		a := p.ObjectAttributes
		if a.NoteableType != "MergeRequest" || a.System || isBotOutput(a.Note) || s.isSelf(host, p.User.Username) {
			ignored(w, "not a user comment on a merge request")
			return
		}
		ref := vcs.Ref{Platform: vcs.GitLab, Host: host, Repo: p.Project.PathWithNamespace, Number: p.MergeRequest.IID}
		sourceRef := "gitlab|" + host + "|note|" + idString(a.ID)
		cmd, ok := parseCommand(tgt.botName, a.Note)
		if !ok {
			if a.DiscussionID != "" {
				// A reply in a thread: if the thread is the bot's comment,
				// the reply may hold a team learning.
				did := a.DiscussionID
				s.replyToBot(tgt, ref, func(ctx context.Context) (parentComment, error) {
					return s.gitlabDiscussionStart(ctx, tgt, ref, did)
				}, s.gitlabReact(tgt, ref, a.ID), a.Note, p.User.Username, sourceRef)
				ignored(w, "not a command (replies to the bot are checked for learnings)")
				return
			}
			ignored(w, "bot not mentioned")
			return
		}
		cmd.author, cmd.sourceRef = p.User.Username, sourceRef
		allowed, ov := s.gate(r.Context(), ref)
		if !allowed {
			ignored(w, "repository is not enabled in the admin panel")
			return
		}
		go func() {
			if err := tgt.client.React(context.Background(), &vcs.PR{Ref: ref}, strconv.FormatInt(a.ID, 10), "eyes"); err != nil {
				log.Debug("reaction failed", "error", err)
			}
		}()
		if err := s.dispatch(tgt, ref, cmd, ov); err != nil {
			submitErr(w, err)
			return
		}
		log.Info("command queued", "mr", ref.String(), "command", cmd.kind)
		accepted(w, cmd.kind)

	default:
		ignored(w, "event "+p.ObjectKind)
	}
}
