// Copyright (c) DIVMORA Technologies. All rights reserved.
// Licensed under the Divmora Enterprise License; see LICENSE-ENTERPRISE.
// Use requires a valid Divmora license. Do not modify or remove license checks.

// Package license checks the console's Divmora license (a DIV1 token from
// github.com/divmora/license-go, issued by the license portal for Product)
// and reports which paid features it unlocks. Keys are verified offline.
package license

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	lg "github.com/divmora/license-go/pkg/license"
)

// Product is the console's product ID in Divmora licenses.
const Product = "code-review-console"

// Feature is a paid capability. The free core never needs a key.
type Feature string

// Paid features (license "features"; "*" grants all).
const (
	// Hosts: more than one code host per platform, and a custom bot
	// identity (mention name, reviewer) per host.
	Hosts Feature = "hosts"
	// Rules: settings for a whole host, a group or a name pattern.
	Rules Feature = "rules"
	// Learnings: team decisions captured from replies and applied to reviews.
	Learnings Feature = "learnings"
	// Executors: the docker and kubernetes executors.
	Executors Feature = "executors"
	// Budgets: daily token budgets.
	Budgets Feature = "budgets"
	// Metrics: charts and rollups, and review history kept longer than
	// FreeRetentionDays.
	Metrics Feature = "metrics"
	// Audit: the audit log in the admin panel.
	Audit Feature = "audit"
	// BranchSync: skipping merge requests that only sync a default branch.
	BranchSync Feature = "branch_sync"
	// SSORules: admin sign-in by email domain or identity-provider group.
	SSORules Feature = "sso_rules"
)

// All lists every paid feature, in display order.
var All = []Feature{Hosts, Rules, Learnings, Executors, Budgets, Metrics, Audit, BranchSync, SSORules}

// Titles name the features for people.
var Titles = map[Feature]string{
	Hosts: "Multiple code hosts and bot identities", Rules: "Group and pattern rules", Learnings: "Team learnings",
	Executors: "Docker and Kubernetes executors", Budgets: "Token budgets", Metrics: "Metrics and long history",
	Audit: "Audit log", BranchSync: "Branch-sync skip", SSORules: "SSO domain and group rules",
}

// FreeRetentionDays caps review history without the Metrics feature.
const FreeRetentionDays = 30

// Grace keeps an expired license working (with a warning) this long,
// unless the license sets its own grace_period_days.
const Grace = 14 * 24 * time.Hour

// ErrFormat is returned for an empty key.
var ErrFormat = errors.New("not a Divmora license key")

// Grants reports whether claims enable f.
func Grants(c *lg.Claims, f Feature) bool { return c != nil && c.HasFeature(string(f)) }

// State of the installed license.
type State string

// States.
const (
	None    State = "none"    // free core
	Active  State = "active"  // valid
	InGrace State = "grace"   // expired, within the grace period: still works
	Expired State = "expired" // free core again
	Invalid State = "invalid" // could not be verified: free core
)

// Status describes the installed license for the admin panel and logs.
type Status struct {
	State    State     `json:"state"`
	Source   string    `json:"source,omitempty"` // env, file or database
	ID       string    `json:"id,omitempty"`
	Customer string    `json:"customer,omitempty"`
	Plan     string    `json:"plan,omitempty"`
	Expires  time.Time `json:"expires_at,omitzero"` // zero: perpetual
	// GraceUntil is when paid features stop after expiry.
	GraceUntil time.Time `json:"grace_until,omitzero"`
	Features   []Feature `json:"features"` // features on now
	Error      string    `json:"error,omitempty"`
	// Managed is true when the key comes from the environment or a file,
	// so it cannot be replaced from the admin panel.
	Managed bool `json:"managed"`
}

// Manager holds the installed license. A nil *Manager is the free core.
type Manager struct {
	v   *lg.Validator
	now func() time.Time

	mu     sync.RWMutex
	claims *lg.Claims
	source string
	err    error
}

// New returns a Manager that verifies keys with v (see NewValidator;
// Default uses Divmora's keys).
func New(v *lg.Validator) *Manager { return &Manager{v: v, now: time.Now} }

// NewValidator builds the validator for Product. Expired keys are accepted
// so the grace period is applied here.
func NewValidator(ring *lg.KeyRing) (*lg.Validator, error) {
	return lg.NewValidatorWithKeyRing(ring, lg.WithProduct(Product), lg.WithAllowExpired(true))
}

// SetClock replaces the clock (tests).
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// Verify checks key without installing it.
func (m *Manager) Verify(key string) (*lg.Claims, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrFormat
	}
	return m.v.Verify(strings.TrimSpace(key))
}

// Install verifies key and makes it the license. An empty key removes it.
// On error the previous license stays.
func (m *Manager) Install(key, source string) error {
	var c *lg.Claims
	if strings.TrimSpace(key) != "" {
		var err error
		if c, err = m.Verify(key); err != nil {
			return err
		}
	} else {
		source = ""
	}
	m.mu.Lock()
	m.claims, m.source, m.err = c, source, nil
	m.mu.Unlock()
	return nil
}

// Fail records a key that could not be installed (shown in the status).
func (m *Manager) Fail(source string, err error) {
	m.mu.Lock()
	m.claims, m.source, m.err = nil, source, err
	m.mu.Unlock()
}

// grace is the license's own grace period, or Grace.
func grace(c *lg.Claims) time.Duration {
	if c.GracePeriodDays > 0 {
		return time.Duration(c.GracePeriodDays) * 24 * time.Hour
	}
	return Grace
}

func (m *Manager) state() (State, *lg.Claims) {
	if m.claims == nil {
		if m.err != nil {
			return Invalid, nil
		}
		return None, nil
	}
	c, now := m.claims, m.now()
	switch {
	case !c.NotBefore.IsZero() && now.Before(c.NotBefore):
		return Invalid, c
	case c.ExpiresAt.IsZero() || now.Before(c.ExpiresAt):
		return Active, c
	case now.Before(c.ExpiresAt.Add(grace(c))):
		return InGrace, c
	}
	return Expired, c
}

// Has reports whether f is on now.
func (m *Manager) Has(f Feature) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, c := m.state()
	return (st == Active || st == InGrace) && Grants(c, f)
}

// Status describes the license.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{State: None, Features: []Feature{}}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, c := m.state()
	out := Status{State: st, Source: m.source, Features: []Feature{}, Managed: m.source == "env" || m.source == "file"}
	if m.err != nil {
		out.Error = m.err.Error()
	}
	if c != nil {
		out.ID, out.Customer, out.Plan, out.Expires = c.ID, c.Customer.Name, c.Plan, c.ExpiresAt
		if !c.ExpiresAt.IsZero() {
			out.GraceUntil = c.ExpiresAt.Add(grace(c))
		}
		if st == Active || st == InGrace {
			for _, f := range All {
				if Grants(c, f) {
					out.Features = append(out.Features, f)
				}
			}
		}
	}
	return out
}

// Error is returned when an action needs a feature the license lacks.
type Error struct{ Feature Feature }

func (e *Error) Error() string {
	return fmt.Sprintf("%s needs a Divmora license with the %q feature (install one in the admin panel under License, or contact licensing@divmora.com)", Titles[e.Feature], e.Feature)
}

// Require returns an *Error unless f is on.
func (m *Manager) Require(f Feature) error {
	if m.Has(f) {
		return nil
	}
	return &Error{Feature: f}
}
