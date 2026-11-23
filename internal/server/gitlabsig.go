package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/config"
)

// gitlabSignatureTolerance bounds the age of a signed GitLab webhook.
// GitLab does not prescribe a window; Standard Webhooks suggests 5 minutes.
const gitlabSignatureTolerance = 5 * time.Minute

// gitlabSigningKey decodes a signing token: "whsec_" plus base64.
func gitlabSigningKey(token string) ([]byte, error) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(token), "whsec_")
	if !ok {
		return nil, errors.New(`signing_token must start with "whsec_" (copy it from the webhook settings in GitLab)`)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(key) == 0 {
		return nil, errors.New("signing_token is not valid base64 after the whsec_ prefix")
	}
	return key, nil
}

// verifyGitLab authenticates a GitLab webhook and returns how ("signature"
// or "token") and, for signed requests, the delivery ID. A request with a
// webhook-signature header is judged by the signature alone when signing
// tokens are configured (any of them may match: system, group and project
// hooks can each have their own); otherwise a secret token is checked,
// unless the instance requires signatures.
// AuthUnsignedToken is the auth of a GitLab delivery accepted by its
// secret token on a webhook (or host) that also has a signing token.
const AuthUnsignedToken = "token-unsigned"

func verifyGitLab(h http.Header, body []byte, inst config.GitLab, now time.Time) (method, deliveryID string, err error) {
	tokens := inst.AllSigningTokens()
	if sig := h.Get("Webhook-Signature"); sig != "" && len(tokens) > 0 {
		id, ts := h.Get("Webhook-Id"), h.Get("Webhook-Timestamp")
		if id == "" || ts == "" {
			return "", "", errors.New("signed webhook without webhook-id or webhook-timestamp")
		}
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return "", "", errors.New("webhook-timestamp is not a Unix time")
		}
		if age := now.Sub(time.Unix(sec, 0)); age > gitlabSignatureTolerance || age < -gitlabSignatureTolerance {
			return "", "", fmt.Errorf("webhook-timestamp is %s from now (allowed ±%s): check clocks or a replay", age.Round(time.Second), gitlabSignatureTolerance)
		}
		for _, tok := range tokens {
			key, err := gitlabSigningKey(tok)
			if err != nil {
				return "", "", err
			}
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte(id + "." + ts + "."))
			mac.Write(body)
			want := []byte("v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			for _, got := range strings.Fields(sig) {
				if hmac.Equal([]byte(got), want) {
					return "signature", id, nil
				}
			}
		}
		return "", "", errors.New("webhook-signature does not match any signing token")
	}
	if inst.RequireSignature {
		return "", "", errors.New("unsigned webhook rejected: this webhook accepts signed deliveries only (it has a signing token, or the host rejects unsigned webhooks)")
	}
	secrets := inst.AllSecrets()
	if len(secrets) == 0 {
		return "", "", errors.New("unsigned webhook and no webhook_secret configured")
	}
	got := []byte(h.Get("X-Gitlab-Token"))
	for _, sec := range secrets {
		if subtle.ConstantTimeCompare(got, []byte(sec)) == 1 {
			if len(tokens) > 0 {
				// Accepted by the secret token although a signing token is
				// set (migration mode): shown on the Webhooks page.
				return AuthUnsignedToken, "", nil
			}
			return "token", "", nil
		}
	}
	return "", "", errors.New("X-Gitlab-Token does not match any webhook secret")
}

// deliveries is the in-memory record of replay keys, used without a
// database or while it can't be reached (see Server.firstDelivery).
type deliveries struct {
	mu   sync.Mutex
	seen map[string]time.Time // key -> expiry
}

// first reports whether key is new (or expired), and records it for ttl.
func (d *deliveries) first(key string, ttl time.Duration, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]time.Time{}
	}
	for k, exp := range d.seen {
		if now.After(exp) {
			delete(d.seen, k)
		}
	}
	if _, dup := d.seen[key]; dup {
		return false
	}
	d.seen[key] = now.Add(ttl)
	return true
}
