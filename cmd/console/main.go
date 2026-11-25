// Command console runs Divmora Code Review Console: webhooks, the review
// queue, executors that run the codereview CLI, the admin API and UI.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/divmora/code-review-console/internal/dashboards"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/divmora/code-review-console/internal/admin"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/execsettings"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/models"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/server"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/tuning"
	"github.com/divmora/code-review-console/internal/version"
)

const usage = `console — Divmora Code Review Console

Usage:
  console serve [--config console.yaml] [--addr :8080]   Run webhooks, queue, executors, API and UI
  console check-config [--config console.yaml]          Validate the config with the CLI the executor runs
  console hosts export|import ...                       Move code hosts and webhooks as YAML (console hosts help)
  console env                                           List the environment variables the console reads
  console version

The config file is optional: every startup setting can be an environment
variable (console env), and an environment variable wins over the file.
Everything else is managed in the admin panel.
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
	case "hosts":
		err = hostsCmd(ctx, os.Args[2:])
	case "env":
		printEnv()
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
	path := fs.String("config", "", "config file, optional (default $CODEREVIEW_CONSOLE_CONFIG, or ./console.yaml if it exists)")
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
	// Model key variables and the extra variables set in the admin panel
	// are added to each job by the server; these are the provider defaults.
	pass := executor.PassthroughNames()
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
	// The console keeps its configuration (code hosts, models, settings,
	// executor settings, the license) in the database, with secrets
	// encrypted by the secret key.
	logger.Info("configuration", "file", cmp.Or(cfg.File(), "none"), "environment", cfg.EnvUsed())
	if r, err := cfg.ClientResolver(); err == nil && len(r.WideRanges()) > 0 {
		logger.Warn("trusted proxy ranges this wide usually hold clients too, and a client connecting from a trusted address can choose the IP recorded in the audit log, sessions and webhooks: "+
			"list only the load balancer's own addresses, or use CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS", "ranges", r.WideRanges())
	}
	if cfg.Database.URL == "" {
		return errors.New("DATABASE_URL (or database.url) is required: the console keeps its configuration in PostgreSQL")
	}
	box, err := secrets.FromEnv()
	if err != nil {
		return err
	}
	if box == nil {
		return errors.New(secrets.EnvKey + " is required: it encrypts code host tokens in the database (create one with: openssl rand -base64 32)")
	}
	// from says where first-start values come from, for the log.
	from := " with the starting values"
	if cfg.File() != "" {
		from = " from the config file (with the starting values for what it doesn't set)"
	}
	var deps server.Deps
	st, err := store.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer st.Close()
	deps.Store = st
	logger.Info("database connected; schema up to date")
	lic := loadLicense(ctx, cfg, deps.Store, logger)
	if err := requireExecutor(cfg, lic); err != nil {
		// Start anyway, so the license can be added in the admin panel;
		// reviews are refused until it is (see licensedExecutor).
		logger.Error("reviews are paused until a license is added in the admin panel", "error", err)
	}
	warnSSORules(cfg, lic, logger)
	exec, err := newExecutor(cfg)
	if err != nil {
		return err
	}
	execSvc := execsettings.New(cfg.Executor, deps.Store)
	if execSvc.Editable() {
		seeded, differs, err := execSvc.Seed(ctx)
		if err != nil {
			return fmt.Errorf("executor settings: %w", err)
		}
		if seeded {
			logger.Info("executor settings saved in the database" + from + "; change them in the admin panel (Executor) from now on")
		}
		for _, d := range differs {
			logger.Warn("config file value ignored: the executor setting saved in the admin panel wins", "setting", "executor."+cfg.Executor.Type+"."+d)
		}
		if saved, ok, err := execSvc.Get(ctx); err == nil && ok {
			if name := execsettings.IgnoredCASecret(cfg.Executor, saved); name != "" {
				logger.Warn("the CA bundle Secret saved on the Executor page isn't mounted into review pods: put the certificates in a ConfigMap and choose it there, or name the Secret in the config file (executor.kubernetes.ca_bundle.secret)", "secret", name)
			}
		}
		base := exec
		exec = execsettings.NewExecutor(execSvc, base, func(e config.Executor) (executor.Executor, error) {
			if k, ok := base.(*executor.Kubernetes); ok {
				return k.With(e.Kubernetes) // same client and namespace
			}
			c := *cfg
			c.Executor = e
			return newExecutor(&c)
		}, logger)
	}
	if cfg.Executor.Type != "local" {
		exec = &licensedExecutor{Executor: exec, lic: lic}
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
			logger.Info("settings saved in the database" + from + "; change them in the admin panel from now on")
		}
		for _, d := range differs {
			logger.Info("config file value ignored: the database value wins (change it in the admin panel)", "setting", d)
		}
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
	deps.Tuning = tuning.New(cfg, deps.Store)
	if copied, err := deps.Tuning.Import(ctx); err != nil && !copied {
		return fmt.Errorf("queue and review engine settings: %w", err)
	} else if copied {
		logger.Info("queue, review engine, bot name and dashboard token settings saved in the database" + from + "; change them in the admin panel (Settings) from now on")
		if err != nil {
			logger.Error("part of the config file's review: section was not copied; set it in the admin panel", "error", err)
		}
	}
	settingsSvc, tuningSvc := deps.Settings, deps.Tuning
	deps.Dashboards = dashboards.New(deps.Store, func(ctx context.Context) dashboards.Legacy {
		return dashboards.LegacyFrom(settingsSvc.Runtime(ctx), tuningSvc.Get(ctx))
	})
	deps.Models = models.New(cfg, deps.Store)
	imported, skipped, err := deps.Models.Import(ctx)
	if err != nil {
		return fmt.Errorf("model profiles: %w", err)
	}
	if len(imported) > 0 {
		logger.Info("model profiles copied from the config file into the database; manage them in the admin panel (Models) from now on", "imported", imported)
	}
	if blocked, err := deps.Models.BlockedKeyEnvs(ctx); err == nil && len(blocked) > 0 {
		logger.Warn("model profiles whose api_key_env isn't a model key variable get no key: rename the variable to the provider's key name (such as OPENAI_API_KEY) or "+models.ModelKeyPrefix+"<NAME>, and update the profile under Models",
			"profiles", blocked)
	}
	for _, sk := range skipped {
		logger.Error("model profile not copied from the config file; add it under Models in the admin panel", "problem", sk)
	}
	if keys := cfg.RemovedKeys(); len(keys) > 0 {
		logger.Warn("these config file keys are managed in the admin panel now: each is copied into the database only while it has no value there yet, and ignored after that; remove them",
			"keys", keys)
	}
	if cfg.Admin.Enabled {
		adm, err := admin.New(ctx, cfg.Admin, deps.Store, logger)
		if err != nil {
			return err
		}
		adm.Settings = deps.Settings
		adm.Lic = lic
		adm.Hosts = deps.Hosts
		adm.ExecutorType = cfg.Executor.Type
		adm.Models = deps.Models
		adm.Tuning = deps.Tuning
		adm.Box = box
		adm.Executor = execSvc
		adm.Dashboards = deps.Dashboards
		deps.Admin = adm
		logger.Info("admin enabled", "url", cfg.Admin.BaseURL, "issuer", cfg.Admin.OIDC.Issuer)
	}
	deps.Box = box
	srv, err := server.New(cfg, exec, logger, deps)
	if err != nil {
		return err
	}
	if deps.Admin != nil {
		deps.Admin.Reviewer = srv
		deps.Admin.Labels = srv
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
	// Check what reviews will run: the executor settings, model profiles
	// and license saved in the admin panel. The database is only read (no
	// migrations, nothing written).
	if cfg.Database.URL == "" {
		return errors.New("DATABASE_URL (or database.url) is required: the console keeps its configuration in PostgreSQL")
	}
	pg, err := store.Connect(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pg.Close()
	var st store.Store = readOnlyStore{pg}
	lic := loadLicense(ctx, cfg, st, slog.New(slog.DiscardHandler))
	run, source, err := checkExecutorConfig(ctx, cfg, st)
	if err != nil {
		return err
	}
	exec, err := newExecutor(run)
	if err != nil {
		return err
	}
	if err := requireExecutor(cfg, lic); err != nil {
		fmt.Printf("note: %v\n      (the console still starts; reviews wait until the license is added in the admin panel)\n", err)
	}
	rt, rtSource := checkRuntimeSettings(ctx, cfg, st)
	tn := tuning.New(cfg, st).Get(ctx)
	fmt.Printf("executor: %s\nworkers: %d\nadmin: %v\nallowlist: %v%s\nrequire reviewer: %v%s\ndatabase: %v\n", exec.Name(), tn.Workers, cfg.Admin.Enabled,
		rt.Allowlist, rtSource, rt.RequireReviewer, rtSource, cfg.Database.URL != "")
	if source != "" {
		fmt.Printf("executor settings: %s\n", source)
	}
	if keys := cfg.RemovedKeys(); len(keys) > 0 {
		fmt.Printf("note: managed in the admin panel now, remove from the config file: %s\n", strings.Join(keys, ", "))
	}
	ms := models.New(cfg, st)
	profiles, defModel, fallback, err := ms.Effective(ctx)
	if err != nil {
		return err
	}
	if len(profiles) == 0 && defModel == "" {
		fmt.Println("models: none yet; add one under Models in the admin panel (skipping the CLI check)")
		return nil
	}
	yml, env, err := server.EngineConfigWith(tn, profiles, defModel, fallback)
	if err != nil {
		return err
	}
	if keys, err := ms.KeyEnvs(ctx); err == nil {
		for k, v := range keys { // keys of profiles managed in the admin panel
			if _, taken := env[k]; !taken {
				env[k] = v
			}
		}
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

// checkRuntimeSettings returns the settings reviews use now (the admin
// panel's when saved, else the config file's) and a note saying where they
// come from.
func checkRuntimeSettings(ctx context.Context, cfg *config.Config, st store.Store) (settings.Effective, string) {
	svc := settings.New(cfg, nil)
	if st == nil {
		return svc.Get(ctx), ""
	}
	var stored settings.Stored
	found, err := st.GetSetting(ctx, settings.Key, &stored)
	switch {
	case err != nil:
		fmt.Printf("note: settings in the database: %v (showing the config file's)\n", err)
		return svc.Get(ctx), " (config file)"
	case !found:
		return svc.Get(ctx), " (config file; copied into the admin panel on the next start)"
	}
	return settings.New(cfg, st).Get(ctx), " (admin panel)"
}

// checkExecutorConfig returns the config with the executor settings saved
// in the admin panel applied, and a line saying where they come from.
func checkExecutorConfig(ctx context.Context, cfg *config.Config, st store.Store) (*config.Config, string, error) {
	es := execsettings.New(cfg.Executor, st)
	if !es.Editable() {
		return cfg, "", nil
	}
	saved, found, err := es.Get(ctx)
	if err != nil {
		fmt.Printf("note: executor settings in the database: %v (using the config file's)\n", err)
		return cfg, "config file", nil
	}
	if !found {
		return cfg, "config file (not saved in the admin panel yet; copied there on the next start)", nil
	}
	c := *cfg
	c.Executor = execsettings.Apply(cfg.Executor, saved)
	return &c, "admin panel (version " + saved.Version + ")", nil
}

// readOnlyStore lets check-config read the database without writing to it
// (loadLicense would otherwise copy license.file into it).
type readOnlyStore struct{ store.Store }

var errReadOnly = errors.New("check-config does not write to the database")

func (readOnlyStore) PutSetting(context.Context, string, any, string) error { return errReadOnly }
func (readOnlyStore) PutSettingAudited(context.Context, string, any, any, string) error {
	return errReadOnly
}

// loadLicense reads the license key: $CODEREVIEW_CONSOLE_LICENSE (an
// override, e.g. when nobody can sign in), then the key managed in the admin
// panel (database). license.file is deprecated: when the database has no key
// yet, a valid key from the file is copied into it once, and from then on
// the admin panel is where the license changes. Without a database the file
// is read as before. Without a valid key the console runs the free core.
func loadLicense(ctx context.Context, cfg *config.Config, st store.Store, logger *slog.Logger) *license.Manager {
	return loadLicenseInto(ctx, license.Default(), cfg, st, logger)
}

// loadLicenseInto is loadLicense with the Manager given (tests trust a test key).
func loadLicenseInto(ctx context.Context, lic *license.Manager, cfg *config.Config, st store.Store, logger *slog.Logger) *license.Manager {
	key, source := strings.TrimSpace(os.Getenv("CODEREVIEW_CONSOLE_LICENSE")), "env"
	var stored admin.StoredLicense
	found := false
	if st != nil {
		var err error
		if found, err = st.GetSetting(ctx, admin.LicenseSetting, &stored); err != nil {
			logger.Error("could not read the stored license", "error", err)
		}
		found = found && stored.Key != ""
	}
	if key == "" && found {
		key, source = stored.Key, "database"
	}
	if cfg.License.File != "" {
		switch {
		case st == nil:
			logger.Warn("deprecated: license.file; set database.url and manage the license in the admin panel")
		case found:
			logger.Warn("license.file is ignored: the license is managed in the admin panel (remove license.file)")
		default:
			logger.Warn("deprecated: license.file; its key is copied into the database once, then the license is managed in the admin panel (remove license.file)")
		}
	}
	if key == "" && !found && cfg.License.File != "" {
		b, err := os.ReadFile(cfg.License.File)
		if err != nil {
			lic.Fail("file", err)
			logger.Error("license file unreadable; running the free core", "file", cfg.License.File, "error", err)
			return lic
		}
		key, source = strings.Join(strings.Fields(string(b)), ""), "file"
		if st != nil {
			if c, err := lic.Verify(key); err == nil {
				detail := map[string]any{"id": c.ID, "customer": c.Customer.Name, "plan": c.Plan, "expires_at": c.ExpiresAt, "from": "license.file"}
				if err := st.PutSettingAudited(ctx, admin.LicenseSetting, admin.StoredLicense{Key: key}, detail, "license.file (initial)"); err != nil {
					logger.Error("could not copy the license from license.file into the database", "error", err)
				} else {
					source = "database"
					logger.Info("license copied from license.file into the database; manage it in the admin panel from now on")
				}
			}
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
	if !cfg.Admin.Enabled || len(o.AllowedDomains)+len(o.AllowedGroups)+len(o.OperatorEmails)+len(o.OperatorGroups)+len(o.ViewerEmails)+len(o.ViewerGroups)+len(o.OperatorSubjects)+len(o.ViewerSubjects) == 0 || lic.Has(license.SSORules) {
		return
	}
	logger.Warn("admin.oidc allowed_domains, allowed_groups, operators and viewers are ignored until a license with the sso_rules feature is added; only allowed_emails can sign in (as admins)",
		"allowed_emails", len(o.AllowedEmails))
}

// requireExecutor reports whether the docker and kubernetes executors are
// covered by the license.
func requireExecutor(cfg *config.Config, lic *license.Manager) error {
	if cfg.Executor.Type == "local" {
		return nil
	}
	if err := lic.Require(license.Executors); err != nil {
		return fmt.Errorf("executor.type %s: %w", cfg.Executor.Type, err)
	}
	return nil
}

// licensedExecutor refuses to run jobs while the license does not cover the
// docker or kubernetes executor. It checks on every job, so a key added in
// the admin panel applies without a restart.
type licensedExecutor struct {
	executor.Executor
	lic *license.Manager
}

func (e *licensedExecutor) Run(ctx context.Context, t executor.Task) (*executor.Result, error) {
	if err := e.lic.Require(license.Executors); err != nil {
		return nil, &executor.RunError{Message: "the " + e.Name() + " executor needs a Divmora license with the executors feature: add one under License in the admin panel"}
	}
	return e.Executor.Run(ctx, t)
}

// printEnv lists the environment variables the console reads.
func printEnv() {
	fmt.Println("Startup settings (an environment variable wins over the config file):")
	w := len("CODEREVIEW_CONSOLE_REGISTRY_USERNAME/_PASSWORD")
	for _, e := range config.EnvVars {
		w = max(w, len(e.Name))
	}
	for _, e := range config.EnvVars {
		fmt.Printf("  %-*s  %s", w, e.Name, e.Key)
		if e.Help != "" {
			fmt.Printf(": %s", e.Help)
		}
		fmt.Println()
	}
	fmt.Printf(`
Also:
  %-*s  config file path (optional)
  %-*s  encrypts code host tokens in the database (required)
  %-*s  license key override (else the admin panel's)
  %-*s  read-only registry credentials for the Executor page's version list
Model API keys and cloud credentials are environment variables too: a
model profile names its variable, and provider defaults (OPENAI_API_KEY,
AWS_*, ...) are passed to review jobs.
`, w, "CODEREVIEW_CONSOLE_CONFIG", w, secrets.EnvKey, w, "CODEREVIEW_CONSOLE_LICENSE", w, "CODEREVIEW_CONSOLE_REGISTRY_USERNAME/_PASSWORD")
}
