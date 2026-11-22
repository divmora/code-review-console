package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/settings"
)

const maxWebhookBody = 25 << 20

// verifyGitHub checks X-Hub-Signature-256.
func verifyGitHub(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || secret == "" {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

type ghPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName      string `json:"full_name"`
		HTMLURL       string `json:"html_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
	Number      int `json:"number"`
	PullRequest struct {
		Number int    `json:"number"`
		State  string `json:"state"` // open, closed
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
	} `json:"pull_request"`
	RequestedReviewer struct {
		Login string `json:"login"`
	} `json:"requested_reviewer"`
	Issue struct {
		Number      int       `json:"number"`
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		ID        int64  `json:"id"`
		InReplyTo int64  `json:"in_reply_to_id"`
		Body      string `json:"body"`
		User      struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
}

func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	hm := hookMetaOf(r.Context())
	hm.Event, hm.Delivery = r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	// The payload names the host (github.com or a GitHub Enterprise
	// server); it is only trusted once that host's secret verifies it.
	var p ghPayload
	_ = json.Unmarshal(body, &p)
	host := ""
	h, viaEndpoint, ok := s.endpointHost(w, r, hosts.GitHub)
	if !ok {
		return
	}
	if viaEndpoint {
		host = h.Host // the URL names the host
	} else {
		if u, err := url.Parse(p.Repository.HTMLURL); err == nil && u.Host != "" {
			host = strings.ToLower(u.Host)
		}
		h, ok = s.hosts.Find(hosts.GitHub, host)
		if !ok {
			h, ok = s.hosts.Find(hosts.GitHub, "")
		}
	}
	hm.Host = firstNonEmpty(host, h.Host)
	if !ok {
		hm.Auth, hm.Reason = "failed", "no GitHub host configured for "+firstNonEmpty(host, "this delivery")
		http.Error(w, "unknown github host", http.StatusUnauthorized)
		return
	}
	verified := false
	for _, secret := range h.WebhookSecrets {
		verified = verified || verifyGitHub(secret, body, r.Header.Get("X-Hub-Signature-256"))
	}
	if !verified {
		hm.Auth, hm.Reason = "failed", "X-Hub-Signature-256 does not match the webhook secret of "+h.Host
		s.logger.Warn("github webhook signature mismatch", "host", h.Host, "delivery", r.Header.Get("X-GitHub-Delivery"))
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	hm.Auth = "signature"
	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	if p.Repository.FullName == "" {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	host = firstNonEmpty(host, h.Host)
	hm.Host, hm.Project, hm.Action, hm.Sender = host, p.Repository.FullName, p.Action, p.Sender.Login
	hm.Kind, hm.Number = event, firstNonZero(p.Number, p.PullRequest.Number, p.Issue.Number)
	s.seen(r.Context(), vcs.GitHub, host, p.Repository.FullName)
	tgt := s.githubTarget(h, p.Installation.ID)
	log := s.logger.With("event", event, "action", p.Action, "repo", p.Repository.FullName, "delivery", r.Header.Get("X-GitHub-Delivery"))

	switch event {
	case "pull_request":
		if p.Action == "closed" {
			ref := vcs.Ref{Platform: vcs.GitHub, Host: host, Repo: p.Repository.FullName, Number: firstNonZero(p.Number, p.PullRequest.Number)}
			if s.queue.Cancel("review:"+ref.String(), "pull request closed before the review started") {
				ignored(w, "pull request closed; cancelled its waiting review")
				return
			}
		}
		if p.PullRequest.State == "closed" {
			ignored(w, "pull request is closed")
			return
		}
		ref := vcs.Ref{Platform: vcs.GitHub, Host: host, Repo: p.Repository.FullName, Number: firstNonZero(p.Number, p.PullRequest.Number)}
		acc := s.access(r.Context(), ref)
		if sync, why := settings.BranchSync(acc.SkipBranchSync, s.settings.Runtime(r.Context()).SyncSourceBranches,
			p.PullRequest.Head.Ref, p.PullRequest.Base.Ref, p.Repository.DefaultBranch); sync {
			ignored(w, why)
			return
		}
		requireReviewer := acc.RequireReviewer
		self := s.reviewerFor(tgt, h.Host)
		requested := p.Action == "review_requested" && self != "" && strings.EqualFold(p.RequestedReviewer.Login, self)
		switch p.Action {
		case "opened", "reopened", "synchronize", "ready_for_review":
		default:
			if !requireReviewer || !requested {
				ignored(w, "pull_request action "+p.Action)
				return
			}
		}
		if requireReviewer && !requested {
			if self == "" {
				ignored(w, "bot user unknown; cannot check reviewers (GitHub Apps cannot be requested as reviewers)")
				return
			}
			isReviewer := false
			for _, r := range p.PullRequest.RequestedReviewers {
				isReviewer = isReviewer || strings.EqualFold(r.Login, self)
			}
			if !isReviewer {
				ignored(w, "@"+self+" is not a requested reviewer on this pull request")
				return
			}
		}
		ov := s.overrides(acc)
		if !acc.Enabled {
			ignored(w, "repository is not enabled in the admin panel")
			return
		}
		if err := s.enqueueReview(tgt, ref, reviewOpts{Auto: true, Overrides: ov}, true); err != nil {
			submitErr(w, err)
			return
		}
		log.Info("review queued", "pr", ref.String())
		accepted(w, "review")

	case "issue_comment":
		if p.Action != "created" || p.Issue.PullRequest == nil {
			ignored(w, "not a new pull request comment")
			return
		}
		if p.Comment.User.Type == "Bot" || p.Sender.Type == "Bot" || isBotOutput(p.Comment.Body) || s.isSelf(h.Host, p.Comment.User.Login) {
			ignored(w, "comment from a bot")
			return
		}
		cmd, ok := parseCommand(tgt.botName, p.Comment.Body)
		if !ok {
			ignored(w, "bot not mentioned")
			return
		}
		cmd.author, cmd.sourceRef = p.Comment.User.Login, "github|"+host+"|comment|"+idString(p.Comment.ID)
		ref := vcs.Ref{Platform: vcs.GitHub, Host: host, Repo: p.Repository.FullName, Number: p.Issue.Number}
		allowed, ov := s.gate(r.Context(), ref)
		if !allowed {
			ignored(w, "repository is not enabled in the admin panel")
			return
		}
		go func() {
			if err := tgt.client.React(context.Background(), &vcs.PR{Ref: ref}, itoa(p.Comment.ID), "eyes"); err != nil {
				log.Debug("reaction failed", "error", err)
			}
		}()
		if err := s.dispatch(tgt, ref, cmd, ov); err != nil {
			submitErr(w, err)
			return
		}
		log.Info("command queued", "pr", ref.String(), "command", cmd.kind)
		accepted(w, cmd.kind)

	case "pull_request_review_comment":
		// Replies in a review thread: commands, or answers to the bot that
		// may hold a team learning.
		if p.Action != "created" || p.Comment.User.Type == "Bot" || isBotOutput(p.Comment.Body) || s.isSelf(h.Host, p.Comment.User.Login) {
			ignored(w, "not a new review comment from a person")
			return
		}
		ref := vcs.Ref{Platform: vcs.GitHub, Host: host, Repo: p.Repository.FullName, Number: p.PullRequest.Number}
		sourceRef := "github|" + host + "|review_comment|" + idString(p.Comment.ID)
		if cmd, ok := parseCommand(tgt.botName, p.Comment.Body); ok {
			allowed, ov := s.gate(r.Context(), ref)
			if !allowed {
				ignored(w, "repository is not enabled in the admin panel")
				return
			}
			cmd.author, cmd.sourceRef = p.Comment.User.Login, sourceRef
			if err := s.dispatch(tgt, ref, cmd, ov); err != nil {
				submitErr(w, err)
				return
			}
			accepted(w, cmd.kind)
			return
		}
		if p.Comment.InReplyTo == 0 {
			ignored(w, "bot not mentioned")
			return
		}
		parent := p.Comment.InReplyTo
		s.replyToBot(tgt, ref, func(ctx context.Context) (parentComment, error) { return s.githubComment(ctx, tgt, ref, parent) },
			s.githubReviewReact(tgt, ref, p.Comment.ID), p.Comment.Body, p.Comment.User.Login, sourceRef)
		ignored(w, "not a command (replies to the bot are checked for learnings)")

	default:
		ignored(w, "event "+event)
	}
}

// isSelf reports whether login is the token user (PAT mode), compared
// case-insensitively. In App mode the bot is detected by user type.
func (s *Server) isSelf(host, login string) bool {
	self := s.self(host)
	return self != "" && strings.EqualFold(self, login)
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
