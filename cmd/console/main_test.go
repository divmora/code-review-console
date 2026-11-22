package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/admin"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/execsettings"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/settings"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLicenseSources(t *testing.T) {
	ctx := context.Background()
	k := licensetest.NewKey(t)
	fileKey := k.Issue(t, 30*24*time.Hour, license.Hosts)
	panelKey := k.Issue(t, 60*24*time.Hour, license.Executors)
	envKey := k.Issue(t, 90*24*time.Hour, license.Rules)
	path := filepath.Join(t.TempDir(), "license.key")
	if err := os.WriteFile(path, []byte(fileKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{License: config.License{File: path}}
	t.Setenv("CODEREVIEW_CONSOLE_LICENSE", "")

	// license.file with an empty database: copied in once, then managed there.
	st := storetest.New()
	lic := loadLicenseInto(ctx, k.Manager(t), cfg, st, quiet)
	if s := lic.Status(); s.Source != "database" || s.Managed || !lic.Has(license.Hosts) {
		t.Fatalf("seeded from the file: %+v", s)
	}
	var stored admin.StoredLicense
	if found, _ := st.GetSetting(ctx, admin.LicenseSetting, &stored); !found || stored.Key != fileKey {
		t.Fatalf("the file key must be stored: %v %q", found, stored.Key)
	}

	// A key replaced in the panel wins over the file on the next start.
	_ = st.PutSetting(ctx, admin.LicenseSetting, admin.StoredLicense{Key: panelKey}, "a@x")
	lic = loadLicenseInto(ctx, k.Manager(t), cfg, st, quiet)
	if !lic.Has(license.Executors) || lic.Has(license.Hosts) {
		t.Errorf("the panel's key must win over license.file: %+v", lic.Status())
	}

	// The environment overrides the panel, and the panel can't replace it.
	t.Setenv("CODEREVIEW_CONSOLE_LICENSE", envKey)
	lic = loadLicenseInto(ctx, k.Manager(t), cfg, st, quiet)
	if s := lic.Status(); s.Source != "env" || !s.Managed || !lic.Has(license.Rules) {
		t.Errorf("env override: %+v", s)
	}
	t.Setenv("CODEREVIEW_CONSOLE_LICENSE", "")

	// An invalid file key is not copied into the database.
	bad := filepath.Join(t.TempDir(), "bad.key")
	_ = os.WriteFile(bad, []byte("DIV1.nope"), 0o600)
	st2 := storetest.New()
	lic = loadLicenseInto(ctx, k.Manager(t), &config.Config{License: config.License{File: bad}}, st2, quiet)
	if found, _ := st2.GetSetting(ctx, admin.LicenseSetting, &stored); found || lic.Status().State != license.Invalid {
		t.Errorf("invalid file key: stored=%v %+v", found, lic.Status())
	}

	// Without a database the file is read as before.
	lic = loadLicenseInto(ctx, k.Manager(t), cfg, nil, quiet)
	if s := lic.Status(); s.Source != "file" || !s.Managed || !lic.Has(license.Hosts) {
		t.Errorf("no database: %+v", s)
	}
}

type okExec struct{ runs int }

func (e *okExec) Name() string { return "kubernetes" }
func (e *okExec) Run(context.Context, executor.Task) (*executor.Result, error) {
	e.runs++
	return &executor.Result{}, nil
}

func TestLicensedExecutorAppliesWithoutRestart(t *testing.T) {
	k := licensetest.NewKey(t)
	lic := k.Manager(t) // no key yet: the free core
	inner := &okExec{}
	ex := &licensedExecutor{Executor: inner, lic: lic}
	_, err := ex.Run(context.Background(), executor.Task{Kind: "review"})
	if err == nil || !strings.Contains(err.Error(), "needs a Divmora license") || inner.runs != 0 {
		t.Fatalf("unlicensed: %v (runs %d)", err, inner.runs)
	}
	if err := lic.Install(k.Issue(t, time.Hour, license.Executors), "database"); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Run(context.Background(), executor.Task{Kind: "review"}); err != nil || inner.runs != 1 {
		t.Errorf("after the key is added: %v (runs %d)", err, inner.runs)
	}
	cfg := &config.Config{Executor: config.Executor{Type: "kubernetes"}}
	if err := requireExecutor(cfg, k.Manager(t)); err == nil {
		t.Error("requireExecutor still reports the missing feature (for logs and check-config)")
	}
}

// TestCheckConfigUsesSavedSettings: check-config runs its Job with the
// executor settings saved in the admin panel, like reviews (issue #45),
// and never writes to the database.
func TestCheckConfigUsesSavedSettings(t *testing.T) {
	ctx := context.Background()
	st := storetest.New()
	cfg := &config.Config{Executor: config.Executor{Type: "kubernetes", Kubernetes: config.KubeExecutor{
		Image: "ghcr.io/divmora/code-review-bot:0.4.0", Namespace: "code-review", TTL: 10 * time.Minute}}}

	run, source, err := checkExecutorConfig(ctx, cfg, readOnlyStore{st})
	if err != nil || run != cfg || !strings.Contains(source, "not saved") {
		t.Fatalf("nothing saved yet: %v %q", err, source)
	}
	saved := execsettings.Settings{Type: "kubernetes", Version: "0.4.1", CPU: "500m", Memory: "1Gi", NodeSelector: map[string]string{"pool": "batch"}}
	_ = st.PutSetting(ctx, execsettings.Key, saved, "a@x")
	run, source, err = checkExecutorConfig(ctx, cfg, readOnlyStore{st})
	k := run.Executor.Kubernetes
	if err != nil || k.Image != "ghcr.io/divmora/code-review-bot:0.4.1" || k.CPU != "500m" || k.Memory != "1Gi" || k.NodeSelector["pool"] != "batch" ||
		k.Namespace != "code-review" || !strings.Contains(source, "admin panel (version 0.4.1)") {
		t.Errorf("saved settings must apply: %+v %q %v", k, source, err)
	}
	if cfg.Executor.Kubernetes.CPU != "" {
		t.Error("the loaded config must not be changed")
	}

	// license.file is not copied into the database by check-config.
	lk := licensetest.NewKey(t)
	path := filepath.Join(t.TempDir(), "license.key")
	_ = os.WriteFile(path, []byte(lk.Issue(t, 24*time.Hour, license.Executors)), 0o600)
	t.Setenv("CODEREVIEW_CONSOLE_LICENSE", "")
	lic := loadLicenseInto(ctx, lk.Manager(t), &config.Config{License: config.License{File: path}}, readOnlyStore{st}, quiet)
	if !lic.Has(license.Executors) {
		t.Error("the file's license still counts for the check")
	}
	var stored admin.StoredLicense
	if found, _ := st.GetSetting(ctx, admin.LicenseSetting, &stored); found {
		t.Error("check-config must not write the license into the database")
	}
}

// TestCheckConfigShowsSavedSettings: check-config prints the allowlist and
// require-reviewer values reviews use, the admin panel's once saved
// (issue #50).
func TestCheckConfigShowsSavedSettings(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{} // repos: removed from the config file
	if rt, src := checkRuntimeSettings(ctx, cfg, nil); rt.Allowlist || src != "" {
		t.Errorf("no database: config file values, no note: %v %q", rt.Allowlist, src)
	}
	st := storetest.New()
	if _, src := checkRuntimeSettings(ctx, cfg, readOnlyStore{st}); !strings.Contains(src, "config file") {
		t.Errorf("nothing saved yet: %q", src)
	}
	yes := true
	_ = st.PutSetting(ctx, settings.Key, settings.Stored{Allowlist: &yes, RequireReviewer: &yes}, "a@x")
	rt, src := checkRuntimeSettings(ctx, cfg, readOnlyStore{st})
	if !rt.Allowlist || !rt.RequireReviewer || src != " (admin panel)" {
		t.Errorf("saved values must be shown: %+v %q", rt, src)
	}
}
