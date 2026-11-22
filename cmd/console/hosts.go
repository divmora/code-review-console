package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/version"
)

const hostsUsage = `Usage:
  console hosts export [--config console.yaml] [--with-secrets] [-o hosts.yaml]
      Write every code host and webhook as YAML (stdout by default). Without
      --with-secrets, tokens and webhook secrets are left out. Reads the
      database when one is configured, otherwise the config file's
      github:, gitlab: and bitbucket: sections.

  console hosts import [--config console.yaml] [--dry-run] [-f hosts.yaml]
      Create or update hosts and webhooks from YAML (stdin by default).
      Hosts match by platform and URL, webhooks by id; nothing is deleted.
      Absent secrets keep their stored value, ${VAR} reads the environment,
      and a webhook without a secret gets a new one (printed once).
      Needs database.url and CODEREVIEW_CONSOLE_SECRET_KEY.
`

func hostsCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, hostsUsage)
		return errors.New("hosts: want export or import")
	}
	switch args[0] {
	case "export":
		return hostsExport(ctx, args[1:])
	case "import":
		return hostsImport(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(hostsUsage)
		return nil
	}
	fmt.Fprint(os.Stderr, hostsUsage)
	return fmt.Errorf("hosts: unknown command %q", args[0])
}

// hostRegistry opens what serve opens: the database (when configured), the
// secret key and the license, and loads the hosts. close releases them.
func hostRegistry(ctx context.Context, cfg *config.Config, logger *slog.Logger) (reg *hosts.Registry, closeFn func(), err error) {
	var st store.Store
	closeFn = func() {}
	if cfg.Database.URL != "" {
		pg, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return nil, nil, err
		}
		st, closeFn = pg, pg.Close
	}
	box, err := secrets.FromEnv()
	if err != nil {
		closeFn()
		return nil, nil, err
	}
	reg = hosts.New(cfg, st, box, logger)
	reg.Lic = loadLicense(ctx, cfg, st, logger)
	if _, err := reg.Init(ctx); err != nil {
		closeFn()
		return nil, nil, fmt.Errorf("hosts: %w", err)
	}
	return reg, closeFn, nil
}

// quietLoad is load for the hosts commands: flags plus the config file,
// with logs (license, seeding) on stderr only when they matter.
func quietLoad(args []string, name string, extra func(fs *flag.FlagSet)) (*config.Config, *slog.Logger, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	path := fs.String("config", "", "config file (default ./console.yaml or $CODEREVIEW_CONSOLE_CONFIG)")
	extra(fs)
	_ = fs.Parse(args)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)
	cfg, err := config.Load(*path)
	return cfg, logger, err
}

func hostsExport(ctx context.Context, args []string) error {
	var withSecrets bool
	var out string
	cfg, logger, err := quietLoad(args, "hosts export", func(fs *flag.FlagSet) {
		fs.BoolVar(&withSecrets, "with-secrets", false, "include tokens and webhook secrets in clear text")
		fs.StringVar(&out, "o", "", "output file (default stdout)")
	})
	if err != nil {
		return err
	}
	reg, closeFn, err := hostRegistry(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeFn()
	f := reg.Export(withSecrets, cfg.Admin.BaseURL)
	sort.SliceStable(f.Hosts, func(i, j int) bool { return f.Hosts[i].Platform < f.Hosts[j].Platform })
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("# Code hosts exported from the %s by console %s.\n", map[string]string{"database": "database", "config": "config file"}[reg.Mode()], version.Version)
	if withSecrets {
		header += "# Contains secrets in clear text: keep it out of Git, or replace values with ${VAR} references.\n"
		fmt.Fprintln(os.Stderr, "warning: the export contains secrets in clear text")
	} else {
		header += "# Secrets are left out; importing this file keeps the stored ones.\n"
	}
	data = append([]byte(header), data...)
	if out == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(out, data, 0o600)
}

func hostsImport(ctx context.Context, args []string) error {
	var dryRun bool
	var in string
	cfg, logger, err := quietLoad(args, "hosts import", func(fs *flag.FlagSet) {
		fs.BoolVar(&dryRun, "dry-run", false, "check the file and show what would change, without writing")
		fs.StringVar(&in, "f", "", "input file (default stdin)")
	})
	if err != nil {
		return err
	}
	var data []byte
	if in == "" {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	} else {
		data, err = os.ReadFile(in)
	}
	if err != nil {
		return err
	}
	f, err := hosts.ParseFile(data)
	if err != nil {
		return err
	}
	reg, closeFn, err := hostRegistry(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeFn()
	res, err := reg.Import(ctx, f, "cli: hosts import", dryRun)
	if err != nil {
		return err
	}
	for _, l := range res.Lines {
		fmt.Println(l)
	}
	if len(res.Secrets) > 0 {
		fmt.Println("\nNew webhook secrets (shown once; set them on the code host with the webhook URL):")
		keys := make([]string, 0, len(res.Secrets))
		for k := range res.Secrets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s: %s\n", k, res.Secrets[k])
		}
	}
	if dryRun {
		fmt.Println("\ndry run: nothing was written")
	}
	return nil
}

// warnConfigHosts logs that the config file's host sections are deprecated.
func warnConfigHosts(cfg *config.Config, mode string, logger *slog.Logger) {
	if len(hosts.FromConfig(cfg)) == 0 {
		return
	}
	if mode == "database" {
		logger.Warn("deprecated: github:, gitlab: and bitbucket: in the config file only seed the database on first start and are otherwise ignored; " +
			"manage hosts in the admin panel (or with `console hosts import`) and remove these sections")
		return
	}
	logger.Warn("deprecated: hosts from the config file (github:, gitlab:, bitbucket:) will be removed in a future release; " +
		"set database.url and " + secrets.EnvKey + ": on the next start they are copied into the database, and from then on managed in the admin panel")
}
