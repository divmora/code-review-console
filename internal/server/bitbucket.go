package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/settings"
)

// Bitbucket Cloud webhooks. Bitbucket signs each delivery with the
// webhook's secret: X-Hub-Signature: sha256=<hex HMAC of the body>.

type bbUser struct {
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
	AccountID   string `json:"account_id"`
}

func (u bbUser) is(login string) bool {
	return login != "" && (strings.EqualFold(u.Nickname, login) || u.AccountID == login || strings.EqualFold(u.DisplayName, login))
}

type bbPayload struct {
	Actor      bbUser `json:"actor"`
	Repository struct {
		FullName string `json:"full_name"`
		Links    struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	} `json:"repository"`
	PullRequest struct {
		ID     int    `json:"id"`
		Title  string `json:"title"`
		State  string `json:"state"` // OPEN, MERGED, DECLINED, SUPERSEDED
		Draft  bool   `json:"draft"`
		Source struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"source"`
		Destination struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"destination"`
		Reviewers []bbUser `json:"reviewers"`
	} `json:"pullrequest"`
	Comment struct {
		ID     int64 `json:"id"`
		Parent *struct {
			ID int64 `json:"id"`
		} `json:"parent"`
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
	} `json:"comment"`
}

func verifyBitbucket(secrets []string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		if hmac.Equal(mac.Sum(nil), want) {
			return true
		}
	}
	return false
}

func (s *Server) handleBitbucket(w http.ResponseWriter, r *http.Request) {
	// Nothing from the body is parsed or recorded before the signature
	// verifies: rejected deliveries keep transport facts only.
	event := r.Header.Get("X-Event-Key")
	hm := hookMetaOf(r.Context())
	hm.Event, hm.Delivery = truncate(event, 100), truncate(r.Header.Get("X-Request-UUID"), 100)
	sig := firstNonEmpty(r.Header.Get("X-Hub-Signature"), r.Header.Get("X-Hub-Signature-256"))
	if sig == "" {
		hm.Auth, hm.Reason = "failed", "no X-Hub-Signature header (set a secret on the Bitbucket webhook)"
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	h, viaEndpoint, ok := s.endpointHost(w, r, hosts.Bitbucket)
	if !ok {
		return
	}
	cands := []hosts.Host{h}
	if !viaEndpoint {
		cands = s.hosts.Enabled(hosts.Bitbucket) // the secret picks one
	}
	if len(cands) == 0 {
		hm.Auth, hm.Reason = "failed", "no Bitbucket host configured"
		http.Error(w, "unknown bitbucket host", http.StatusUnauthorized)
		return
	}
	if len(cands) == 1 {
		hm.Host = cands[0].Host
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	verified := false
	for _, c := range cands {
		if verifyBitbucket(c.WebhookSecrets, body, sig) {
			h, verified = c, true
			break
		}
	}
	if !verified {
		hm.Auth, hm.Reason = "failed", "X-Hub-Signature does not match the webhook secret of "+hostNames(cands)
		s.logger.Warn("bitbucket webhook signature mismatch", "hosts", hostNames(cands))
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	hm.Host = h.Host
	var p bbPayload
	_ = json.Unmarshal(body, &p)
	hm.Auth = "signature"
	if event == "diagnostics:ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	repo := p.Repository.FullName
	if repo == "" || p.PullRequest.ID == 0 {
		ignored(w, "event "+event)
		return
	}
	hm.Project, hm.Number, hm.Kind, hm.Sender = repo, p.PullRequest.ID, event, firstNonEmpty(p.Actor.Nickname, p.Actor.DisplayName)
	hm.Action = strings.TrimPrefix(event, "pullrequest:")
	s.seen(r.Context(), vcs.Bitbucket, h.Host, repo)
	if hm.Delivery != "" && !s.glDeliveries.first("bb|"+hm.Delivery, time.Now()) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate delivery"})
		return
	}
	tgt := s.bitbucketTarget(h)
	ref := vcs.Ref{Platform: vcs.Bitbucket, Host: h.Host, Repo: repo, Number: p.PullRequest.ID}
	log := s.logger.With("event", event, "repo", repo, "pr", p.PullRequest.ID)
	self := s.reviewerFor(tgt, h.Host)
	pr := p.PullRequest

	switch event {
	case "pullrequest:fulfilled", "pullrequest:rejected":
		if s.queue.Cancel("review:"+ref.String(), "pull request "+strings.ToLower(pr.State)+" before the review started") {
			ignored(w, "pull request "+strings.ToLower(pr.State)+"; cancelled its waiting review")
			return
		}
		ignored(w, "pull request "+strings.ToLower(pr.State))

	case "pullrequest:created", "pullrequest:updated":
		// Bitbucket sends "updated" for new commits and for edits alike;
		// the CLI skips a commit it already reviewed, so a metadata-only
		// update costs no model call.
		if pr.State != "" && pr.State != "OPEN" {
			ignored(w, "pull request is "+strings.ToLower(pr.State))
			return
		}
		acc := s.access(r.Context(), ref)
		if sync, why := settings.BranchSync(acc.SkipBranchSync, s.settings.Runtime(r.Context()).SyncSourceBranches,
			pr.Source.Branch.Name, pr.Destination.Branch.Name, p.Repository.MainBranch.Name); sync {
			ignored(w, why)
			return
		}
		if acc.RequireReviewer {
			if self == "" {
				ignored(w, "bot user unknown; set the reviewer for this Bitbucket host (access tokens have no user)")
				return
			}
			isReviewer := false
			for _, u := range pr.Reviewers {
				isReviewer = isReviewer || u.is(self)
			}
			if !isReviewer {
				ignored(w, "@"+self+" is not a reviewer on this pull request")
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
		log.Info("review queued")
		accepted(w, "review")

	case "pullrequest:comment_created":
		text := p.Comment.Content.Raw
		if isBotOutput(text) || p.Actor.is(self) {
			ignored(w, "comment from the bot")
			return
		}
		sourceRef := "bitbucket|" + h.Host + "|comment|" + idString(p.Comment.ID)
		cmd, ok := parseCommand(tgt.botName, text)
		if !ok {
			if p.Comment.Parent != nil && p.Comment.Parent.ID != 0 {
				parent := p.Comment.Parent.ID
				// Bitbucket Cloud has no reactions API: nothing to react with.
				s.replyToBot(tgt, ref, func(ctx context.Context) (parentComment, error) { return s.bitbucketComment(ctx, tgt, ref, parent) },
					nil, text, firstNonEmpty(p.Actor.Nickname, p.Actor.DisplayName), sourceRef)
				ignored(w, "not a command (replies to the bot are checked for learnings)")
				return
			}
			ignored(w, "bot not mentioned")
			return
		}
		cmd.author, cmd.sourceRef, cmd.authorID = firstNonEmpty(p.Actor.Nickname, p.Actor.DisplayName), sourceRef, p.Actor.AccountID
		allowed, ov := s.gate(r.Context(), ref)
		if !allowed {
			ignored(w, "repository is not enabled in the admin panel")
			return
		}
		if why := s.gateCommand(r.Context(), tgt, ref, cmd); why != "" {
			ignored(w, why)
			return
		}
		go func() {
			if err := tgt.client.React(context.Background(), &vcs.PR{Ref: ref}, strconv.FormatInt(p.Comment.ID, 10), "eyes"); err != nil {
				log.Debug("reaction failed", "error", err)
			}
		}()
		if err := s.dispatch(tgt, ref, cmd, ov); err != nil {
			submitErr(w, err)
			return
		}
		log.Info("command queued", "command", cmd.kind)
		accepted(w, cmd.kind)

	default:
		ignored(w, "event "+event)
	}
}
