// Package secrets encrypts values the console keeps in its database (code
// host tokens, webhook secrets) with AES-256-GCM. The key comes from the
// CODEREVIEW_CONSOLE_SECRET_KEY environment variable and never touches the
// database, so a database dump alone does not reveal the tokens.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnvKey names the environment variable that holds the key.
const EnvKey = "CODEREVIEW_CONSOLE_SECRET_KEY"

// prefix versions the format, so the scheme can change later.
const prefix = "v1:"

// ErrNoKey means a secret must be stored or read but no key is configured.
var ErrNoKey = errors.New(EnvKey + " is not set: it is needed to store or read code host tokens")

// Box seals and opens secrets. A nil *Box has no key.
type Box struct {
	aead cipher.AEAD
	raw  []byte // for Derive
}

// FromEnv returns a Box for CODEREVIEW_CONSOLE_SECRET_KEY, or nil when it
// is unset.
func FromEnv() (*Box, error) {
	k := strings.TrimSpace(os.Getenv(EnvKey))
	if k == "" {
		return nil, nil
	}
	return New(k)
}

// New builds a Box. The key is 32 bytes as base64 or hex (e.g. from
// "openssl rand -base64 32"), or any passphrase of at least 32 characters,
// which is hashed to 32 bytes.
func New(key string) (*Box, error) {
	raw, err := decodeKey(key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, raw: raw}, nil
}

func decodeKey(k string) ([]byte, error) {
	k = strings.TrimSpace(k)
	for _, dec := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString, base64.URLEncoding.DecodeString, hex.DecodeString} {
		if b, err := dec(k); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len(k) < 32 {
		return nil, fmt.Errorf("%s must be 32 random bytes as base64 or hex (openssl rand -base64 32), or a passphrase of at least 32 characters", EnvKey)
	}
	sum := sha256.Sum256([]byte(k))
	return sum[:], nil
}

// Derive returns a 32-byte key for label, derived from the console's key
// (HMAC-SHA256), so other keys don't need storing: the same console key
// and label always give the same key. nil for a nil Box.
func (b *Box) Derive(label string) []byte {
	if b == nil {
		return nil
	}
	h := hmac.New(sha256.New, b.raw)
	h.Write([]byte("code-review-console derive v1\x00" + label))
	return h.Sum(nil)
}

// Seal encrypts plaintext.
func (b *Box) Seal(plaintext []byte) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, plaintext, []byte(prefix))
	return prefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Open decrypts a value from Seal.
func (b *Box) Open(sealed string) ([]byte, error) {
	if sealed == "" {
		return nil, nil
	}
	if b == nil {
		return nil, ErrNoKey
	}
	body, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return nil, errors.New("secret: unknown format")
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return nil, errors.New("secret: too short")
	}
	pt, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(prefix))
	if err != nil {
		return nil, fmt.Errorf("secret: cannot decrypt (wrong %s?)", EnvKey)
	}
	return pt, nil
}
