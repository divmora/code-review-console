// Package licensetest makes license Managers for tests, signed with a
// throwaway key that production builds do not trust.
package licensetest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	lg "github.com/divmora/license-go/pkg/license"
)

// Key signs DIV1 licenses with a fresh test key.
type Key struct {
	priv ed25519.PrivateKey
	Ring *lg.KeyRing
}

// NewKey makes a test signing key.
func NewKey(t testing.TB) *Key {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Key{priv: priv, Ring: lg.NewKeyRing(pub)}
}

// Sign signs claims as a DIV1 token.
func (k *Key) Sign(t testing.TB, c lg.Claims) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return lg.EncodeToken(raw, ed25519.Sign(k.priv, []byte("DIV1."+payload)))
}

// Issue signs a console license valid for d with the given features.
func (k *Key) Issue(t testing.TB, d time.Duration, features ...license.Feature) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	fs := make([]string, len(features))
	for i, f := range features {
		fs[i] = string(f)
	}
	return k.Sign(t, lg.Claims{ID: "7b0c6f1e-3f43-4a52-9d1e-2b8f0a9c1d11", Customer: lg.Customer{Name: "Test Co"}, Product: license.Product,
		Plan: "enterprise", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(d), Features: fs})
}

// Manager returns an empty Manager trusting k.
func (k *Key) Manager(t testing.TB) *license.Manager {
	t.Helper()
	v, err := license.NewValidator(k.Ring)
	if err != nil {
		t.Fatal(err)
	}
	return license.New(v)
}

// Manager returns a Manager with the features installed.
func Manager(t testing.TB, features ...license.Feature) *license.Manager {
	t.Helper()
	k := NewKey(t)
	m := k.Manager(t)
	if err := m.Install(k.Issue(t, 24*time.Hour, features...), "env"); err != nil {
		t.Fatal(err)
	}
	return m
}

// Full returns a Manager with every feature.
func Full(t testing.TB) *license.Manager { return Manager(t, "*") }
