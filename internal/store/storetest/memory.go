// Package storetest provides an in-memory store.Store for tests.
package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/clientip"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
)

// Memory is a thread-safe in-memory store.Store.
type Memory struct {
	mu        sync.Mutex
	nextID    int64
	repos     map[int64]*store.Repo
	rules     map[int64]*store.Rule
	hooks     []store.WebhookEvent
	changes   []store.ChangeRequest
	hosts     map[int64]*store.HostRow
	hooksEP   map[string]*store.WebhookEndpoint
	learns    []*store.Learning
	jobs      []store.Job
	sessions  map[string]store.Session
	sessionID int64
	seenKeys  map[string]time.Time
	audit     []store.AuditEntry
	models    map[string]store.ModelProfile
	settings  map[string][]byte
}

// New returns an empty store.
func New() *Memory {
	return &Memory{repos: map[int64]*store.Repo{}, rules: map[int64]*store.Rule{}, hosts: map[int64]*store.HostRow{}, hooksEP: map[string]*store.WebhookEndpoint{}, sessions: map[string]store.Session{},
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

func (m *Memory) ListHosts(context.Context) ([]store.HostRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.HostRow{}
	for _, h := range m.hosts {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform+out[i].Host < out[j].Platform+out[j].Host })
	return out, nil
}

func (m *Memory) PutHost(_ context.Context, in store.HostRow, actor string) (*store.HostRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in.Platform, in.Host = strings.ToLower(in.Platform), strings.ToLower(in.Host)
	var h *store.HostRow
	for _, x := range m.hosts {
		if x.Platform == in.Platform && x.Host == in.Host {
			h = x
		}
	}
	if h == nil {
		m.nextID++
		h = &store.HostRow{ID: m.nextID}
		m.hosts[h.ID] = h
	}
	id := h.ID
	*h = in
	h.ID, h.UpdatedBy, h.UpdatedAt = id, actor, time.Now()
	if len(h.Config) == 0 {
		h.Config = json.RawMessage("{}")
	}
	m.addAudit(actor, "host.put", h.Platform+":"+h.Host, map[string]any{"after": map[string]any{"name": h.Name, "enabled": h.Enabled}, "secrets_changed": nonNilList(h.SecretsChanged)})
	c := *h
	return &c, nil
}

func (m *Memory) DeleteHost(_ context.Context, id int64, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(m.hosts, id)
	for k, e := range m.hooksEP {
		if e.HostID == id {
			delete(m.hooksEP, k)
		}
	}
	m.addAudit(actor, "host.delete", h.Platform+":"+h.Host, map[string]any{})
	return nil
}

func (m *Memory) ListWebhookEndpoints(context.Context) ([]store.WebhookEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.WebhookEndpoint{}
	for _, e := range m.hooksEP {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt) || (out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID)
	})
	return out, nil
}

func (m *Memory) PutWebhookEndpoint(_ context.Context, in store.WebhookEndpoint, actor string) (*store.WebhookEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[in.HostID]; !ok {
		return nil, store.ErrNotFound
	}
	now := time.Now()
	e, ok := m.hooksEP[in.ID]
	if ok && e.HostID != in.HostID {
		return nil, store.ErrNotFound
	}
	if !ok {
		e = &store.WebhookEndpoint{ID: in.ID, HostID: in.HostID, CreatedAt: now, CreatedBy: actor}
		m.hooksEP[in.ID] = e
	}
	e.Name, e.Enabled, e.Secrets, e.UpdatedAt, e.UpdatedBy, e.AcceptSecretToken = in.Name, in.Enabled, in.Secrets, now, actor, in.AcceptSecretToken
	m.addAudit(actor, "webhook.put", e.ID, map[string]any{"after": map[string]any{"host_id": e.HostID, "name": e.Name, "enabled": e.Enabled, "accept_secret_token": e.AcceptSecretToken}, "secrets_changed": nonNilList(in.SecretsChanged)})
	c := *e
	return &c, nil
}

func (m *Memory) DeleteWebhookEndpoint(_ context.Context, id string, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.hooksEP[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(m.hooksEP, id)
	m.addAudit(actor, "webhook.delete", id, map[string]any{"host_id": e.HostID, "name": e.Name})
	return nil
}

func (m *Memory) ListLearnings(context.Context) ([]store.Learning, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.Learning{}
	for _, l := range m.learns {
		out = append(out, *l)
	}
	return out, nil
}

func (m *Memory) CreateLearning(_ context.Context, in store.Learning, actor string) (*store.Learning, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.SourceRef != "" {
		for _, l := range m.learns {
			if l.SourceRef == in.SourceRef {
				return nil, store.ErrDuplicate
			}
		}
	}
	m.nextID++
	in.ID, in.CreatedAt, in.UpdatedAt, in.UpdatedBy = m.nextID, time.Now(), time.Now(), actor
	in.Platform, in.Host = strings.ToLower(in.Platform), strings.ToLower(in.Host)
	c := in
	m.learns = append(m.learns, &c)
	m.addAudit(actor, "learning.create", in.Platform+":"+in.Host+"/"+in.Scope, map[string]any{"text": in.Text})
	out := in
	return &out, nil
}

func (m *Memory) UpdateLearning(_ context.Context, in store.Learning, actor string) (*store.Learning, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.learns {
		if l.ID == in.ID {
			l.Scope, l.Text, l.Path, l.Status, l.UpdatedBy, l.UpdatedAt = in.Scope, in.Text, in.Path, in.Status, actor, time.Now()
			m.addAudit(actor, "learning.update", l.Platform+":"+l.Host+"/"+l.Scope, map[string]any{"text": l.Text})
			c := *l
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Memory) DeleteLearning(_ context.Context, id int64, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, l := range m.learns {
		if l.ID == id {
			m.learns = append(m.learns[:i], m.learns[i+1:]...)
			m.addAudit(actor, "learning.delete", l.Platform+":"+l.Host+"/"+l.Scope, map[string]any{})
			return nil
		}
	}
	return store.ErrNotFound
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

func (m *Memory) HookStats(context.Context) ([]store.HostHookStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	by := map[string]*store.HostHookStats{}
	var keys []string
	for _, e := range m.hooks {
		if (e.Auth != "signature" && e.Auth != "token") || e.Host == "" {
			continue
		}
		k := e.Platform + ":" + e.Host
		s := by[k]
		if s == nil {
			s = &store.HostHookStats{Platform: e.Platform, Host: e.Host}
			by[k] = s
			keys = append(keys, k)
		}
		s.Verified++
		if slices.Contains(store.CommentKinds, e.Kind) || e.Event == "Note Hook" {
			s.Comments++
		}
		if e.Event == "System Hook" {
			s.SystemHook++
		}
	}
	sort.Strings(keys)
	out := []store.HostHookStats{}
	for _, k := range keys {
		out = append(out, *by[k])
	}
	return out, nil
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

func (m *Memory) MarkSeen(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seenKeys == nil {
		m.seenKeys = map[string]time.Time{}
	}
	if t, ok := m.seenKeys[key]; ok && time.Since(t) < ttl {
		return false, nil
	}
	m.seenKeys[key] = time.Now()
	return true, nil
}

func (m *Memory) PruneSeen(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, t := range m.seenKeys {
		if t.Before(before) {
			delete(m.seenKeys, k)
			n++
		}
	}
	return n, nil
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
	m.nextID++
	j.ID = m.nextID
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
		if !jobMatches(j, f) {
			continue
		}
		j.Result = nil
		out = append(out, j)
	}
	return out, nil
}

func jobMatches(j store.Job, f store.JobFilter) bool {
	return (f.Platform == "" || j.Platform == f.Platform) && (f.Repo == "" || strings.EqualFold(j.Repo, f.Repo)) &&
		(f.Host == "" || strings.EqualFold(j.Host, f.Host)) &&
		(f.RepoLike == "" || strings.Contains(strings.ToLower(j.Repo), strings.ToLower(f.RepoLike))) &&
		(f.State == "" || j.State == f.State) && (f.Kind == "" || j.Kind == f.Kind) && (f.Trigger == "" || j.Trigger == f.Trigger) &&
		(f.From.IsZero() || !j.Finished.Before(f.From)) && (f.To.IsZero() || j.Finished.Before(f.To)) &&
		(f.BeforeID == 0 || j.ID < f.BeforeID)
}

func (m *Memory) GetJob(_ context.Context, id int64) (*store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id {
			c := j
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

// Metrics computes rollups on the fly (the Postgres store keeps them).
func (m *Memory) Metrics(_ context.Context, f store.MetricsFilter) ([]store.MetricPoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	trunc := func(t time.Time) time.Time {
		t = t.UTC()
		switch f.Granularity {
		case store.Hourly:
			return t.Truncate(time.Hour)
		case store.Monthly:
			return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	by := map[time.Time]*store.MetricPoint{}
	for _, j := range m.jobs {
		if j.Finished.Before(f.From) || !j.Finished.Before(f.To) || (f.Platform != "" && j.Platform != f.Platform) ||
			(f.Host != "" && !strings.EqualFold(j.Host, f.Host)) || (f.Repo != "" && !strings.EqualFold(j.Repo, f.Repo)) {
			continue
		}
		b := trunc(j.Finished)
		p := by[b]
		if p == nil {
			p = &store.MetricPoint{Bucket: b}
			by[b] = p
		}
		p.Jobs++
		switch j.State {
		case "done":
			p.Done++
		case "skipped":
			p.Skipped++
		case "failed":
			p.Failed++
		}
		p.Comments += j.Comments
		p.Files += j.Files
		p.TokensIn += int64(j.TokensIn)
		p.TokensOut += int64(j.TokensOut)
		if !j.Started.IsZero() {
			p.DurationMS += j.Finished.Sub(j.Started).Milliseconds()
		}
	}
	out := []store.MetricPoint{}
	for _, p := range by {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Bucket.Before(out[k].Bucket) })
	return out, nil
}

func (m *Memory) TokensUsed(_ context.Context, at time.Time, platform, host, repo string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	day := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)
	var n int64
	for _, j := range m.jobs {
		f := j.Finished.UTC()
		if f.Before(day) || !f.Before(day.Add(24*time.Hour)) {
			continue
		}
		if repo != "" && (j.Platform != platform || !strings.EqualFold(j.Host, host) || !strings.EqualFold(j.Repo, repo)) {
			continue
		}
		n += int64(j.TokensIn + j.TokensOut)
	}
	return n, nil
}

func (m *Memory) PruneJobs(_ context.Context, before, _ time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []store.Job
	for _, j := range m.jobs {
		if !j.Finished.Before(before) {
			kept = append(kept, j)
		}
	}
	n := int64(len(m.jobs) - len(kept))
	m.jobs = kept
	return n, nil
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

func (m *Memory) PutSetting(ctx context.Context, key string, value any, actor string) error {
	return m.PutSettingAudited(ctx, key, value, value, actor)
}

func (m *Memory) PutSettingAudited(ctx context.Context, key string, value, detail any, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	js, err := json.Marshal(value)
	if err != nil {
		return err
	}
	m.settings[key] = js
	m.addAudit(actor, "setting.put", key, detail)
	m.stampAudit(ctx)
	return nil
}

func (m *Memory) CreateSession(_ context.Context, idHash []byte, s store.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionID++
	s.ID, s.Created, s.LastSeen = m.sessionID, time.Now(), time.Now()
	if s.Groups == nil {
		s.Groups = []string{}
	}
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

func (m *Memory) ListSessions(context.Context) ([]store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.Session{}
	for _, s := range m.sessions {
		if time.Now().Before(s.Expires) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *Memory) DeleteSessionByID(_ context.Context, id int64) (*store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if s.ID == id && time.Now().Before(s.Expires) {
			delete(m.sessions, k)
			return &s, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Memory) DeleteOtherSessions(_ context.Context, keep []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.sessions {
		if k != string(keep) {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}

func (m *Memory) addAudit(actor, action, target string, detail any) {
	js, _ := json.Marshal(detail)
	m.audit = append(m.audit, store.AuditEntry{ID: int64(len(m.audit) + 1), At: time.Now(), Actor: actor, Action: action, Target: target, Detail: js})
}

func (m *Memory) Audit(ctx context.Context, actor, action, target string, detail any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addAudit(actor, action, target, detail)
	m.stampAudit(ctx)
	return nil
}

// stampAudit adds the request's client to the newest audit entry.
func (m *Memory) stampAudit(ctx context.Context) {
	if len(m.audit) > 0 {
		c := clientip.From(ctx)
		m.audit[len(m.audit)-1].IP, m.audit[len(m.audit)-1].UserAgent = c.IP, c.UserAgent
	}
}

func (m *Memory) TouchSession(_ context.Context, idHash []byte, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(idHash)]
	if !ok || time.Since(s.LastSeen) < time.Minute {
		return nil
	}
	s.LastSeen = time.Now()
	if ip != "" {
		s.IP = ip
	}
	m.sessions[string(idHash)] = s
	return nil
}

func (m *Memory) CreateChangeRequest(ctx context.Context, c store.ChangeRequest) (*store.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	c.ID, c.RequestedAt, c.Status = m.nextID, time.Now(), store.ChangePending
	if len(c.Preview) == 0 {
		c.Preview = json.RawMessage("{}")
	}
	m.changes = append(m.changes, c)
	m.addAudit(c.RequestedBy, "change.requested", c.Summary, map[string]any{"id": c.ID, "method": c.Method, "path": c.Path, "after": c.Preview})
	m.stampAudit(ctx)
	return &c, nil
}

func (m *Memory) GetChangeRequest(_ context.Context, id int64) (*store.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.changes {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Memory) ListChangeRequests(_ context.Context, status string, limit int) ([]store.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.ChangeRequest{}
	for i := len(m.changes) - 1; i >= 0; i-- {
		if status == "" || m.changes[i].Status == status {
			out = append(out, m.changes[i])
		}
	}
	return out, nil
}

func (m *Memory) DecideChangeRequest(ctx context.Context, id int64, status, by string, resultStatus int, result string) (*store.ChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.changes {
		c := &m.changes[i]
		if c.ID == id && c.Status == store.ChangePending {
			c.Status, c.DecidedBy, c.DecidedAt, c.ResultStatus, c.Result = status, by, time.Now(), resultStatus, result
			if status != store.ChangePending {
				c.Body = ""
			}
			m.addAudit(by, "change."+status, c.Summary, map[string]any{"id": c.ID, "requested_by": c.RequestedBy, "result_status": resultStatus, "result": result})
			m.stampAudit(ctx)
			out := *c
			return &out, nil
		}
	}
	return nil, store.ErrNotFound
}

func nonNilList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (m *Memory) SearchAudit(_ context.Context, f store.AuditFilter) ([]store.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	has := func(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }
	out := []store.AuditEntry{}
	for i := len(m.audit) - 1; i >= 0 && len(out) < f.Limit; i-- {
		e := m.audit[i]
		a, prefix := f.Prefix()
		switch {
		case f.BeforeID > 0 && e.ID >= f.BeforeID,
			f.Actor != "" && !has(e.Actor, f.Actor),
			a != "" && prefix && !strings.HasPrefix(e.Action, a),
			a != "" && !prefix && e.Action != a,
			f.Target != "" && !has(e.Target, f.Target),
			f.Query != "" && !has(e.Target, f.Query) && !has(string(e.Detail), f.Query) && !has(e.Actor, f.Query),
			!f.From.IsZero() && e.At.Before(f.From),
			!f.To.IsZero() && !e.At.Before(f.To):
			continue
		}
		out = append(out, e)
	}
	return out, nil
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

// AgeSessions moves every session's last request d into the past (tests
// of the idle timeout).
func (m *Memory) AgeSessions(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		s.LastSeen = s.LastSeen.Add(-d)
		m.sessions[k] = s
	}
}

// LastSeen returns the newest session's last request (tests).
func (m *Memory) LastSeen() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	var t time.Time
	for _, s := range m.sessions {
		if s.LastSeen.After(t) {
			t = s.LastSeen
		}
	}
	return t
}
