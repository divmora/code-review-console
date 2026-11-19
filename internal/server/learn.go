// Copyright (c) DIVMORA Technologies. All rights reserved.
// Licensed under the Divmora Enterprise License; see LICENSE-ENTERPRISE.
// Use requires a valid Divmora license. Do not modify or remove license checks.

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
)

// Learnings are team decisions captured from review discussions. The
// console stores and scopes them; the CLI uses them (CODEREVIEW_OVERRIDES)
// and extracts them from replies ("codereview learn").

// maxJobLearnings caps learnings per job (the CLI's limit).
const maxJobLearnings = 100

// learningsFor returns the active learnings that apply to a repository:
// host-wide first, then groups and patterns, then the repository itself.
func (s *Server) learningsFor(ctx context.Context, ref vcs.Ref) []config.Learning {
	if s.store == nil || !s.settings.Runtime(ctx).LearningsEnabled {
		return nil
	}
	all, err := s.store.ListLearnings(ctx)
	if err != nil {
		s.logger.Warn("could not load learnings", "error", err)
		return nil
	}
	var matched []store.Learning
	for _, l := range all {
		if l.Status == store.LearningActive && l.AsRule().Matches(ref.Platform, ref.Host, ref.Repo) {
			matched = append(matched, l)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return strings.Count(matched[i].Scope, "/") < strings.Count(matched[j].Scope, "/")
	})
	out := make([]config.Learning, 0, len(matched))
	for _, l := range matched {
		if len(out) == maxJobLearnings {
			break
		}
		out = append(out, config.Learning{Text: l.Text, Path: l.Path})
	}
	return out
}

// withLearnings returns overrides with the repository's learnings added.
func (s *Server) withLearnings(ctx context.Context, ref vcs.Ref, ov *config.Overrides) *config.Overrides {
	ls := s.learningsFor(ctx, ref)
	if len(ls) == 0 {
		return ov
	}
	var c config.Overrides
	if ov != nil {
		c = *ov
	}
	c.Learnings = append(append([]config.Learning(nil), c.Learnings...), ls...)
	return &c
}

// saveLearning stores a learning scoped to the repository. It returns
// whether it applies at once.
func (s *Server) saveLearning(ctx context.Context, ref vcs.Ref, text, path, source, url, author, sourceRef string) (*store.Learning, error) {
	if s.store == nil {
		return nil, errors.New("learnings need a database")
	}
	status := store.LearningActive
	if !s.settings.Runtime(ctx).LearningsAutoActivate {
		status = store.LearningPending
	}
	text = strings.TrimSpace(text)
	if len(text) > 600 {
		text = text[:600]
	}
	return s.store.CreateLearning(ctx, store.Learning{Platform: ref.Platform, Host: ref.Host, Scope: strings.ToLower(ref.Repo),
		Text: text, Path: path, Status: status, Source: source, SourceURL: url, SourceAuthor: author, SourceRef: sourceRef}, "bot")
}

// remember handles "@bot remember <text>": the author states the rule.
func (s *Server) remember(tgt target, ref vcs.Ref, text, author, sourceRef string) error {
	return s.queue.Submit(&Job{
		Key: "remember:" + sourceRef, Label: "remember " + ref.String(), Meta: meta(ref, "remember", "command"),
		Run: func(ctx context.Context, rep *Report) error {
			if !s.lic().Has(license.Learnings) {
				return tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, "Team learnings are part of the licensed Divmora console; this console has no license for them, so nothing was saved.")
			}
			if strings.TrimSpace(text) == "" {
				return tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, "Tell me what to remember: `@"+tgt.botName+" remember <a rule for future reviews>`.")
			}
			l, err := s.saveLearning(ctx, ref, text, "", "command", webURL(ref), author, sourceRef)
			if errors.Is(err, store.ErrDuplicate) {
				return nil
			}
			if err != nil {
				return err
			}
			note := "📝 Noted for future reviews of `" + ref.Repo + "`: " + l.Text
			if l.Status == store.LearningPending {
				note += "\n\n_An admin approves new learnings in the console before they apply._"
			}
			return tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, note)
		},
	})
}

// learnInput is what "codereview learn" reads.
type learnInput struct {
	Repo     string            `json:"repo"`
	Path     string            `json:"path"`
	Finding  string            `json:"finding"`
	Reply    string            `json:"reply"`
	Author   string            `json:"author"`
	Existing []config.Learning `json:"existing,omitempty"`
}

// learnFromReply runs "codereview learn" on a reply to one of the bot's
// comments and stores the learning, if any. It is quiet: nothing is posted.
func (s *Server) learnFromReply(tgt target, ref vcs.Ref, in learnInput, sourceRef string, react func(context.Context, string)) error {
	if s.store == nil || !s.settings.Runtime(context.Background()).LearningsEnabled || isBotOutput(in.Reply) {
		return nil
	}
	return s.queue.Submit(&Job{
		Key: "learn:" + sourceRef, Label: "learn " + ref.String(), Meta: meta(ref, "learn", "reply"),
		Run: func(ctx context.Context, rep *Report) error {
			in.Existing = s.learningsFor(ctx, ref)
			b, _ := json.Marshal(in)
			t, err := s.newTask(ctx, tgt, ref, "learn", nil)
			if err != nil {
				return err
			}
			t.Env["CODEREVIEW_LEARN_INPUT"] = string(b)
			res, err := s.exec.Run(ctx, t)
			if err != nil {
				return err
			}
			var out struct {
				Learning bool   `json:"learning"`
				Text     string `json:"text"`
				Path     string `json:"path"`
				Reason   string `json:"reason"`
				Usage    struct {
					InputTokens  int `json:"InputTokens"`
					OutputTokens int `json:"OutputTokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(res.Raw, &out); err != nil {
				return fmt.Errorf("learn output: %w", err)
			}
			rep.TokensIn, rep.TokensOut = out.Usage.InputTokens, out.Usage.OutputTokens
			if !out.Learning {
				rep.Skipped, rep.Note = true, "no learning: "+out.Reason
				return nil
			}
			l, err := s.saveLearning(ctx, ref, out.Text, out.Path, "reply", webURL(ref), in.Author, sourceRef)
			if errors.Is(err, store.ErrDuplicate) {
				rep.Skipped, rep.Note = true, "already learned from this comment"
				return nil
			}
			if err != nil {
				return err
			}
			rep.Note = "learned (" + l.Status + "): " + l.Text
			if react != nil {
				react(ctx, "learned")
			}
			s.logger.Info("learning captured", "pr", ref.String(), "status", l.Status, "text", l.Text)
			return nil
		},
	})
}

// ---- parent comments: is the reply to one of the bot's comments? ----

// parentComment is the comment a reply answers.
type parentComment struct {
	Author string
	Body   string
	Path   string
}

func (s *Server) hostGET(ctx context.Context, h hosts.Host, tgt target, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return err
	}
	env, err := tgt.secrets(ctx)
	if err != nil {
		return err
	}
	switch h.Platform {
	case hosts.GitLab:
		req.Header.Set("PRIVATE-TOKEN", env["GITLAB_TOKEN"])
	case hosts.GitHub:
		req.Header.Set("Authorization", "Bearer "+env["GITHUB_TOKEN"])
		req.Header.Set("Accept", "application/vnd.github+json")
	case hosts.Bitbucket:
		if h.Token != "" {
			req.Header.Set("Authorization", "Bearer "+h.Token)
		} else {
			req.SetBasicAuth(h.Email, h.APIToken)
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	if h.SkipTLSVerify {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opted in per host
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", req.URL.Path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// gitlabDiscussionStart returns the first note of a merge request discussion.
func (s *Server) gitlabDiscussionStart(ctx context.Context, tgt target, ref vcs.Ref, discussionID string) (parentComment, error) {
	var d struct {
		Notes []struct {
			Body   string `json:"body"`
			Author struct {
				Username string `json:"username"`
			} `json:"author"`
			Position *struct {
				NewPath string `json:"new_path"`
			} `json:"position"`
		} `json:"notes"`
	}
	u := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/discussions/%s", tgt.host.BaseURL, url.PathEscape(ref.Repo), ref.Number, url.PathEscape(discussionID))
	if err := s.hostGET(ctx, tgt.host, tgt, u, &d); err != nil {
		return parentComment{}, err
	}
	if len(d.Notes) == 0 {
		return parentComment{}, errors.New("empty discussion")
	}
	p := parentComment{Author: d.Notes[0].Author.Username, Body: d.Notes[0].Body}
	if d.Notes[0].Position != nil {
		p.Path = d.Notes[0].Position.NewPath
	}
	return p, nil
}

// githubComment returns a pull request review comment.
func (s *Server) githubComment(ctx context.Context, tgt target, ref vcs.Ref, id int64) (parentComment, error) {
	var c struct {
		Body string `json:"body"`
		Path string `json:"path"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	u := fmt.Sprintf("%s/repos/%s/pulls/comments/%d", tgt.host.BaseURL, ref.Repo, id)
	if err := s.hostGET(ctx, tgt.host, tgt, u, &c); err != nil {
		return parentComment{}, err
	}
	return parentComment{Author: c.User.Login, Body: c.Body, Path: c.Path}, nil
}

// bitbucketComment returns a pull request comment.
func (s *Server) bitbucketComment(ctx context.Context, tgt target, ref vcs.Ref, id int64) (parentComment, error) {
	var c struct {
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
		User   bbUser `json:"user"`
		Inline *struct {
			Path string `json:"path"`
		} `json:"inline"`
	}
	u := fmt.Sprintf("%s/repositories/%s/pullrequests/%d/comments/%d", tgt.host.BaseURL, ref.Repo, ref.Number, id)
	if err := s.hostGET(ctx, tgt.host, tgt, u, &c); err != nil {
		return parentComment{}, err
	}
	p := parentComment{Author: firstNonEmpty(c.User.Nickname, c.User.AccountID), Body: c.Content.Raw}
	if c.Inline != nil {
		p.Path = c.Inline.Path
	}
	return p, nil
}

// replyToBot checks that a reply answers one of the bot's own comments
// (fetching the parent in the background, off the webhook path) and runs
// the learning job when it does.
// react, when set, puts a reaction on the reply: "seen" as soon as it is
// known to answer the bot, "learned" when a learning was saved.
func (s *Server) replyToBot(tgt target, ref vcs.Ref, fetch func(ctx context.Context) (parentComment, error), react func(context.Context, string),
	reply, author, sourceRef string) {
	if s.store == nil || !s.settings.Runtime(context.Background()).LearningsEnabled {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if !s.access(ctx, ref).Enabled {
			return // learnings only for repositories the console reviews
		}
		p, err := fetch(ctx)
		if err != nil {
			s.logger.Debug("could not read the parent comment", "pr", ref.String(), "error", err)
			return
		}
		self := s.self(tgt.host.Host)
		if !isBotOutput(p.Body) && (self == "" || !strings.EqualFold(p.Author, self)) {
			return // not a reply to the bot
		}
		if react != nil {
			react(ctx, "seen")
		}
		if err := s.learnFromReply(tgt, ref, learnInput{Repo: ref.Repo, Path: p.Path, Finding: p.Body, Reply: reply, Author: author}, sourceRef, react); err != nil {
			s.logger.Warn("could not queue a learning job", "pr", ref.String(), "error", err)
		}
	}()
}

func idString(n int64) string { return strconv.FormatInt(n, 10) }

// gitlabReact reacts on a GitLab note: 👀 when seen, ✅ when learned.
func (s *Server) gitlabReact(tgt target, ref vcs.Ref, noteID int64) func(context.Context, string) {
	return func(ctx context.Context, what string) {
		emoji := map[string]string{"seen": "eyes", "learned": "white_check_mark"}[what]
		if err := tgt.client.React(ctx, &vcs.PR{Ref: ref}, idString(noteID), emoji); err != nil {
			s.logger.Debug("reaction failed", "pr", ref.String(), "error", err)
		}
	}
}

// githubReviewReact reacts on a pull request review comment: 👀 when seen,
// 🎉 when learned (GitHub's reaction set has no check mark).
func (s *Server) githubReviewReact(tgt target, ref vcs.Ref, commentID int64) func(context.Context, string) {
	return func(ctx context.Context, what string) {
		emoji := map[string]string{"seen": "eyes", "learned": "hooray"}[what]
		u := fmt.Sprintf("%s/repos/%s/pulls/comments/%d/reactions", tgt.host.BaseURL, ref.Repo, commentID)
		if err := s.hostPOST(ctx, tgt, u, map[string]string{"content": emoji}); err != nil {
			s.logger.Debug("reaction failed", "pr", ref.String(), "error", err)
		}
	}
}

func (s *Server) hostPOST(ctx context.Context, tgt target, rawURL string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", rawURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	env, err := tgt.secrets(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+env["GITHUB_TOKEN"])
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %s", req.URL.Path, resp.Status)
	}
	return nil
}

var _ = executor.Task{} // the learn job runs through the executor like a review
