package license_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	lg "github.com/divmora/license-go/pkg/license"
)

func TestManager(t *testing.T) {
	var free *license.Manager
	if free.Has(license.Rules) || free.Status().State != license.None || free.Require(license.Rules) == nil {
		t.Fatal("nil manager must be the free core")
	}

	k := licensetest.NewKey(t)
	m := k.Manager(t)
	key := k.Issue(t, 48*time.Hour, license.Rules, license.Audit)
	if err := m.Install(key, "database"); err != nil {
		t.Fatal(err)
	}
	if !m.Has(license.Rules) || !m.Has(license.Audit) || m.Has(license.Learnings) {
		t.Errorf("features: %+v", m.Status())
	}
	if st := m.Status(); st.Customer != "Test Co" || st.Plan != "custom" || st.Expires.IsZero() {
		t.Errorf("status: %+v", st)
	}
	// Armored (PEM-style) keys work too.
	if err := m.Install(lg.WrapArmored(key), "database"); err != nil {
		t.Errorf("armored key: %v", err)
	}

	// Expiry: grace, then the free core.
	m.SetClock(func() time.Time { return time.Now().Add(72 * time.Hour) })
	if st := m.Status(); st.State != license.InGrace || !m.Has(license.Rules) {
		t.Errorf("grace: %+v", st)
	}
	m.SetClock(func() time.Time { return time.Now().Add(48*time.Hour + license.Grace + time.Hour) })
	if st := m.Status(); st.State != license.Expired || m.Has(license.Rules) || len(st.Features) != 0 {
		t.Errorf("expired: %+v", st)
	}
	m.SetClock(time.Now)

	// Tampering, foreign keys, other products, garbage: rejected; the old
	// license stays.
	parts := strings.Split(key, ".")
	if err := m.Install(parts[0]+"."+parts[1][:len(parts[1])-2]+"AA."+parts[2], "database"); err == nil {
		t.Error("tampered key accepted")
	}
	other := licensetest.NewKey(t)
	if err := m.Install(other.Issue(t, time.Hour, "*"), "database"); err == nil {
		t.Error("key signed by an untrusted key accepted")
	}
	now := time.Now().UTC()
	otherProduct := k.Sign(t, lg.Claims{ID: "1d3c2b8a-0f8e-4c4b-9a55-5d1f0e2a7b33", Customer: lg.Customer{Name: "X"}, Product: "gitlab-fleet-governor",
		Plan: "enterprise", IssuedAt: now, ExpiresAt: now.Add(time.Hour), Features: []string{"*"}})
	if err := m.Install(otherProduct, "database"); !errors.Is(err, lg.ErrProductMismatch) {
		t.Errorf("a license for another product: %v", err)
	}
	if err := m.Install("hello", "database"); err == nil {
		t.Error("garbage accepted")
	}
	if !m.Has(license.Rules) {
		t.Error("a failed install must keep the previous license")
	}

	// Production keys do not trust test keys.
	if err := license.Default().Install(key, "env"); err == nil {
		t.Error("default keys accepted a test license")
	}

	// Perpetual licenses never expire.
	perpetual := k.Sign(t, lg.Claims{ID: "2e4d3c9b-1a9f-4d5c-8b66-6e2a1f3b8c44", Customer: lg.Customer{Name: "P"}, Product: license.Product,
		Plan: "enterprise", IssuedAt: now.Add(-time.Hour), Features: []string{"*"}})
	if err := m.Install(perpetual, "file"); err != nil {
		t.Fatal(err)
	}
	m.SetClock(func() time.Time { return time.Now().Add(10 * 365 * 24 * time.Hour) })
	if st := m.Status(); st.State != license.Active || !st.Managed || len(st.Features) != len(license.All) {
		t.Errorf("perpetual: %+v", st)
	}

	// Plans include features: the portal writes only add-ons into
	// "features", so an enterprise or trial key with none unlocks everything.
	for _, plan := range []string{"enterprise", "Trial"} {
		key := k.Sign(t, lg.Claims{ID: "3f5e4d0c-2b0a-4e6d-9c77-7f3b2a4c9d55", Customer: lg.Customer{Name: "P"}, Product: license.Product,
			Plan: plan, IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)})
		m := k.Manager(t)
		if err := m.Install(key, "file"); err != nil {
			t.Fatal(err)
		}
		if st := m.Status(); len(st.Features) != len(license.All) {
			t.Errorf("plan %s with no explicit features: %v", plan, st.Features)
		}
	}
	// Any other plan gets only its explicit features (add-ons).
	addon := k.Sign(t, lg.Claims{ID: "4a6f5e1d-3c1b-4f7e-8d88-8a4c3b5d0e66", Customer: lg.Customer{Name: "P"}, Product: license.Product,
		Plan: "starter", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Features: []string{"audit"}})
	m2 := k.Manager(t)
	if err := m2.Install(addon, "file"); err != nil {
		t.Fatal(err)
	}
	if !m2.Has(license.Audit) || m2.Has(license.Rules) {
		t.Errorf("starter + audit add-on: %v", m2.Status().Features)
	}
}
