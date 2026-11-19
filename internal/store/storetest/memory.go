// Package storetest provides an in-memory store.Store for tests.
package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
)

// Memory is a thread-safe in-memory store.Store.
type Memory struct {
	mu       sync.Mutex
	nextID   int64
	repos    map[int64]*store.Repo
	rules    map[int64]*store.Rule
	hooks    []store.WebhookEvent
	jobs     []store.Job
	sessions map[string]store.Session
	audit    []store.AuditEntry
	models   map[string]store.ModelProfile
	settings map[string][]byte
}

// New returns an empty store.
func New() *Memory {
	return &Memory{repos: map[int64]*store.Repo{}, rules: map[int64]*store.Rule{}, sessions: map[string]store.Session{},
		models: map[string]store.ModelProfile{}, settings: map[string][]byte{}}
}

func (m *Memory) find(platform, host, path string) *store.Repo {
	platform, host, path = store.RepoKey(platform, host, path)
	for _, r := range m.repos {
		if r.Platform == platform && r.Host == host && r.Path == path {
			return r
		}
	}
	return nil
}

func (m *Memory) TouchRepo(_ context.Context, platform, host, path string) (*store.Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	r := m.find(platform, host, path)
	if r == nil {
		m.nextID++
		p, h, pa := store.RepoKey(platform, host, path)
		r = &store.Repo{ID: m.nextID, Platform: p, Host: h, Path: pa, Mode: store.ModeInherit, FirstSeen: now, UpdatedAt: now}
		m.repos[r.ID] = r
	}
	r.LastSeen = &now
	c := *r
	return &c, nil
}

func (m *Memory) FindRepo(_ context.Context, platform, host, path string) (*store.Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(platform, host, path); r != nil {
		c := *r
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (m *Memory) ListRepos(context.Context) ([]store.Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Repo, 0, len(m.repos))
	for _, r := range m.repos {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) CreateRepo(_ context.Context, platform, host, path, mode string, actor string) (*store.Repo, error) {
	if !store.ValidMode(mode) {
		return nil, fmt.Errorf("mode %q: want inherit, enabled or disabled", mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.find(platform, host, path)
	if r == nil {
		m.nextID++
		p, h, pa := store.RepoKey(platform, host, path)
		r = &store.Repo{ID: m.nextID, Platform: p, Host: h, Path: pa, FirstSeen: time.Now()}
		m.repos[r.ID] = r
	}
	r.Mode, r.UpdatedBy, r.UpdatedAt = mode, actor, time.Now()
	m.addAudit(actor, "repo.create", r.Platform+":"+r.Host+"/"+r.Path, map[string]any{"mode": mode})
	c := *r
	return &c, nil
}

func (m *Memory) UpdateRepo(_ context.Context, id int64, mode string, settings config.Overrides, actor string) (*store.Repo, error) {
	if !store.ValidMode(mode) {
		return nil, fmt.Errorf("mode %q: want inherit, enabled or disabled", mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	before := *r
	r.Mode, r.Settings, r.UpdatedBy, r.UpdatedAt = mode, settings, actor, time.Now()
	m.addAudit(actor, "repo.update", r.Platform+":"+r.Host+"/"+r.Path, map[string]any{
		"before": map[string]any{"mode": before.Mode}, "after": map[string]any{"mode": mode},
	})
	c := *r
	return &c, nil
}

func (m *Memory) DeleteRepo(_ context.Context, id int64, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(m.repos, id)
	m.addAudit(actor, "repo.delete", r.Platform+":"+r.Host+"/"+r.Path, map[string]any{})
	return nil
}

func (m *Memory) ListRules(context.Context) ([]store.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Rule, 0, len(m.rules))
	for _, r := range m.rules {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out, nil
}

func (m *Memory) CreateRule(_ context.Context, in store.Rule, actor string) (*store.Rule, error) {
	pattern, err := store.NormalizePattern(in.Pattern)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, h, _ := store.RepoKey(in.Platform, in.Host, "")
	var r *store.Rule
	for _, x := range m.rules {
		if x.Platform == p && x.Host == h && x.Pattern == pattern {
			r = x
		}
	}
	if r == nil {
		m.nextID++
		r = &store.Rule{ID: m.nextID, Platform: p, Host: h, Pattern: pattern}
		m.rules[r.ID] = r
	}
	r.Enabled, r.RequireReviewer, r.SkipBranchSync, r.ScoreLabels, r.CommitStatus = in.Enabled, in.RequireReviewer, in.SkipBranchSync, in.ScoreLabels, in.CommitStatus
	r.Settings, r.UpdatedBy, r.UpdatedAt = in.Settings, actor, time.Now()
	m.addAudit(actor, "rule.create", p+":"+h+"/"+pattern, map[string]any{"enabled": in.Enabled})
	c := *r
	return &c, nil
}

func (m *Memory) UpdateRule(_ context.Context, id int64, in store.Rule, actor string) (*store.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	r.Enabled, r.RequireReviewer, r.SkipBranchSync, r.ScoreLabels, r.CommitStatus = in.Enabled, in.RequireReviewer, in.SkipBranchSync, in.ScoreLabels, in.CommitStatus
	r.Settings, r.UpdatedBy, r.UpdatedAt = in.Settings, actor, time.Now()
	m.addAudit(actor, "rule.update", r.Platform+":"+r.Host+"/"+r.Pattern, map[string]any{"enabled": in.Enabled})
	c := *r
	return &c, nil
}

func (m *Memory) DeleteRule(_ context.Context, id int64, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(m.rules, id)
	m.addAudit(actor, "rule.delete", r.Platform+":"+r.Host+"/"+r.Pattern, map[string]any{})
	return nil
}

func (m *Memory) InsertWebhookEvents(_ context.Context, events []store.WebhookEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range events {
		m.nextID++
		e.ID = m.nextID
		if e.ReceivedAt.IsZero() {
			e.ReceivedAt = time.Now()
		}
		m.hooks = append(m.hooks, e)
	}
	return nil
}

func (m *Memory) ListWebhookEvents(_ context.Context, f store.WebhookFilter) ([]store.WebhookEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	out := []store.WebhookEvent{}
	for i := len(m.hooks) - 1; i >= 0 && len(out) < f.Limit; i-- {
		e := m.hooks[i]
		if (f.Platform != "" && e.Platform != f.Platform) || (f.Outcome != "" && e.Outcome != f.Outcome) ||
			(f.Project != "" && !strings.Contains(strings.ToLower(e.Project), strings.ToLower(f.Project))) ||
			(f.BeforeID > 0 && e.ID >= f.BeforeID) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (m *Memory) CountWebhookEvents(_ context.Context, since time.Time) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, e := range m.hooks {
		if !e.ReceivedAt.Before(since) {
			out[e.Outcome]++
		}
	}
	return out, nil
}

func (m *Memory) PruneWebhookEvents(_ context.Context, before time.Time, keep int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []store.WebhookEvent
	for _, e := range m.hooks {
		if !e.ReceivedAt.Before(before) {
			kept = append(kept, e)
		}
	}
	if len(kept) > keep {
		kept = kept[len(kept)-keep:]
	}
	n := int64(len(m.hooks) - len(kept))
	m.hooks = kept
	return n, nil
}

func (m *Memory) InsertJob(_ context.Context, j store.Job) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.ID = int64(len(m.jobs) + 1)
	m.jobs = append(m.jobs, j)
	return j.ID, nil
}

func (m *Memory) RecentJobs(ctx context.Context, limit int) ([]store.Job, error) {
	return m.ListJobs(ctx, store.JobFilter{Limit: limit})
}

func (m *Memory) ListJobs(_ context.Context, f store.JobFilter) ([]store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	out := []store.Job{}
	for i := len(m.jobs) - 1; i >= 0 && len(out) < f.Limit; i-- {
		j := m.jobs[i]
		if (f.Platform != "" && j.Platform != f.Platform) || (f.Repo != "" && !strings.EqualFold(j.Repo, f.Repo)) ||
			(f.State != "" && j.State != f.State) || (f.Kind != "" && j.Kind != f.Kind) || (f.BeforeID > 0 && j.ID >= f.BeforeID) {
			continue
		}
		j.Result = nil
		out = append(out, j)
	}
	return out, nil
}

func (m *Memory) GetJob(_ context.Context, id int64) (*store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id < 1 || int(id) > len(m.jobs) {
		return nil, store.ErrNotFound
	}
	j := m.jobs[id-1]
	return &j, nil
}

func (m *Memory) ListModelProfiles(context.Context) ([]store.ModelProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.ModelProfile{}
	for _, p := range m.models {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) PutModelProfile(_ context.Context, name string, cfg config.ModelProfile, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg = cfg.WithoutKey()
	m.models[name] = store.ModelProfile{Name: name, Config: cfg, UpdatedAt: time.Now(), UpdatedBy: actor}
	m.addAudit(actor, "model.put", name, cfg)
	return nil
}

func (m *Memory) DeleteModelProfile(_ context.Context, name string, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[name]; !ok {
		return store.ErrNotFound
	}
	delete(m.models, name)
	m.addAudit(actor, "model.delete", name, map[string]any{})
	return nil
}

func (m *Memory) GetSetting(_ context.Context, key string, out any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.settings[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (m *Memory) PutSetting(_ context.Context, key string, value any, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	js, err := json.Marshal(value)
	if err != nil {
		return err
	}
	m.settings[key] = js
	m.addAudit(actor, "setting.put", key, value)
	return nil
}

func (m *Memory) CreateSession(_ context.Context, idHash []byte, s store.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(idHash)] = s
	return nil
}

func (m *Memory) GetSession(_ context.Context, idHash []byte) (*store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(idHash)]
	if !ok || time.Now().After(s.Expires) {
		return nil, store.ErrNotFound
	}
	return &s, nil
}

func (m *Memory) DeleteSession(_ context.Context, idHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(idHash))
	return nil
}

func (m *Memory) addAudit(actor, action, target string, detail any) {
	js, _ := json.Marshal(detail)
	m.audit = append(m.audit, store.AuditEntry{ID: int64(len(m.audit) + 1), At: time.Now(), Actor: actor, Action: action, Target: target, Detail: js})
}

func (m *Memory) Audit(_ context.Context, actor, action, target string, detail any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addAudit(actor, action, target, detail)
	return nil
}

func (m *Memory) ListAudit(_ context.Context, limit int) ([]store.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.AuditEntry{}
	for i := len(m.audit) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.audit[i])
	}
	return out, nil
}

func (m *Memory) Close() {}

var _ store.Store = (*Memory)(nil)
