package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func TestSeedThenDatabaseWins(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Repos.Allowlist = true
	cfg.Server.Dashboard.Title = "From file"
	st := storetest.New()
	svc := New(cfg, st)

	seeded, differs, err := svc.Seed(ctx)
	if err != nil || !seeded || len(differs) != 0 {
		t.Fatalf("first start should copy the config file: %v %v %v", seeded, differs, err)
	}
	e := svc.Get(ctx)
	if !e.Allowlist || e.DashboardTitle != "From file" || e.Source["allowlist"] != "database" {
		t.Fatalf("seeded values: %+v", e)
	}

	// An admin changes a value.
	off := false
	if _, err := svc.Put(ctx, Stored{Allowlist: &off, RequireReviewer: &off, DashboardEnabled: &off, HideRepos: &off, DashboardTitle: "From UI"}, "a@x"); err != nil {
		t.Fatal(err)
	}
	// Restart with the same config file: the database value stays.
	svc2 := New(cfg, st)
	seeded, differs, err = svc2.Seed(ctx)
	if err != nil || seeded {
		t.Fatalf("second start must not seed again: %v %v", seeded, err)
	}
	if len(differs) != 2 || !strings.Contains(strings.Join(differs, ";"), "repos.allowlist: config file true, database false") {
		t.Errorf("differences should be reported: %v", differs)
	}
	if e := svc2.Get(ctx); e.Allowlist || e.DashboardTitle != "From UI" {
		t.Errorf("database must win over the config file: %+v", e)
	}
}

func TestNotYetSavedFallsBackToConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Repos.RequireReviewer = true
	st := storetest.New()
	_ = st.PutSetting(context.Background(), Key, map[string]any{"allowlist": false}, "old version")
	e := New(cfg, st).Get(context.Background())
	if !e.RequireReviewer || e.Source["require_reviewer"] != "config" || e.Source["allowlist"] != "database" {
		t.Errorf("a setting missing from the saved row should come from the config file: %+v", e)
	}
}

func TestValidate(t *testing.T) {
	bad := "wild"
	p := config.Profile(bad)
	if err := (Stored{ReviewDefaults: config.Overrides{Profile: &p}}).Validate(nil, true); err == nil {
		t.Error("invalid review defaults must be rejected")
	}
	if err := (Stored{DashboardTitle: "<script>"}).Validate(nil, true); err == nil {
		t.Error("markup in the title must be rejected")
	}
}

func TestIsBranchSync(t *testing.T) {
	e := Effective{SkipBranchSync: true, SyncSourceBranches: []string{"develop", "release/*"}}
	for _, c := range []struct {
		src, dst, def string
		want          bool
	}{
		{"master", "release-2026-11", "master", true}, // the !1234 case
		{"TICKET-1337", "master", "master", false},
		{"develop", "master", "master", true},
		{"release/2026.11", "hotfix", "main", true},
		{"release", "main", "main", false},
		{"main", "main", "main", false},
		{"master", "release", "", false}, // default branch unknown
	} {
		if got, _ := BranchSync(e.SkipBranchSync, e.SyncSourceBranches, c.src, c.dst, c.def); got != c.want {
			t.Errorf("%s -> %s (default %s): got %v", c.src, c.dst, c.def, got)
		}
	}
	e.SkipBranchSync = false
	if got, _ := BranchSync(e.SkipBranchSync, e.SyncSourceBranches, "master", "release", "master"); got {
		t.Error("switched off: nothing is a branch sync")
	}
	if New(&config.Config{}, nil).Get(context.Background()).SkipBranchSync != true {
		t.Error("skipping branch syncs should be the default")
	}
}

func TestLicensedSwitchesOffPaidSettings(t *testing.T) {
	e := Effective{LearningsEnabled: true, DailyTokenBudget: 5, RepoDailyTokenBudget: 3, SkipBranchSync: true,
		SyncSourceBranches: []string{"develop"}, ReviewRetentionDays: 180, Allowlist: true}
	free := e.Licensed(nil)
	if free.LearningsEnabled || free.DailyTokenBudget != 0 || free.RepoDailyTokenBudget != 0 || free.SkipBranchSync ||
		free.SyncSourceBranches != nil || free.ReviewRetentionDays != license.FreeRetentionDays || !free.Allowlist {
		t.Errorf("free core: %+v", free)
	}
	if full := e.Licensed(licensetest.Full(t)); full.ReviewRetentionDays != 180 || !full.LearningsEnabled || full.DailyTokenBudget != 5 || !full.SkipBranchSync {
		t.Errorf("licensed: %+v", full)
	}
	short := Effective{ReviewRetentionDays: 7}
	if got := short.Licensed(nil).ReviewRetentionDays; got != 7 {
		t.Errorf("a retention under the free cap stays: %d", got)
	}
}

func TestApprovalSettings(t *testing.T) {
	st := storetest.New()
	s := New(&config.Config{}, st)
	e := s.Get(context.Background())
	if e.Approve || e.ApproveBlock != "major" || e.ApproveMinScore != 0 || !e.ApproveWithdraw || e.Base().Approve {
		t.Errorf("defaults: off, blocked by major, withdraw on: %+v", e)
	}
	on, off, score := true, false, 80
	if _, err := s.Put(context.Background(), Stored{Approve: &on, ApproveBlock: "none", ApproveMinScore: &score, ApproveWithdraw: &off}, "a@x"); err != nil {
		t.Fatal(err)
	}
	e = s.Get(context.Background())
	if !e.Approve || e.ApproveBlock != "none" || e.ApproveMinScore != 80 || e.ApproveWithdraw || !e.Base().Approve {
		t.Errorf("saved: %+v", e)
	}
	for _, bad := range []Stored{{ApproveBlock: "high"}, {ApproveMinScore: ptr(101)}, {ApproveMinScore: ptr(-1)}} {
		if err := bad.Validate(nil, true); err == nil {
			t.Errorf("must be rejected: %+v", bad)
		}
	}
}

func ptr(v int) *int { return &v }
