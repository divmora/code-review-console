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
	exec, err := newExecutor(cfg)
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
	deps.Settings = settings.New(cfg, deps.Store)
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
	})
	if err != nil {
		var re *executor.RunError
		if errors.As(err, &re) && strings.Contains(re.Logs, `unknown command "config"`) {
			return fmt.Errorf("the codereview CLI used by the %s executor has no \"config validate\" command; use code-review-bot v0.4.0 or later", exec.Name())
		}
		return fmt.Errorf("codereview config validate (%s executor): %w", exec.Name(), err)
	}
	var rep struct {
		Chain   []string `json:"model_chain"`
		Verify  string   `json:"verify_model"`
		Version string   `json:"version"`
	}
	_ = json.Unmarshal(res.Raw, &rep)
	fmt.Printf("engine config: ok (codereview %s)\nmodel chain: %s\n", rep.Version, strings.Join(rep.Chain, " -> "))
	if rep.Verify != "" {
		fmt.Println("verify model:", rep.Verify)
	}
	return nil
}
