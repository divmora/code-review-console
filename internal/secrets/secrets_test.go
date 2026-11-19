package secrets

import (
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, err := New("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") // hex
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Seal([]byte("glpat-secret"))
	if err != nil || !strings.HasPrefix(s, "v1:") || strings.Contains(s, "glpat") {
		t.Fatalf("sealed = %q %v", s, err)
	}
	if s2, _ := b.Seal([]byte("glpat-secret")); s2 == s {
		t.Error("each seal must use a fresh nonce")
	}
	pt, err := b.Open(s)
	if err != nil || string(pt) != "glpat-secret" {
		t.Fatalf("open = %q %v", pt, err)
	}
	other, _ := New("a passphrase that is long enough to be a key!!")
	if _, err := other.Open(s); err == nil {
		t.Error("a different key must not decrypt")
	}
	var none *Box
	if _, err := none.Seal([]byte("x")); err != ErrNoKey {
		t.Errorf("no key: %v", err)
	}
	if _, err := New("short"); err == nil {
		t.Error("short keys must be rejected")
	}
}
