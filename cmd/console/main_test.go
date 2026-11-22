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
	"github.com/divmora/code-review-console/internal/executor"
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
