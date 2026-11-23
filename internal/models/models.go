// Package models holds the model profiles reviews run with, managed in the
// admin panel (the database). The config file's models:, model: and
// fallback: are only copied into the database once (Import); without a
// database (tests) they are used as they are. API keys are never stored: a
// profile names the environment variable that holds its key (api_key_env),
// and the console passes that variable to review jobs.
package models

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store"
)

// SettingKey is the settings row holding the default and fallback models.
const SettingKey = "models"

// Choice is the default model and its fallbacks, as stored under SettingKey.
type Choice struct {
	Default  string   `json:"default"`
	Fallback []string `json:"fallback"`
}

// Providers are the codereview CLI's providers, with the env var each
// reads its key from when a profile sets no api_key_env ("" = none).
var Providers = map[string]string{
	"openai":            "OPENAI_API_KEY",
	"anthropic":         "ANTHROPIC_API_KEY",
	"gemini":            "GEMINI_API_KEY",
	"bedrock":           "",
	"azure":             "AZURE_OPENAI_API_KEY",
	"ollama":            "",
	"openrouter":        "OPENROUTER_API_KEY",
	"groq":              "GROQ_API_KEY",
	"deepseek":          "DEEPSEEK_API_KEY",
	"mistral":           "MISTRAL_API_KEY",
	"xai":               "XAI_API_KEY",
	"together":          "TOGETHER_API_KEY",
	"litellm":           "LITELLM_API_KEY",
	"openai-compatible": "OPENAI_COMPATIBLE_API_KEY",
}

// Service reads and changes model profiles.
type Service struct {
	cfg *config.Config
	st  store.Store // nil: config file only
	// Getenv reads the console's environment (os.Getenv by default).
	Getenv func(string) string
}

// New returns a Service over the config file and the database (st may be nil).
func New(cfg *config.Config, st store.Store) *Service {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Service{cfg: cfg, st: st, Getenv: os.Getenv}
}

// Effective returns every profile, the default model and the fallbacks
// reviews use right now: the database's, or without one the config file's.
func (s *Service) Effective(ctx context.Context) (map[string]config.ModelProfile, string, []string, error) {
	models := map[string]config.ModelProfile{}
	if s.st == nil {
		for k, v := range s.cfg.Models {
			models[k] = v
		}
		return models, s.cfg.Model, s.cfg.Fallback, nil
	}
	def, fb := "", []string(nil)
	profiles, err := s.st.ListModelProfiles(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("load model profiles: %w", err)
	}
	for _, p := range profiles {
		models[p.Name] = p.Config
	}
	c, found, err := s.choice(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	if found && c.Default != "" {
		def, fb = c.Default, c.Fallback
	}
	return models, def, fb, nil
}

func (s *Service) choice(ctx context.Context) (Choice, bool, error) {
	var c Choice
	found, err := s.st.GetSetting(ctx, SettingKey, &c)
	return c, found, err
}

// Names returns every profile name, sorted.
func (s *Service) Names(ctx context.Context) ([]string, error) {
	models, _, _, err := s.Effective(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(models))
	for n := range models {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// KeyEnvs returns the env vars review jobs need from the console's
// environment for the profiles in the database: each profile's
// api_key_env that is set here. Config file profiles are passed through
// by the executor already.
func (s *Service) KeyEnvs(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	if s.st == nil {
		return out, nil
	}
	profiles, err := s.st.ListModelProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		name := p.Config.String("api_key_env")
		if name == "" || CheckKeyEnv(name) != nil {
			continue
		}
		if v := s.Getenv(name); v != "" {
			out[name] = v
		}
	}
	return out, nil
}

// Profile is one profile as the admin panel shows it.
type Profile struct {
	Name   string              `json:"name"`
	Config config.ModelProfile `json:"config"`
	// Source is "console" (editable) or "config" (the config file).
	Source string `json:"source"`
	// KeyEnv is the env var the key is read from ("" = the provider needs
	// none, e.g. Bedrock or Ollama) and KeySet whether the console has it.
	KeyEnv string `json:"key_env,omitempty"`
	KeySet bool   `json:"key_set"`
	// KeyInConfig is true for a config file profile with a literal api_key
	// (never shown).
	KeyInConfig bool   `json:"key_in_config,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	UpdatedBy   string `json:"updated_by,omitempty"`
}

// View is the Models page.
type View struct {
	Profiles []Profile `json:"profiles"`
	Default  string    `json:"default"`
	Fallback []string  `json:"fallback"`
	// ChoiceSource says where the default comes from: "console", "config"
	// or "" when none is set.
	ChoiceSource string   `json:"choice_source"`
	Providers    []string `json:"providers"`
}

// List returns the Models page.
func (s *Service) List(ctx context.Context) (View, error) {
	v := View{Profiles: []Profile{}, Fallback: []string{}}
	if s.st != nil {
		profiles, err := s.st.ListModelProfiles(ctx)
		if err != nil {
			return v, err
		}
		for _, p := range profiles {
			v.Profiles = append(v.Profiles, s.profile(p.Name, p.Config, "console", p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"), p.UpdatedBy))
		}
	}
	if s.st == nil {
		for name, m := range s.cfg.Models {
			v.Profiles = append(v.Profiles, s.profile(name, m.WithoutKey(), "config", "", ""))
		}
		v.Default, v.Fallback = s.cfg.Model, append([]string{}, s.cfg.Fallback...)
		if v.Default != "" {
			v.ChoiceSource = "config"
		}
	}
	sort.Slice(v.Profiles, func(i, j int) bool { return v.Profiles[i].Name < v.Profiles[j].Name })
	if s.st != nil {
		c, found, err := s.choice(ctx)
		if err != nil {
			return v, err
		}
		if found && c.Default != "" {
			v.Default, v.Fallback, v.ChoiceSource = c.Default, append([]string{}, c.Fallback...), "console"
		}
	}
	for p := range Providers {
		v.Providers = append(v.Providers, p)
	}
	sort.Strings(v.Providers)
	return v, nil
}

func (s *Service) profile(name string, m config.ModelProfile, source, at, by string) Profile {
	p := Profile{Name: name, Config: m, Source: source, UpdatedAt: at, UpdatedBy: by}
	p.KeyEnv = m.String("api_key_env")
	if p.KeyEnv == "" {
		p.KeyEnv = Providers[m.String("provider")]
	}
	if source == "config" && s.cfg.Models[name].String("api_key") != "" {
		p.KeyEnv, p.KeySet, p.KeyInConfig = "", true, true // a literal key (or ${VAR}) in the config file
		return p
	}
	if p.KeyEnv == "" {
		p.KeySet = true // nothing to set
	} else {
		p.KeySet = s.Getenv(p.KeyEnv) != ""
	}
	return p
}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

// reservedEnv are the console's own secrets: a profile can't name them as
// its key, or the key would leave the console with the review job.
var reservedEnv = map[string]bool{
	"DATABASE_URL": true, "GITHUB_TOKEN": true, "GITHUB_WEBHOOK_SECRET": true, "GITHUB_APP_PRIVATE_KEY": true,
	"GITLAB_TOKEN": true, "GITLAB_WEBHOOK_SECRET": true, "BITBUCKET_TOKEN": true, "BITBUCKET_API_TOKEN": true,
	"BITBUCKET_WEBHOOK_SECRET": true,
}

// CheckKeyEnv rejects env var names that aren't valid or that hold the
// console's own secrets.
func CheckKeyEnv(name string) error {
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("api_key_env %q: use an environment variable name such as OPENAI_API_KEY", name)
	}
	if reservedEnv[name] || strings.HasPrefix(name, "CODEREVIEW_CONSOLE_") || strings.HasPrefix(name, "CODEREVIEW_KEY_") {
		return fmt.Errorf("api_key_env %q holds one of the console's own secrets: use a separate variable for the model key", name)
	}
	return nil
}

// ErrInUse is returned when deleting a profile something still uses.
var ErrInUse = errors.New("in use")

// Validate checks a profile before it is saved.
func Validate(name string, m config.ModelProfile) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("name %q: use lowercase letters, digits, '.', '_' or '-' (up to 63)", name)
	}
	provider := m.String("provider")
	if _, ok := Providers[provider]; !ok {
		return fmt.Errorf("provider %q is not known", provider)
	}
	if strings.TrimSpace(m.String("model")) == "" {
		return errors.New("model is required")
	}
	if _, ok := m["api_key"]; ok {
		return errors.New("API keys are not stored in the console: put the key in an environment variable of the console and set api_key_env to its name")
	}
	if env := m.String("api_key_env"); env != "" {
		if err := CheckKeyEnv(env); err != nil {
			return err
		}
	}
	if _, ok := m["headers"]; ok {
		return errors.New("headers are not supported in the console (they could hold secrets): use the config file for this profile")
	}
	if b := m.String("base_url"); b != "" {
		u, err := url.Parse(b)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("base_url %q: want an http(s) URL", b)
		}
	} else if provider == "azure" || provider == "openai-compatible" {
		return fmt.Errorf("the %s provider needs base_url", provider)
	}
	return nil
}

// Put creates or replaces a console profile.
func (s *Service) Put(ctx context.Context, name string, m config.ModelProfile, actor string) error {
	if s.st == nil {
		return errors.New("model profiles need a database")
	}
	clean := config.ModelProfile{}
	for k, v := range m {
		if str, ok := v.(string); ok {
			if str = strings.TrimSpace(str); str == "" {
				continue // unset
			}
			v = str
		}
		if v != nil {
			clean[k] = v
		}
	}
	if err := Validate(name, clean); err != nil {
		return err
	}
	return s.st.PutModelProfile(ctx, name, clean, actor)
}

// Delete removes a console profile. It refuses while the console's default
// or fallback names it, or while a repository is set to use it.
func (s *Service) Delete(ctx context.Context, name string, actor string) error {
	if s.st == nil {
		return store.ErrNotFound
	}
	if c, found, err := s.choice(ctx); err != nil {
		return err
	} else if found && (c.Default == name || contains(c.Fallback, name)) {
		return fmt.Errorf("%w: %s is the default or a fallback model; choose another first", ErrInUse, name)
	}
	repos, err := s.st.ListRepos(ctx)
	if err != nil {
		return err
	}
	var users []string
	for _, r := range repos {
		if r.Settings.Model != nil && *r.Settings.Model == name {
			users = append(users, r.Path)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("%w: repositories set to use %s: %s", ErrInUse, name, strings.Join(users, ", "))
	}
	return s.st.DeleteModelProfile(ctx, name, actor)
}

// importedKey marks that the config file's models were copied once.
const importedKey = "models_imported"

// ImportActor is the audit actor of the copy from the config file.
const ImportActor = "config file (initial)"

// Import copies the config file's model profiles, default and fallbacks
// into the database, once: profiles the database doesn't have, and the
// default when none is saved. Profiles the admin panel can't hold (a
// literal api_key, or headers) are skipped, with the reason in skipped.
func (s *Service) Import(ctx context.Context) (imported, skipped []string, err error) {
	if s.st == nil || (len(s.cfg.Models) == 0 && s.cfg.Model == "") {
		return nil, nil, nil
	}
	var done bool
	if _, err := s.st.GetSetting(ctx, importedKey, &done); err != nil || done {
		return nil, nil, err
	}
	have := map[string]bool{}
	profiles, err := s.st.ListModelProfiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range profiles {
		have[p.Name] = true
	}
	names := make([]string, 0, len(s.cfg.Models))
	for n := range s.cfg.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if have[name] {
			continue
		}
		if err := s.Put(ctx, name, s.cfg.Models[name], ImportActor); err != nil {
			skipped = append(skipped, fmt.Sprintf("models.%s: %v", name, err))
			continue
		}
		have[name] = true
		imported = append(imported, name)
	}
	if s.cfg.Model != "" {
		if _, found, err := s.choice(ctx); err != nil {
			return imported, skipped, err
		} else if !found {
			if err := s.SetChoice(ctx, Choice{Default: s.cfg.Model, Fallback: s.cfg.Fallback}, ImportActor); err != nil {
				skipped = append(skipped, fmt.Sprintf("model/fallback: %v", err))
			} else {
				imported = append(imported, "default model "+s.cfg.Model)
			}
		}
	}
	return imported, skipped, s.st.PutSetting(ctx, importedKey, true, ImportActor)
}

// SetChoice stores the default model and fallbacks. Every name must be a
// profile; the fallbacks are tried in order when the default fails.
func (s *Service) SetChoice(ctx context.Context, c Choice, actor string) error {
	if s.st == nil {
		return errors.New("the default model needs a database")
	}
	names, err := s.Names(ctx)
	if err != nil {
		return err
	}
	if c.Default == "" {
		return errors.New("choose a default model")
	}
	if !contains(names, c.Default) {
		return fmt.Errorf("%q is not a model profile", c.Default)
	}
	seen := map[string]bool{c.Default: true}
	fb := []string{}
	for _, f := range c.Fallback {
		if !contains(names, f) {
			return fmt.Errorf("fallback %q is not a model profile", f)
		}
		if seen[f] {
			return fmt.Errorf("fallback %q is listed twice (or is the default)", f)
		}
		seen[f] = true
		fb = append(fb, f)
	}
	return s.st.PutSetting(ctx, SettingKey, Choice{Default: c.Default, Fallback: fb}, actor)
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
