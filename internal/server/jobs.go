package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
)

// target is a code host connection for one webhook: a client for light
// calls (reactions, help, error notes) and the secrets a review job needs.
type target struct {
	client  vcs.Client
	secrets func(ctx context.Context) (map[string]string, error)
	host    hosts.Host
	// botName is the mention handle on this host; reviewer the username
	// that counts as the bot (empty: the token's user).
	botName, reviewer string
}

func (s *Server) githubTarget(h hosts.Host, installation int64) target {
	var tokens vcs.TokenSource = vcs.StaticToken(h.Token)
	if app := s.ghApp(h.ID); app != nil && installation != 0 {
		tokens = app.Installation(installation)
	}
	return target{
		client: vcs.NewGitHub(h.BaseURL, tokens),
		secrets: func(ctx context.Context) (map[string]string, error) {
			// Installation tokens are short-lived (about an hour) and scoped
			// to the installation's repositories.
			tok, err := tokens.Token(ctx)
			if err != nil {
				return nil, fmt.Errorf("github token: %w", err)
			}
			return map[string]string{"GITHUB_TOKEN": tok, "GITHUB_API_URL": h.BaseURL}, nil
		},
		host:     h,
		botName:  s.mention(h.BotName, h.Host),
		reviewer: h.Reviewer,
	}
}

func (s *Server) gitlabTarget(h hosts.Host) target {
	return target{
		client: vcs.NewGitLab(h.BaseURL, h.Token, h.SkipTLSVerify),
		secrets: func(context.Context) (map[string]string, error) {
			return map[string]string{"GITLAB_TOKEN": h.Token}, nil
		},
		host:     h,
		botName:  s.mention(h.BotName, h.Host),
		reviewer: h.Reviewer,
	}
}

func (s *Server) bitbucketTarget(h hosts.Host) target {
	return target{
		client: vcs.NewBitbucket(h.BaseURL, vcs.BitbucketAuth{Token: h.Token, Email: h.Email, APIToken: h.APIToken}),
		secrets: func(context.Context) (map[string]string, error) {
			env := map[string]string{}
			if h.Token != "" {
				env["BITBUCKET_TOKEN"] = h.Token
			} else {
				env["BITBUCKET_API_TOKEN"] = h.APIToken
			}
			return env, nil
		},
		host:     h,
		botName:  s.mention(h.BotName, h.Host),
		reviewer: h.Reviewer,
	}
}

var envName = regexp.MustCompile(`[^A-Z0-9_]`)

// engineConfig is the codereview.yaml a job runs with. Only the keys the
// console sets are written; the CLI applies its own defaults to the rest
// and rejects keys it does not know ("console check-config" catches that).
type engineConfig struct {
	Model          string                         `yaml:"model,omitempty"`
	Fallback       []string                       `yaml:"fallback,omitempty"`
	Models         map[string]config.ModelProfile `yaml:"models,omitempty"`
	AllowRepoModel bool                           `yaml:"allow_repo_model,omitempty"`
	Review         map[string]any                 `yaml:"review,omitempty"`
	BotName        string                         `yaml:"bot_name,omitempty"`
	GitHub         *engineGitHub                  `yaml:"github,omitempty"`
	GitLab         []engineGitLab                 `yaml:"gitlab,omitempty"`
	Bitbucket      *engineBitbucket               `yaml:"bitbucket,omitempty"`
}

type engineBitbucket struct {
	APIURL   string `yaml:"api_url,omitempty"`
	Token    string `yaml:"token,omitempty"`
	Email    string `yaml:"email,omitempty"`
	APIToken string `yaml:"api_token,omitempty"`
}

type engineGitHub struct {
	APIURL string `yaml:"api_url,omitempty"`
}

type engineGitLab struct {
	BaseURL       string `yaml:"base_url"`
	Token         string `yaml:"token"`
	SkipTLSVerify bool   `yaml:"skip_tls_verify,omitempty"`
}

// jobConfig builds the engine config for a job. Secret values never go into
// the YAML: literal API keys move to env vars and are referenced by name.
func (s *Server) jobConfig(ctx context.Context, tgt target) ([]byte, map[string]string, error) {
	models, defModel, fallback, err := s.effectiveModels(ctx)
	if err != nil {
		return nil, nil, err
	}
	ec, env := buildEngineConfig(s.cfg, models, defModel, fallback)
	if tgt.botName != "" {
		ec.BotName = tgt.botName // placeholders (@<bot> summary) follow the host's handle
	}
	// Code host settings for this job; secrets stay in env vars.
	switch h := tgt.host; h.Platform {
	case hosts.GitHub:
		if h.BaseURL != "https://api.github.com" {
			ec.GitHub = &engineGitHub{APIURL: h.BaseURL}
		}
	case hosts.GitLab:
		ec.GitLab = []engineGitLab{{BaseURL: h.BaseURL, Token: "${GITLAB_TOKEN}", SkipTLSVerify: h.SkipTLSVerify}}
	case hosts.Bitbucket:
		bb := &engineBitbucket{APIURL: h.BaseURL}
		if h.Token != "" {
			bb.Token = "${BITBUCKET_TOKEN}"
		} else {
			bb.Email, bb.APIToken = h.Email, "${BITBUCKET_API_TOKEN}"
		}
		ec.Bitbucket = bb
	}
	b, err := yaml.Marshal(ec)
	if err != nil {
		return nil, nil, err
	}
	return b, env, nil
}

// EngineConfig builds the job config from the config file alone (no
// UI-managed models), as YAML plus the env vars holding moved API keys.
// "console check-config" validates it with the CLI.
func EngineConfig(cfg *config.Config) ([]byte, map[string]string, error) {
	ec, env := buildEngineConfig(cfg, cfg.Models, cfg.Model, cfg.Fallback)
	b, err := yaml.Marshal(ec)
	return b, env, err
}

func buildEngineConfig(cfg *config.Config, models map[string]config.ModelProfile, defModel string, fallback []string) (engineConfig, map[string]string) {
	env := map[string]string{}
	clean := map[string]config.ModelProfile{}
	for name, m := range models {
		key := "CODEREVIEW_KEY_" + envName.ReplaceAllString(strings.ToUpper(name), "_")
		m, secret := m.WithKeyEnv(key)
		if secret != "" {
			env[key] = secret
		}
		clean[name] = m
	}
	ec := engineConfig{
		Model:          defModel,
		Fallback:       fallback,
		Models:         clean,
		AllowRepoModel: cfg.AllowRepoModel,
		Review:         cfg.Review,
		BotName:        cfg.BotName,
	}
	return ec, env
}

// effectiveModels merges model profiles from the config file with those
// managed in the admin UI (UI wins), plus the default and fallback models.
func (s *Server) effectiveModels(ctx context.Context) (map[string]config.ModelProfile, string, []string, error) {
	models := map[string]config.ModelProfile{}
	for k, v := range s.cfg.Models {
		models[k] = v
	}
	def, fb := s.cfg.Model, s.cfg.Fallback
	if s.store != nil {
		profiles, err := s.store.ListModelProfiles(ctx)
		if err != nil {
			return nil, "", nil, fmt.Errorf("load model profiles: %w", err)
		}
		for _, p := range profiles {
			models[p.Name] = p.Config
		}
		var ms modelSettings
		if found, err := s.store.GetSetting(ctx, settingModels, &ms); err != nil {
			return nil, "", nil, err
		} else if found && ms.Default != "" {
			def, fb = ms.Default, ms.Fallback
		}
	}
	return models, def, fb, nil
}

const settingModels = "models"

// modelSettings is the UI-managed default and fallback model.
type modelSettings struct {
	Default  string   `json:"default"`
	Fallback []string `json:"fallback"`
}

type reviewOpts struct {
	Auto      bool
	Full      bool
	Overrides *config.Overrides
	By        string // admin who requested it from the UI
}

func (s *Server) newTask(ctx context.Context, tgt target, ref vcs.Ref, kind string, ov *config.Overrides) (executor.Task, error) {
	secrets, err := tgt.secrets(ctx)
	if err != nil {
		return executor.Task{}, err
	}
	cfgYAML, keyEnv, err := s.jobConfig(ctx, tgt)
	if err != nil {
		return executor.Task{}, err
	}
	for k, v := range keyEnv {
		secrets[k] = v
	}
	t := executor.Task{
		ID:         fmt.Sprintf("%s-%d-%d", strings.ReplaceAll(ref.Repo, "/", "-"), ref.Number, time.Now().UnixNano()),
		Kind:       kind,
		URL:        webURL(ref),
		Post:       true,
		ConfigYAML: cfgYAML,
		Env:        secrets,
	}
	if kind == "review" || kind == "ask" {
		ov = s.withLearnings(ctx, ref, ov)
	}
	if ov != nil {
		t.Overrides = ov
	}
	return t, nil
}

// enqueueReview schedules a review, merging with a pending one for the PR.
func (s *Server) enqueueReview(tgt target, ref vcs.Ref, opts reviewOpts, debounce bool) error {
	cur := opts
	job := &Job{
		Key:   "review:" + ref.String(),
		Label: "review " + ref.String(),
		State: &cur,
	}
	reMeta := func() {
		kind, trigger := "review", "push"
		if cur.Full {
			kind = "full review"
		}
		if !cur.Auto {
			trigger = "command"
		}
		if cur.By != "" {
			trigger = "ui"
		}
		job.Meta = meta(ref, kind, trigger)
		job.Meta.RequestedBy = cur.By
	}
	if debounce {
		job.Delay = s.cfg.Server.Debounce
	}
	job.Merge = func(older *Job) {
		if o, ok := older.State.(*reviewOpts); ok {
			cur.Full = cur.Full || o.Full
			cur.Auto = cur.Auto && o.Auto
			if cur.By == "" {
				cur.By = o.By
			}
		}
		reMeta()
	}
	reMeta()
	job.Run = func(ctx context.Context, rep *Report) error {
		if cur.By == "" { // requests from the admin panel are not budgeted
			if why := s.overBudget(ctx, ref); why != "" {
				rep.Skipped, rep.Note = true, why
				s.logger.Warn("review skipped: token budget", "pr", ref.String(), "reason", why)
				if !cur.Auto {
					_ = tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, "⏸️ Review skipped: "+why+".")
				}
				return nil
			}
		}
		t, err := s.newTask(ctx, tgt, ref, "review", cur.Overrides)
		if err != nil {
			return err
		}
		t.Full, t.Auto, t.Commands = cur.Full, cur.Auto, true
		// Posting options are resolved when the job runs, so a change in
		// the admin panel applies to reviews still waiting.
		acc := s.access(ctx, ref)
		t.ScoreLabels, t.CommitStatus = acc.ScoreLabels, acc.CommitStatus
		res, err := s.exec.Run(ctx, t)
		if err != nil {
			var re *executor.RunError
			if errors.As(err, &re) && re.Logs != "" {
				s.logger.Warn("review job failed", "pr", ref.String(), "executor", s.exec.Name(), "logs", tail(re.Logs, 2000))
			}
			if !cur.Auto {
				_ = tgt.client.PostComment(context.WithoutCancel(ctx), &vcs.PR{Ref: ref}, "⚠️ The review failed: "+truncate(err.Error(), 500))
			}
			return err
		}
		if res.Skipped {
			rep.Skipped, rep.Note = true, res.SkipReason
			s.logger.Info("review skipped", "pr", ref.String(), "reason", res.SkipReason)
			if !cur.Auto {
				_ = tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, "ℹ️ Review skipped: "+res.SkipReason)
			}
			return nil
		}
		rep.Comments, rep.Files, rep.Model = res.Posted, res.Files, res.Model
		rep.TokensIn, rep.TokensOut = res.TokensIn, res.TokensOut
		rep.Result = res.Raw
		switch {
		case res.TooLarge:
			rep.Note = "too many files for an automatic review"
		case res.Partial:
			rep.Note = "partial review"
		}
		s.logger.Info("review posted", "pr", ref.String(), "executor", s.exec.Name(), "comments", res.Posted, "files", res.Files,
			"tokens_in", res.TokensIn, "tokens_out", res.TokensOut)
		return nil
	}
	return s.queue.Submit(job)
}

func (s *Server) enqueueAnswer(tgt target, ref vcs.Ref, question string, ov *config.Overrides) error {
	return s.queue.Submit(&Job{
		Key:   fmt.Sprintf("answer:%s:%d", ref.String(), time.Now().UnixNano()),
		Label: "answer " + ref.String(),
		Meta:  meta(ref, "answer", "command"),
		Run: func(ctx context.Context, rep *Report) error {
			if why := s.overBudget(ctx, ref); why != "" {
				rep.Skipped, rep.Note = true, why
				return tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, "⏸️ Not answered: "+why+".")
			}
			t, err := s.newTask(ctx, tgt, ref, "ask", ov)
			if err != nil {
				return err
			}
			t.Question = question
			_, err = s.exec.Run(ctx, t)
			return err
		},
	})
}

func (s *Server) postHelp(tgt target, ref vcs.Ref) error {
	return s.queue.Submit(&Job{
		Key: fmt.Sprintf("help:%s:%d", ref.String(), time.Now().UnixNano()), Label: "help " + ref.String(),
		Meta: meta(ref, "help", "command"),
		Run: func(ctx context.Context, rep *Report) error {
			return tgt.client.PostComment(ctx, &vcs.PR{Ref: ref}, helpText(firstNonEmpty(tgt.botName, s.cfg.BotName)))
		},
	})
}

func (s *Server) dispatch(tgt target, ref vcs.Ref, cmd command, ov *config.Overrides) error {
	switch cmd.kind {
	case "review":
		return s.enqueueReview(tgt, ref, reviewOpts{Overrides: ov}, false)
	case "full":
		return s.enqueueReview(tgt, ref, reviewOpts{Full: true, Overrides: ov}, false)
	case "help":
		return s.postHelp(tgt, ref)
	case "remember":
		return s.remember(tgt, ref, cmd.question, cmd.author, cmd.sourceRef)
	default:
		return s.enqueueAnswer(tgt, ref, cmd.question, ov)
	}
}

// mention is the bot's handle on a host: the host's bot_name, else the
// top-level bot_name, else the token's user, so command hints and
// mentions reach the real bot account, else "codereview".
func (s *Server) mention(explicit, host string) string {
	return firstNonEmpty(explicit, s.cfg.BotName, s.self(host), "codereview")
}

// reviewerFor is the username that counts as the bot on host.
func (s *Server) reviewerFor(tgt target, host string) string {
	if tgt.reviewer != "" {
		return tgt.reviewer
	}
	return s.self(host)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// overBudget reports why a job must not run because today's token budget
// is used up ("" when it may run). Budgets count input and output tokens
// of finished jobs in the current UTC day, from the daily rollup; a job
// that starts under budget may finish over it.
func (s *Server) overBudget(ctx context.Context, ref vcs.Ref) string {
	if s.store == nil {
		return ""
	}
	eff := s.settings.Get(ctx)
	now := time.Now()
	check := func(budget int64, repo string, what string) string {
		if budget <= 0 {
			return ""
		}
		used, err := s.store.TokensUsed(ctx, now, ref.Platform, ref.Host, repo)
		if err != nil {
			s.logger.Warn("could not read token usage", "error", err)
			return "" // fail open: a metrics error should not stop reviews
		}
		if used >= budget {
			return fmt.Sprintf("the daily token budget %s is used up (%d of %d tokens today, UTC)", what, used, budget)
		}
		return ""
	}
	if why := check(eff.DailyTokenBudget, "", "for all repositories"); why != "" {
		return why
	}
	return check(eff.RepoDailyTokenBudget, ref.Repo, "for "+ref.Repo)
}

// ReviewNow queues a review requested from the admin panel (a pasted URL
// or a re-run). It is an explicit request, so the allowlist, reviewer and
// branch-sync rules do not apply; the repository's settings still do.
func (s *Server) ReviewNow(ctx context.Context, ref vcs.Ref, full bool, by string) error {
	h, ok := s.hosts.Find(ref.Platform, ref.Host)
	if !ok {
		return fmt.Errorf("no enabled %s host for %s: add it under Code hosts", ref.Platform, ref.Host)
	}
	var tgt target
	switch ref.Platform {
	case hosts.GitHub:
		tgt = s.githubTarget(h, 0)
	case hosts.GitLab:
		tgt = s.gitlabTarget(h)
	case hosts.Bitbucket:
		tgt = s.bitbucketTarget(h)
	default:
		return fmt.Errorf("unsupported platform %q", ref.Platform)
	}
	acc := s.access(ctx, ref)
	return s.enqueueReview(tgt, ref, reviewOpts{Full: full, By: by, Overrides: s.overrides(acc)}, false)
}
