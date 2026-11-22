package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/settings"
)

// Chat commands that run the model (review, full review, questions) spend
// provider tokens, so they are limited (issue #28):
//   - who: by default only authors who can push to the repository
//     (settings.CommandAccess), looked up with the code host and cached;
//   - how often: per author and merge request, and per author overall.
// help and remember cost nothing and are not limited here (remember's
// learnings have their own approval rule).

const (
	roleCacheTTL      = 10 * time.Minute
	perPRPerHour      = 5  // model commands by one author on one MR
	perAuthorPerHour  = 20 // model commands by one author overall
	noticeEvery       = time.Hour
	roleLookupTimeout = 5 * time.Second
)

// spendsTokens reports whether a command runs the model.
func spendsTokens(kind string) bool {
	switch kind {
	case "help", "remember":
		return false
	}
	return true
}

type roleEntry struct {
	role    string
	trusted bool
	at      time.Time
}

// commandGate holds the role cache, the rate windows and when an author was
// last told why a command was refused (so refusals don't spam the MR).
type commandGate struct {
	// lookup replaces the code host role lookup (tests).
	lookup func(author string) (role string, trusted bool)

	mu      sync.Mutex
	roles   map[string]roleEntry
	runs    map[string][]time.Time
	notices map[string]time.Time
}

func (g *commandGate) init() {
	if g.roles == nil {
		g.roles, g.runs, g.notices = map[string]roleEntry{}, map[string][]time.Time{}, map[string]time.Time{}
	}
}

// allow records a run for key if fewer than limit happened in the last
// hour.
func (g *commandGate) allow(key string, limit int, now time.Time) bool {
	kept := g.runs[key][:0]
	for _, t := range g.runs[key] {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		g.runs[key] = kept
		return false
	}
	g.runs[key] = append(kept, now)
	return true
}

// commandAllowed decides whether a chat command may run. why is the reason
// it may not (for the webhook history), and notice the comment to post, or
// "" when the author was already told recently.
func (s *Server) commandAllowed(ctx context.Context, tgt target, ref vcs.Ref, cmd command) (ok bool, why, notice string) {
	if !spendsTokens(cmd.kind) {
		return true, "", ""
	}
	author := firstNonEmpty(cmd.authorID, cmd.author)
	g := &s.cmdGate
	if s.settings.Runtime(ctx).CommandAccess != settings.CommandAccessAnyone {
		role, trusted := s.cachedRole(ctx, tgt, ref, author)
		if !trusted {
			why = fmt.Sprintf("@%s can't run commands here (role: %s); commands need write access to the repository", cmd.author, role)
			notice = "Only people who can push to this repository can run `@" + tgt.botName + " " + cmdName(cmd.kind) + "`. An admin can change this under Settings in the console."
			return false, why, s.noticeOnce(g, ref, author, notice)
		}
	}
	now := time.Now()
	g.mu.Lock()
	g.init()
	okPR := g.allow("pr|"+ref.String()+"|"+author, perPRPerHour, now)
	okAll := okPR && g.allow("author|"+ref.Host+"|"+author, perAuthorPerHour, now)
	g.mu.Unlock()
	if !okPR || !okAll {
		limit := fmt.Sprintf("%d an hour on this merge request", perPRPerHour)
		if okPR {
			limit = fmt.Sprintf("%d an hour", perAuthorPerHour)
		}
		why = "rate limit: @" + cmd.author + " ran more than " + limit + " commands that run the model"
		notice = "⏸️ Slow down: at most " + limit + " review or question commands per person. Try again later."
		return false, why, s.noticeOnce(g, ref, author, notice)
	}
	return true, "", ""
}

// noticeOnce returns notice unless this author was told about this MR
// within noticeEvery.
func (s *Server) noticeOnce(g *commandGate, ref vcs.Ref, author, notice string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.init()
	key := ref.String() + "|" + author
	if t, ok := g.notices[key]; ok && time.Since(t) < noticeEvery {
		return ""
	}
	g.notices[key] = time.Now()
	return notice
}

func (s *Server) cachedRole(ctx context.Context, tgt target, ref vcs.Ref, author string) (string, bool) {
	key := tgt.host.Platform + "|" + tgt.host.Host + "|" + strings.ToLower(ref.Repo) + "|" + strings.ToLower(author)
	s.cmdGate.mu.Lock()
	s.cmdGate.init()
	e, ok := s.cmdGate.roles[key]
	s.cmdGate.mu.Unlock()
	if ok && time.Since(e.at) < roleCacheTTL {
		return e.role, e.trusted
	}
	lctx, cancel := context.WithTimeout(ctx, roleLookupTimeout)
	defer cancel()
	var role string
	var trusted bool
	if s.cmdGate.lookup != nil {
		role, trusted = s.cmdGate.lookup(author)
	} else {
		role, trusted = s.authorRole(lctx, tgt, ref, author)
	}
	if role != "unknown" { // don't cache a failed lookup
		s.cmdGate.mu.Lock()
		s.cmdGate.roles[key] = roleEntry{role: role, trusted: trusted, at: time.Now()}
		s.cmdGate.mu.Unlock()
	}
	return role, trusted
}

func cmdName(kind string) string {
	switch kind {
	case "full":
		return "full review"
	case "ask":
		return "<question>"
	}
	return kind
}

// gateCommand applies commandAllowed in a webhook handler: on refusal it
// posts the notice (in the background) and returns the reason to record.
func (s *Server) gateCommand(ctx context.Context, tgt target, ref vcs.Ref, cmd command) (refused string) {
	ok, why, notice := s.commandAllowed(ctx, tgt, ref, cmd)
	if ok {
		return ""
	}
	s.logger.Info("chat command refused", "pr", ref.String(), "author", cmd.author, "command", cmd.kind, "reason", why)
	if notice != "" {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = tgt.client.PostComment(c, &vcs.PR{Ref: ref}, notice)
		}()
	}
	return why
}
