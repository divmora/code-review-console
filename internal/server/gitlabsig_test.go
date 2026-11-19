package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
)

var testSigningKey = []byte("0123456789abcdef0123456789abcdef")
var testSigningToken = "whsec_" + base64.StdEncoding.EncodeToString(testSigningKey)

// glSign signs like GitLab: HMAC-SHA256 over "{id}.{timestamp}.{body}".
func glSign(key []byte, id string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func signedHeaders(key []byte, id string, ts int64, body []byte) http.Header {
	h := http.Header{}
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", strconv.FormatInt(ts, 10))
	h.Set("webhook-signature", glSign(key, id, ts, body))
	return h
}

func TestVerifyGitLab(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"object_kind":"merge_request"}`)
	both := config.GitLab{WebhookSecret: "legacy", SigningToken: testSigningToken}

	if m, id, err := verifyGitLab(signedHeaders(testSigningKey, "msg_1", now.Unix(), body), body, both, now); err != nil || m != "signature" || id != "msg_1" {
		t.Fatalf("valid signature: %s %s %v", m, id, err)
	}

	h := signedHeaders(testSigningKey, "msg_1", now.Unix(), body)
	h.Set("X-Gitlab-Token", "legacy") // a valid token must not rescue a bad signature
	if _, _, err := verifyGitLab(h, []byte(`{"object_kind":"tampered"}`), both, now); err == nil {
		t.Error("tampered body must fail")
	}
	if _, _, err := verifyGitLab(signedHeaders([]byte("another key, 32 bytes long......"), "msg_1", now.Unix(), body), body, both, now); err == nil {
		t.Error("wrong key must fail")
	}
	old := now.Add(-6 * time.Minute).Unix()
	if _, _, err := verifyGitLab(signedHeaders(testSigningKey, "msg_1", old, body), body, both, now); err == nil || !strings.Contains(err.Error(), "from now") {
		t.Errorf("stale timestamp must fail: %v", err)
	}
	multi := signedHeaders(testSigningKey, "msg_2", now.Unix(), body)
	multi.Set("webhook-signature", "v1,b2xkLXNpZw== "+multi.Get("webhook-signature"))
	if _, _, err := verifyGitLab(multi, body, both, now); err != nil {
		t.Errorf("any matching signature in the list should pass: %v", err)
	}

	legacy := http.Header{}
	legacy.Set("X-Gitlab-Token", "legacy")
	if m, _, err := verifyGitLab(legacy, body, both, now); err != nil || m != "token" {
		t.Errorf("unsigned request should fall back to the secret token: %s %v", m, err)
	}
	legacy.Set("X-Gitlab-Token", "nope")
	if _, _, err := verifyGitLab(legacy, body, both, now); err == nil {
		t.Error("wrong secret token must fail")
	}
	if _, _, err := verifyGitLab(http.Header{}, body, config.GitLab{SigningToken: testSigningToken}, now); err == nil {
		t.Error("signing token only: unsigned requests must fail (no empty-secret match)")
	}
	strict := both
	strict.RequireSignature = true
	legacy.Set("X-Gitlab-Token", "legacy")
	if _, _, err := verifyGitLab(legacy, body, strict, now); err == nil || !strings.Contains(err.Error(), "require_signature") {
		t.Errorf("require_signature must reject unsigned requests: %v", err)
	}
	// Signature headers but no signing token configured: judged by the secret token.
	h = signedHeaders(testSigningKey, "msg_3", now.Unix(), body)
	h.Set("X-Gitlab-Token", "legacy")
	if m, _, err := verifyGitLab(h, body, config.GitLab{WebhookSecret: "legacy"}, now); err != nil || m != "token" {
		t.Errorf("without a signing token, the secret token decides: %s %v", m, err)
	}
}

func TestGitLabSigningKeyAndConfig(t *testing.T) {
	if _, err := gitlabSigningKey("nope"); err == nil {
		t.Error("missing whsec_ prefix must fail")
	}
	if _, err := gitlabSigningKey("whsec_***"); err == nil {
		t.Error("bad base64 must fail")
	}
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	for _, g := range []config.GitLab{
		{BaseURL: "http://127.0.0.1:1", Token: "t"},
		{BaseURL: "http://127.0.0.1:1", Token: "t", SigningToken: "bad"},
		{BaseURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "x", RequireSignature: true},
	} {
		cfg.GitLab = []config.GitLab{g}
		if _, err := New(cfg, &fakeExec{}, quiet, Deps{}); err == nil {
			t.Errorf("config %+v should be rejected", g)
		}
	}
}

func TestGitLabSignedWebhook(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", SigningToken: testSigningToken}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	body := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":3,"action":"update","oldrev":"abc"}}`)
	send := func(h http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(body))
		for k, v := range h {
			req.Header[k] = v
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	h := signedHeaders(testSigningKey, "msg_42", time.Now().Unix(), body)
	if w := send(h); w.Code != http.StatusAccepted {
		t.Fatalf("signed push should queue: %d %s", w.Code, w.Body)
	}
	if w := send(h); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("a replay of the same delivery must not queue again: %d %s", w.Code, w.Body)
	}
	if w := send(http.Header{}); w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned request without a secret token: %d", w.Code)
	}
}

func TestSeveralHooksOneInstance(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"object_kind":"merge_request"}`)
	groupKey := []byte("group hook key, 32 bytes long...")
	inst := config.GitLab{SigningToken: testSigningToken, SigningTokens: []string{"whsec_" + base64.StdEncoding.EncodeToString(groupKey)},
		WebhookSecrets: []string{"project-secret"}}
	if _, _, err := verifyGitLab(signedHeaders(groupKey, "g1", now.Unix(), body), body, inst, now); err != nil {
		t.Errorf("the group hook's signing token should verify: %v", err)
	}
	h := http.Header{}
	h.Set("X-Gitlab-Token", "project-secret")
	if m, _, err := verifyGitLab(h, body, inst, now); err != nil || m != "token" {
		t.Errorf("a project hook's secret from webhook_secrets should verify: %s %v", m, err)
	}

	// The same comment through the system hook and a project hook: the
	// command must run once.
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", SigningToken: testSigningToken, WebhookSecrets: []string{"project-secret"}}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := New(cfg, &fakeExec{}, quiet, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	note := []byte(`{"object_kind":"note","user":{"username":"dev"},"project":{"path_with_namespace":"g/p"},"merge_request":{"iid":3},` +
		`"object_attributes":{"id":99,"note":"@codereview review","noteable_type":"MergeRequest","updated_at":"2026-11-19 10:00:00 UTC"}}`)
	send := func(h http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(note))
		for k, v := range h {
			req.Header[k] = v
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	sys := signedHeaders(testSigningKey, "sys-1", time.Now().Unix(), note)
	sys.Set("X-Gitlab-Event", "System Hook")
	if w := send(sys); w.Code != http.StatusAccepted {
		t.Fatalf("first delivery should queue: %d %s", w.Code, w.Body)
	}
	proj := http.Header{}
	proj.Set("X-Gitlab-Event", "Note Hook")
	proj.Set("X-Gitlab-Token", "project-secret")
	if w := send(proj); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("the same comment via a project hook must not run again: %d %s", w.Code, w.Body)
	}
}
