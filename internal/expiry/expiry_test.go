package expiry

import (
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/hosts"
)

func TestWarnings(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if DaysLeft(now.Add(36*time.Hour), now) != 2 || DaysLeft(now.Add(-time.Hour), now) != -1 {
		t.Error("days left round up, and are negative once past")
	}
	if _, ok := License(license.Status{State: license.Active, Expires: now.Add(20 * 24 * time.Hour)}, now); ok {
		t.Error("no warning three weeks ahead")
	}
	w, ok := License(license.Status{State: license.Active, Expires: now.Add(10 * 24 * time.Hour)}, now)
	if !ok || w.Severity != Warn || w.Days != 10 || !strings.Contains(w.Message, "3 Dec 2026") {
		t.Errorf("license within two weeks: %+v", w)
	}
	if _, ok := License(license.Status{State: license.InGrace, Expires: now.Add(-24 * time.Hour)}, now); ok {
		t.Error("the grace period has its own banner")
	}
	if _, ok := License(license.Status{State: license.Active}, now); ok {
		t.Error("a perpetual license never warns")
	}
	h := hosts.Host{ID: 3, Host: "gitlab.example.com"}
	if _, ok := Host(h, hosts.TokenExpiry{Known: true}, now); ok {
		t.Error("a token that never expires")
	}
	if _, ok := Host(h, hosts.TokenExpiry{}, now); ok {
		t.Error("unknown expiry")
	}
	w, ok = Host(h, hosts.TokenExpiry{Known: true, Expires: now.Add(24 * time.Hour)}, now)
	if !ok || w.Severity != Warn || !strings.Contains(w.Message, "tomorrow") || w.HostID != 3 || w.Name != "gitlab.example.com" {
		t.Errorf("token tomorrow: %+v", w)
	}
	w, ok = Host(h, hosts.TokenExpiry{Known: true, Expires: now.Add(-24 * time.Hour)}, now)
	if !ok || w.Severity != Bad || !strings.Contains(w.Message, "expired") {
		t.Errorf("expired token: %+v", w)
	}
	// A GitLab token dated 22 Dec stops at the start of that day (issue #132).
	gl := time.Date(2026, 12, 22, 0, 0, 0, 0, time.UTC)
	w, ok = Host(h, hosts.TokenExpiry{Known: true, Expires: gl}, gl.Add(-10*time.Hour))
	if !ok || !strings.Contains(w.Message, "expires on 22 Dec 2026, 00:00 UTC") || w.Days != 1 {
		t.Errorf("GitLab cut-off: %+v", w)
	}
	if w, ok = Host(h, hosts.TokenExpiry{Known: true, Expires: gl}, gl.Add(time.Hour)); !ok || w.Severity != Bad {
		t.Errorf("on its date the token is already expired: %+v", w)
	}
}
