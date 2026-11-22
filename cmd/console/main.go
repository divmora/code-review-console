// Command console runs Divmora Code Review Console: webhooks, the review
// queue, executors that run the codereview CLI, the admin API and UI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/divmora/code-review-console/internal/admin"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/server"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/version"
)

const usage = `console — Divmora Code Review Console

Usage:
  console serve [--config console.yaml] [--addr :8080]   Run webhooks, queue, executors, API and UI
  console check-config [--config console.yaml]          Validate the config with the CLI the executor runs
  console version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, os.Args[2:])
	case "check-config":
		err = checkConfig(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func load(args []string, name string) (*config.Config, *slog.Logger, *flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	path := fs.String("config", "", "config file (default ./console.yaml or $CODEREVIEW_CONSOLE_CONFIG)")
	verbose := fs.Bool("verbose", false, "debug logging")
	addr := fs.String("addr", "", "listen address (overrides server.addr)")
	_ = fs.Parse(args)
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	cfg, err := config.Load(*path)
	if err != nil {
		return nil, nil, nil, err
	}
	if *addr != "" {
		cfg.Server.Addr = *addr
	}
	return cfg, logger, fs, nil
}

// newExecutor builds the configured executor.
func newExecutor(cfg *config.Config) (executor.Executor, error) {
	e := cfg.Executor
	var keyEnvs []string
	for _, m := range cfg.Models {
		keyEnvs = append(keyEnvs, m.String("api_key_env"))
	}
	pass := executor.PassthroughNames(append(keyEnvs, e.Env...)...)
	switch e.Type {
	case "local":
		return &executor.Local{Binary: e.Local.Binary, Pass: pass}, nil
	case "docker":
		d := e.Docker
		return &executor.Docker{Binary: d.Binary, Image: d.Image, Network: d.Network, CPUs: d.CPUs, Memory: d.Memory, Pass: pass}, nil
	case "kubernetes":
		return executor.NewKubernetes(e.Kubernetes, pass)
	}
	return nil, fmt.Errorf("unknown executor %q", e.Type)
}

func serve(ctx context.Context, args []string) error {
	cfg, logger, _, err := load(args, "serve")
	if err != nil {
		return err
	}
	var deps server.Deps
	if cfg.Database.URL != "" {
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		deps.Store = st
		logger.Info("database connected; schema up to date")
	}
	lic := loadLicense(ctx, cfg, deps.Store, logger)
	if err := requireExecutor(cfg, lic); err != nil {
		return err
	}
	warnSSORules(cfg, lic, logger)
	exec, err := newExecutor(cfg)
	if err != nil {
		return err
	}
	deps.License = lic
	deps.Settings = settings.New(cfg, deps.Store)
	deps.Settings.Lic = lic
	if deps.Store != nil {
		seeded, differs, err := deps.Settings.Seed(ctx)
		switch {
		case err != nil:
			return fmt.Errorf("settings: %w", err)
		case seeded:
			logger.Info("settings copied from the config file into the database; change them in the admin panel from now on")
		}
		for _, d := range differs {
			logger.Info("config file value ignored: the database value wins (change it in the admin panel)", "setting", d)
		}
	}
	box, err := secrets.FromEnv()
	if err != nil {
		return err
	}
	deps.Hosts = hosts.New(cfg, deps.Store, box, logger)
	deps.Hosts.Lic = lic
	notes, err := deps.Hosts.Init(ctx)
	if err != nil {
		return fmt.Errorf("hosts: %w", err)
	}
	for _, n := range notes {
		logger.Info(n)
	}
	logger.Info("hosts loaded", "count", len(deps.Hosts.All()), "managed_in", deps.Hosts.Mode())
	if cfg.Admin.Enabled {
		if deps.Store == nil {
			return errors.New("admin.enabled needs database.url")
		}
		profiles := cfg.ProfileNames()
		sort.Strings(profiles)
		adm, err := admin.New(ctx, cfg.Admin, deps.Store, profiles, logger)
		if err != nil {
			return err
		}
		adm.Settings = deps.Settings
		adm.Lic = lic
		adm.Hosts = deps.Hosts
		deps.Admin = adm
		logger.Info("admin enabled", "url", cfg.Admin.BaseURL, "issuer", cfg.Admin.OIDC.Issuer)
	}
	srv, err := server.New(cfg, exec, logger, deps)
	if err != nil {
		return err
	}
	if deps.Admin != nil {
		deps.Admin.Reviewer = srv
	}
	logger.Info("executor ready", "type", exec.Name())
	return srv.ListenAndServe(ctx)
}

// checkConfig validates the console config, then asks the codereview CLI,
// through the configured executor (the same binary or image that runs
// reviews), to validate the engine config the console will hand it.
func checkConfig(ctx context.Context, args []string) error {
	cfg, _, _, err := load(args, "check-config")
	if err != nil {
		return err
	}
	exec, err := newExecutor(cfg)
	if err != nil {
		return err
	}
	if err := requireExecutor(cfg, loadLicense(ctx, cfg, nil, slog.New(slog.DiscardHandler))); err != nil {
		fmt.Printf("note: %v\n      (a key pasted in the admin panel is read from the database when the console starts)\n", err)
	}
	fmt.Printf("executor: %s\nworkers: %d\nadmin: %v\nallowlist: %v\ndatabase: %v\n", exec.Name(), cfg.Server.Workers, cfg.Admin.Enabled, cfg.Repos.Allowlist, cfg.Database.URL != "")
	if len(cfg.Models) == 0 && cfg.Model == "" {
		fmt.Println("models: none in the config file; manage them in the admin UI (skipping the CLI check)")
		return nil
	}
	yml, env, err := server.EngineConfig(cfg)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := exec.Run(cctx, executor.Task{
		ID: fmt.Sprintf("check-config-%d", time.Now().UnixNano()), Kind: "validate", ConfigYAML: yml, Env: env,
		// Every override key the console may send, so a CLI too old to
		// understand one fails here (it rejects unknown keys), not in a review.
		Overrides: config.Overrides{Learnings: []config.Learning{{Text: "check-config probe"}}},
	})
	if err != nil {
		var re *executor.RunError
		if errors.As(err, &re) && strings.Contains(re.Logs, `unknown command "config"`) {
			return fmt.Errorf("the codereview CLI used by the %s executor has no \"config validate\" command; use code-review-bot v0.4.0 or later", exec.Name())
		}
		if errors.As(err, &re) && strings.Contains(re.Logs, "CODEREVIEW_OVERRIDES") && strings.Contains(re.Logs, "unknown field") {
			return fmt.Errorf("the codereview CLI used by the %s executor is too old for this console (it does not know every override key); use code-review-bot v%s or later: %w", exec.Name(), config.MinCLIVersion, err)
		}
		return fmt.Errorf("codereview config validate (%s executor): %w", exec.Name(), err)
	}
	var rep struct {
		Chain   []string `json:"model_chain"`
		Verify  string   `json:"verify_model"`
		Version string   `json:"version"`
	}
	_ = json.Unmarshal(res.Raw, &rep)
	switch ok, known := config.CLIVersionOK(rep.Version); {
	case !ok:
		return fmt.Errorf("the %s executor runs codereview %s; this console needs v%s or later", exec.Name(), rep.Version, config.MinCLIVersion)
	case !known:
		fmt.Printf("note: codereview %q is a development build; release builds must be v%s or later\n", rep.Version, config.MinCLIVersion)
	}
	fmt.Printf("engine config: ok (codereview %s)\nmodel chain: %s\n", rep.Version, strings.Join(rep.Chain, " -> "))
	if rep.Verify != "" {
		fmt.Println("verify model:", rep.Verify)
	}
	return nil
}

// loadLicense reads the license key: $CODEREVIEW_CONSOLE_LICENSE, then
// license.file, then a key pasted in the admin panel (database). Without a
// valid key the console runs the free core.
func loadLicense(ctx context.Context, cfg *config.Config, st store.Store, logger *slog.Logger) *license.Manager {
	lic := license.Default()
	key, source := strings.TrimSpace(os.Getenv("CODEREVIEW_CONSOLE_LICENSE")), "env"
	if key == "" && cfg.License.File != "" {
		b, err := os.ReadFile(cfg.License.File)
		if err != nil {
			lic.Fail("file", err)
			logger.Error("license file unreadable; running the free core", "file", cfg.License.File, "error", err)
			return lic
		}
		key, source = strings.TrimSpace(string(b)), "file"
	}
	if key == "" && st != nil {
		var stored admin.StoredLicense
		if found, err := st.GetSetting(ctx, admin.LicenseSetting, &stored); err != nil {
			logger.Error("could not read the stored license; running the free core", "error", err)
		} else if found {
			key, source = stored.Key, "database"
		}
	}
	if key == "" {
		logger.Info("no license: running the free core (paid features are off)")
		return lic
	}
	if err := lic.Install(key, source); err != nil {
		lic.Fail(source, err)
		logger.Error("license is not valid; running the free core", "source", source, "error", err)
		return lic
	}
	st2 := lic.Status()
	switch st2.State {
	case license.InGrace:
		logger.Warn("license expired; paid features keep working during the grace period", "customer", st2.Customer, "expired", st2.Expires, "grace_until", st2.Expires.Add(license.Grace))
	case license.Expired:
		logger.Error("license expired; running the free core", "customer", st2.Customer, "expired", st2.Expires)
	default:
		logger.Info("license", "customer", st2.Customer, "plan", st2.Plan, "expires", st2.Expires, "features", st2.Features, "source", source)
	}
	return lic
}

// warnSSORules warns when admin domain or group rules are configured but the
// license doesn't cover them, so only allowed_emails can sign in.
func warnSSORules(cfg *config.Config, lic *license.Manager, logger *slog.Logger) {
	o := cfg.Admin.OIDC
	if !cfg.Admin.Enabled || len(o.AllowedDomains)+len(o.AllowedGroups) == 0 || lic.Has(license.SSORules) {
		return
	}
	logger.Warn("admin.oidc allowed_domains and allowed_groups are ignored until a license with the sso_rules feature is added; only allowed_emails can sign in",
		"allowed_emails", len(o.AllowedEmails))
}

// requireExecutor refuses the docker and kubernetes executors without a
// license that covers them.
func requireExecutor(cfg *config.Config, lic *license.Manager) error {
	if cfg.Executor.Type == "local" {
		return nil
	}
	if err := lic.Require(license.Executors); err != nil {
		return fmt.Errorf("executor.type %s: %w (on a first start, provide the key with CODEREVIEW_CONSOLE_LICENSE or license.file, "+
			"or start once with executor.type: local and paste it in the admin panel)", cfg.Executor.Type, err)
	}
	return nil
}
