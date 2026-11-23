package tuning

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func loadConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	p := t.TempDir() + "/c.yaml"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestImportOnceFromFile(t *testing.T) {
	ctx := context.Background()
	cfg := loadConfig(t, `
bot_name: acme-bot
allow_repo_model: true
review:
  passes: 2
  verify_model: fast
  defaults: { language: German }
server:
  workers: 8
  debounce: 0s
  dashboard: { token: a-long-dashboard-token, history: 50 }
executor:
  env: [MY_PROXY_CA]
`)
	st := storetest.New()
	s := New(cfg, st)
	copied, err := s.Import(ctx)
	if err != nil || !copied {
		t.Fatal(copied, err)
	}
	got := s.Get(ctx)
	if got.Workers != 8 || got.QueueSize != DefaultQueueSize || got.Debounce != "0s" || got.JobTimeout != "20m" || got.History != 50 {
		t.Errorf("queue: %+v", got)
	}
	if got.BotName != "acme-bot" || !got.AllowRepoModel || len(got.ExtraEnv) != 1 || got.Engine.Passes != 2 || got.Engine.Defaults["language"] != "German" {
		t.Errorf("engine: %+v", got)
	}
	if !got.DashboardTokenOK("a-long-dashboard-token") || got.DashboardTokenOK("wrong") || strings.Contains(got.DashboardTokenHash, "a-long") {
		t.Errorf("the dashboard token is stored hashed: %q", got.DashboardTokenHash)
	}
	// Only once: later file changes are ignored.
	cfg.Server.Workers = 2
	if copied, err := New(cfg, st).Import(ctx); copied || err != nil {
		t.Errorf("second start: %v %v", copied, err)
	}
	if New(cfg, st).Get(ctx).Workers != 8 {
		t.Error("the database wins after the first start")
	}
}

func TestBuiltInDefaults(t *testing.T) {
	ctx := context.Background()
	s := New(loadConfig(t, "server: {addr: ':9000'}\n"), storetest.New())
	if _, err := s.Import(ctx); err != nil {
		t.Fatal(err)
	}
	got := s.Get(ctx)
	if got.Workers != 4 || got.QueueSize != 100 || got.JobTimeoutDuration() != 20*time.Minute || got.DebounceDuration() != 30*time.Second || got.History != 200 {
		t.Errorf("defaults: %+v", got)
	}
	if got.Access() != AccessAdmins || got.DashboardOpen("", false) || !got.DashboardOpen("", true) {
		t.Error("no token set: the dashboard is for signed-in admins")
	}
}

func TestImportReportsUnknownReviewKeys(t *testing.T) {
	s := New(loadConfig(t, "review: {passes: 2, future_key: 1}\n"), storetest.New())
	copied, err := s.Import(context.Background())
	if !copied || err == nil || !strings.Contains(err.Error(), "review.future_key") {
		t.Errorf("copied=%v err=%v", copied, err)
	}
}

func TestPutKeepsAndChangesToken(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := New(&config.Config{Server: config.Server{Dashboard: config.Dashboard{Token: "first-token-long-enough"}}}, st)
	if _, err := s.Import(ctx); err != nil {
		t.Fatal(err)
	}
	var changed Settings
	s.OnChange = func(n Settings) { changed = n }
	base := s.Get(ctx)
	base.Workers = 6
	got, err := s.Put(ctx, Update{Settings: base}, nil, "a@x")
	if err != nil || !got.DashboardTokenOK("first-token-long-enough") || changed.Workers != 6 {
		t.Fatalf("an empty token keeps the stored one: %v %+v", err, got)
	}
	if _, err := s.Put(ctx, Update{Settings: base, DashboardToken: "short"}, nil, "a@x"); err == nil {
		t.Error("short tokens are refused")
	}
	got, _ = s.Put(ctx, Update{Settings: base, DashboardToken: "second-token-long-enough"}, nil, "a@x")
	if got.DashboardTokenOK("first-token-long-enough") || !got.DashboardTokenOK("second-token-long-enough") {
		t.Error("a new token replaces the old one")
	}
	got, _ = s.Put(ctx, Update{Settings: base, ClearToken: true}, nil, "a@x")
	if got.DashboardTokenHash != "" {
		t.Error("clear removes the token")
	}
}

func TestValidate(t *testing.T) {
	ok := Settings{}.Effective()
	for name, c := range map[string]struct {
		mut  func(*Settings)
		want string
	}{
		"workers":       {func(s *Settings) { s.Workers = 100 }, "reviews at once"},
		"timeout":       {func(s *Settings) { s.JobTimeout = "10s" }, "job timeout"},
		"debounce":      {func(s *Settings) { s.Debounce = "nope" }, "debounce"},
		"bot name":      {func(s *Settings) { s.BotName = "has space" }, "bot name"},
		"secret env":    {func(s *Settings) { s.ExtraEnv = []string{"DATABASE_URL"} }, "console's own"},
		"console env":   {func(s *Settings) { s.ExtraEnv = []string{"CODEREVIEW_CONSOLE_SECRET_KEY"} }, "console's own"},
		"bad env":       {func(s *Settings) { s.ExtraEnv = []string{"lower"} }, "environment variable"},
		"passes":        {func(s *Settings) { s.Engine.Passes = 4 }, "passes"},
		"can drop":      {func(s *Settings) { s.Engine.VerifyCanDrop = "some" }, "verify can drop"},
		"verify model":  {func(s *Settings) { s.Engine.VerifyModel = "nope" }, "not a model profile"},
		"repo file":     {func(s *Settings) { s.Engine.RepoConfigFile = "../x" }, "inside the repository"},
		"defaults keys": {func(s *Settings) { s.Engine.Defaults = map[string]any{"bogus": 1} }, "not a repository setting"},
	} {
		s := ok
		c.mut(&s)
		if err := s.Validate(map[string]bool{"fast": true}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
	good := ok
	good.Engine = Engine{VerifyModel: "anthropic:claude", Passes: 3, VerifyCanDrop: "minor", Defaults: map[string]any{"language": "English"}}
	good.ExtraEnv = []string{"MY_PROXY_CA"}
	if err := good.Validate(nil); err != nil {
		t.Errorf("valid settings: %v", err)
	}
}

func TestDashboardAccess(t *testing.T) {
	tok := Settings{DashboardTokenHash: hashToken("the-token-1234567")}
	if tok.Access() != AccessToken || !tok.DashboardOpen("the-token-1234567", false) || tok.DashboardOpen("nope", false) || tok.DashboardOpen("", false) || !tok.DashboardOpen("", true) {
		t.Error("a token set before access existed keeps working, and admins always get in")
	}
	adminsOnly := tok
	adminsOnly.DashboardAccess = AccessAdmins
	if adminsOnly.DashboardOpen("the-token-1234567", false) || !adminsOnly.DashboardOpen("", true) {
		t.Error("admins only: the token doesn't open it")
	}
	public := Settings{DashboardAccess: AccessPublic}
	if !public.DashboardOpen("", false) {
		t.Error("public is open to anyone")
	}
	bad := Settings{DashboardAccess: AccessToken}.Effective()
	if err := bad.Validate(nil); err == nil || !strings.Contains(err.Error(), "dashboard token") {
		t.Errorf("token access needs a token: %v", err)
	}
	bad.DashboardAccess = "everyone"
	if err := bad.Validate(nil); err == nil {
		t.Error("unknown access refused")
	}
}

func TestPutKeepsAccessWithStoredToken(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	s := New(&config.Config{Server: config.Server{Dashboard: config.Dashboard{Token: "first-token-long-enough"}}}, st)
	if _, err := s.Import(ctx); err != nil {
		t.Fatal(err)
	}
	cur := s.Get(ctx)
	cur.DashboardTokenHash = "" // the panel never sees it
	cur.DashboardAccess = AccessToken
	got, err := s.Put(ctx, Update{Settings: cur}, nil, "a@x")
	if err != nil || got.Access() != AccessToken || !got.DashboardOpen("first-token-long-enough", false) {
		t.Fatalf("saving token access keeps the stored token: %v %+v", err, got)
	}
}
