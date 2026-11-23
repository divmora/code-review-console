package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/clientip"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/execsettings"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/models"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeIdP is a minimal OIDC provider: discovery, JWKS, authorize (auto
// approves as the configured user) and a token endpoint that checks PKCE.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	claims map[string]any // claims for the next login
	codes  map[string]codeInfo
	// tamperNonce makes the ID token carry a wrong nonce.
	tamperNonce bool
}

type codeInfo struct {
	nonce, challenge, clientID string
}

func newIdP(t *testing.T) *fakeIdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: key, codes: map[string]codeInfo{}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	iss := f.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
			"jwks_uri": iss + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "PKCE required", 400)
			return
		}
		code := "code-" + q.Get("state")[:8]
		f.mu.Lock()
		f.codes[code] = codeInfo{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), clientID: q.Get("client_id")}
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		ci, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		claims := map[string]any{}
		for k, v := range f.claims {
			claims[k] = v
		}
		tamper := f.tamperNonce
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != ci.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		nonce := ci.nonce
		if tamper {
			nonce = "evil"
		}
		claims["iss"], claims["aud"], claims["nonce"] = iss, ci.clientID, nonce
		claims["iat"], claims["exp"] = time.Now().Unix(), time.Now().Add(time.Hour).Unix()
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		idt, _ := jwt.Signed(signer).Claims(claims).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": idt})
	})
	return f
}

func (f *fakeIdP) as(claims map[string]any) {
	f.mu.Lock()
	f.claims = claims
	f.mu.Unlock()
}

type env struct {
	adm    *Admin
	idp    *fakeIdP
	app    *httptest.Server
	st     *storetest.Memory
	client *http.Client
}

func setup(t *testing.T, oidcCfg config.OIDC) *env {
	t.Helper()
	idp := newIdP(t)
	app := httptest.NewServer(nil)
	t.Cleanup(app.Close)
	oidcCfg.Issuer, oidcCfg.ClientID, oidcCfg.ClientSecret = idp.srv.URL, "crb", "secret"
	if oidcCfg.GroupsClaim == "" {
		oidcCfg.GroupsClaim = "groups"
	}
	st := storetest.New()
	a, err := New(context.Background(), config.Admin{Enabled: true, BaseURL: app.URL, OIDC: oidcCfg, SessionTTL: time.Hour}, st, quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	box, _ := secrets.New("a test passphrase that is long enough for a key")
	a.Lic = licensetest.Full(t)
	// Two profiles copied from the config file; "fast" is the default and
	// needs no key.
	a.Models = models.New(&config.Config{Model: "fast", Models: map[string]config.ModelProfile{
		"fast":  {"provider": "ollama", "model": "qwen2.5-coder:14b"},
		"smart": {"provider": "anthropic", "model": "claude-sonnet-5-5"},
	}}, st)
	if _, skipped, err := a.Models.Import(context.Background()); err != nil || len(skipped) > 0 {
		t.Fatal(err, skipped)
	}
	a.Hosts = hosts.New(&config.Config{}, st, box, quiet)
	a.Hosts.Lic = a.Lic
	if _, err := a.Hosts.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Register(mux)
	resolver, _ := clientip.NewResolver(nil)
	app.Config.Handler = resolver.Middleware(mux)
	jar, _ := cookiejar.New(nil)
	return &env{idp: idp, app: app, st: st, adm: a, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (e *env) login(t *testing.T) *http.Response {
	t.Helper()
	resp, err := e.client.Get(e.app.URL + "/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// csrf loads the app page (checking it is served with a strict CSP) and
// returns the CSRF token from /admin/api/me.
func (e *env) csrf(t *testing.T) string {
	resp, err := e.client.Get(e.app.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(b, []byte(`id="root"`)) {
		t.Fatalf("admin page: %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Fatalf("admin page CSP: %q", csp)
	}
	code, body := e.do(t, "GET", "/admin/api/me", "", nil)
	var me struct {
		CSRF string `json:"csrf"`
	}
	if code != 200 || json.Unmarshal(body, &me) != nil || me.CSRF == "" {
		t.Fatalf("me: %d %s", code, body)
	}
	return me.CSRF
}

func (e *env) do(t *testing.T, method, path, csrf string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.app.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

type fakeReviewer struct{ refs []vcs.Ref }

func (f *fakeReviewer) ReviewNow(_ context.Context, ref vcs.Ref, full bool, by string) error {
	if ref.Host == "unknown.example" {
		return errors.New("no enabled gitlab host for unknown.example")
	}
	f.refs = append(f.refs, ref)
	return nil
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/admin/rules": "/admin/rules", "/admin/settings": "/admin/settings",
		"https://evil.example/admin/rules": "/admin", "//evil.example": "/admin", "/admin/../x": "/admin", "": "/admin", "/admin/nope": "/admin"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginAndManageRepos(t *testing.T) {
	e := setup(t, config.OIDC{AllowedDomains: []string{"divmora.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "Ana@Divmora.com", "email_verified": true, "name": "Ana"})

	// Unauthenticated API is refused; the page redirects to login.
	if code, _ := e.do(t, "GET", "/admin/api/repos", "", nil); code != http.StatusUnauthorized {
		t.Errorf("api without session: %d", code)
	}
	resp := e.login(t)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasSuffix(resp.Request.URL.Path, "/admin") {
		t.Fatalf("login should land on /admin, got %d %s", resp.StatusCode, resp.Request.URL)
	}
	csrf := e.csrf(t)

	_, _ = e.st.TouchRepo(context.Background(), "github", "github.com", "org/api")
	code, body := e.do(t, "GET", "/admin/api/repos", "", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"org/api"`)) {
		t.Fatalf("list: %d %s", code, body)
	}

	upd := map[string]any{"mode": "enabled", "settings": map[string]any{"profile": "chill", "model": "smart", "max_comments": 5}}
	if code, _ := e.do(t, "PUT", "/admin/api/repos/1", "", upd); code != http.StatusForbidden {
		t.Errorf("write without CSRF must be refused: %d", code)
	}
	if code, body := e.do(t, "PUT", "/admin/api/repos/1", csrf, upd); code != 200 || !bytes.Contains(body, []byte(`"effective":{"enabled":true,"source":"repository"`)) {
		t.Fatalf("update: %d %s", code, body)
	}
	bad := map[string]any{"mode": "enabled", "settings": map[string]any{"model": "not-a-profile"}}
	if code, body := e.do(t, "PUT", "/admin/api/repos/1", csrf, bad); code != 400 || !bytes.Contains(body, []byte("not a model profile")) {
		t.Errorf("unknown model should be rejected: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", "/admin/api/repos", csrf, map[string]any{"platform": "gitlab", "host": "gitlab.example.com", "path": "g/sub/p", "mode": "enabled"}); code != 201 {
		t.Errorf("create: %d", code)
	}
	if code, _ := e.do(t, "POST", "/admin/api/repos", csrf, map[string]any{"platform": "github", "path": "../etc"}); code != 400 {
		t.Errorf("bad path should be rejected: %d", code)
	}

	rule := map[string]any{"platform": "gitlab", "host": "gitlab.example.com", "pattern": "G/", "enabled": true, "settings": map[string]any{"profile": "assertive"}}
	if code, body := e.do(t, "POST", "/admin/api/rules", csrf, rule); code != 201 || !bytes.Contains(body, []byte(`"pattern":"g"`)) {
		t.Fatalf("create rule: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", "/admin/api/rules", csrf, map[string]any{"platform": "gitlab", "pattern": "*", "enabled": true}); code != 400 {
		t.Errorf("a lone * should be rejected: %d", code)
	}
	if code, _ := e.do(t, "POST", "/admin/api/rules", csrf, map[string]any{"platform": "gitlab", "pattern": "g", "settings": map[string]any{"model": "nope"}}); code != 400 {
		t.Errorf("rule settings are validated: %d", code)
	}
	_, _ = e.st.TouchRepo(context.Background(), "gitlab", "gitlab.example.com", "g/other/p")
	if _, body := e.do(t, "GET", "/admin/api/repos", "", nil); !bytes.Contains(body, []byte(`"source":"rule g"`)) {
		t.Errorf("repos should show the rule that enables them: %s", body)
	}
	if code, _ := e.do(t, "PUT", "/admin/api/rules/999", csrf, map[string]any{"enabled": false}); code != 404 {
		t.Errorf("unknown rule: %d", code)
	}

	_ = e.st.InsertWebhookEvents(context.Background(), []store.WebhookEvent{{Platform: "gitlab", Project: "g/p", Outcome: store.HookIgnored, Reason: "merge request action update"}})
	if code, body := e.do(t, "GET", "/admin/api/webhooks?outcome=ignored&project=g/", "", nil); code != 200 ||
		!bytes.Contains(body, []byte(`"reason":"merge request action update"`)) || !bytes.Contains(body, []byte(`"counts_24h":{"ignored":1}`)) {
		t.Errorf("webhooks: %d %s", code, body)
	}

	if code, body := e.do(t, "PUT", "/admin/api/settings", csrf, map[string]any{"allowlist": false, "require_reviewer": true,
		"dashboard_enabled": true, "hide_repos": true, "dashboard_title": "Ops", "review_defaults": map[string]any{"max_comments": 9}}); code != 200 ||
		!bytes.Contains(body, []byte(`"require_reviewer":true`)) {
		t.Fatalf("save settings: %d %s", code, body)
	}
	if code, body := e.do(t, "GET", "/admin/api/settings", "", nil); code != 200 || !bytes.Contains(body, []byte(`"dashboard_title":"Ops"`)) {
		t.Errorf("get settings: %d %s", code, body)
	}
	if _, body := e.do(t, "GET", "/admin/api/repos", "", nil); !bytes.Contains(body, []byte(`"max_comments":9`)) {
		t.Errorf("review defaults should show in effective repo settings: %s", body)
	}
	if code, _ := e.do(t, "PUT", "/admin/api/settings", csrf, map[string]any{"review_defaults": map[string]any{"model": "nope"}}); code != 400 {
		t.Errorf("invalid defaults should be rejected: %d", code)
	}
	if code, _ := e.do(t, "PUT", "/admin/api/settings", "", map[string]any{}); code != http.StatusForbidden {
		t.Errorf("settings write without CSRF: %d", code)
	}

	for _, path := range []string{"/admin/settings", "/admin/rules", "/admin/webhooks"} {
		if code, body := e.do(t, "GET", path, "", nil); code != 200 || !bytes.Contains(body, []byte(`id="root"`)) {
			t.Errorf("%s should serve the admin page so reloads land there: %d", path, code)
		}
	}
	if code, _ := e.do(t, "GET", "/admin/nope", "", nil); code != 404 {
		t.Errorf("unknown section: %d", code)
	}
	if code, body := e.do(t, "GET", "/admin/assets/theme.js", "", nil); code != 200 || !bytes.Contains(body, []byte("crc-theme")) {
		t.Errorf("static assets: %d", code)
	}

	// Code hosts: secrets go in, never come out, and are kept on edit.
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" && r.Header.Get("PRIVATE-TOKEN") == "glpat-SECRET" {
			_, _ = io.WriteString(w, `{"username":"gitlab-bot"}`)
			return
		}
		w.WriteHeader(401)
	}))
	defer gl.Close()
	newHost := map[string]any{"platform": "gitlab", "base_url": gl.URL, "token": "glpat-SECRET",
		"signing_tokens": []string{randomSigningToken(t)}}
	code, body = e.do(t, "POST", "/admin/api/hosts", csrf, newHost)
	if code != 201 || bytes.Contains(body, []byte("glpat-SECRET")) || !bytes.Contains(body, []byte(`"token":true`)) || !bytes.Contains(body, []byte(`/webhooks/gitlab`)) {
		t.Fatalf("create host: %d %s", code, body)
	}
	var hv struct{ ID int64 }
	_ = json.Unmarshal(body, &hv)
	if code, _ := e.do(t, "POST", "/admin/api/hosts", csrf, newHost); code != http.StatusConflict {
		t.Errorf("duplicate host: %d", code)
	}
	if code, body := e.do(t, "GET", "/admin/api/hosts", "", nil); code != 200 || bytes.Contains(body, []byte("glpat-SECRET")) || bytes.Contains(body, []byte("whsec_")) {
		t.Errorf("list must not reveal secrets: %s", body)
	}
	if code, body := e.do(t, "PUT", fmt.Sprintf("/admin/api/hosts/%d", hv.ID), csrf, map[string]any{"name": "Our GitLab", "enabled": true}); code != 200 || !bytes.Contains(body, []byte(`"token":true`)) {
		t.Errorf("editing without a token must keep it: %d %s", code, body)
	}
	if code, body := e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/test", hv.ID), csrf, nil); code != 200 || !bytes.Contains(body, []byte(`"user":"gitlab-bot"`)) {
		t.Errorf("test host: %d %s", code, body)
	}
	// Webhooks: a UUID URL and a generated secret, returned once.
	code, body = e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/webhooks", hv.ID), csrf, map[string]any{"name": "group platform"})
	var created struct {
		Webhook struct {
			ID, URL, Name string
			Secrets       struct {
				WebhookSecret bool `json:"webhook_secret"`
			}
		}
		Secret string
	}
	_ = json.Unmarshal(body, &created)
	if code != 201 || len(created.Secret) != 64 || created.Webhook.Name != "group platform" || !created.Webhook.Secrets.WebhookSecret ||
		created.Webhook.URL != e.app.URL+"/webhooks/gitlab/"+created.Webhook.ID || len(created.Webhook.ID) != 36 {
		t.Fatalf("create webhook: %d %s", code, body)
	}
	if code, body := e.do(t, "GET", "/admin/api/hosts", "", nil); code != 200 || bytes.Contains(body, []byte(created.Secret)) || !bytes.Contains(body, []byte(created.Webhook.ID)) {
		t.Errorf("the host list shows the webhook but never its secret: %s", body)
	}
	if code, body := e.do(t, "PUT", "/admin/api/webhooks/"+created.Webhook.ID, csrf, map[string]any{"signing_tokens": []string{"nope"}}); code != 400 {
		t.Errorf("a malformed signing token must be rejected: %d %s", code, body)
	}
	code, body = e.do(t, "PUT", "/admin/api/webhooks/"+created.Webhook.ID, csrf, map[string]any{"regenerate_secret": true, "enabled": false})
	var regen struct{ Secret string }
	_ = json.Unmarshal(body, &regen)
	if code != 200 || len(regen.Secret) != 64 || regen.Secret == created.Secret || !bytes.Contains(body, []byte(`"enabled":false`)) {
		t.Errorf("regenerate: %d %s", code, body)
	}
	if code, body := e.do(t, "PUT", "/admin/api/webhooks/"+created.Webhook.ID, csrf, map[string]any{"name": "renamed"}); code != 200 || bytes.Contains(body, []byte(`"secret"`)) {
		t.Errorf("a rename returns no secret: %d %s", code, body)
	}
	if ep, ok := e.adm.Hosts.Endpoint(created.Webhook.ID); !ok || ep.Name != "renamed" || ep.WebhookSecrets[0] != regen.Secret {
		t.Errorf("stored webhook: %+v", ep)
	}
	if code, _ := e.do(t, "DELETE", "/admin/api/webhooks/"+created.Webhook.ID, csrf, nil); code != 200 {
		t.Errorf("delete webhook: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/admin/api/webhooks/"+created.Webhook.ID, csrf, nil); code != 404 {
		t.Errorf("deleted webhook: %d", code)
	}
	entries, _ := e.st.ListAudit(context.Background(), 50)
	var audited []string
	for _, a := range entries {
		if strings.HasPrefix(a.Action, "webhook.") {
			audited = append(audited, a.Action)
			if d, _ := json.Marshal(a.Detail); bytes.Contains(d, []byte(regen.Secret)) || bytes.Contains(d, []byte(created.Secret)) {
				t.Errorf("audit must not hold secrets: %s", d)
			}
		}
	}
	if len(audited) < 4 {
		t.Errorf("webhook changes must be audited: %v", audited)
	}
	if code, _ := e.do(t, "POST", "/admin/api/hosts", csrf, map[string]any{"platform": "gitlab", "base_url": "https://other.example", "webhook_secrets": []string{"x"}}); code != 400 {
		t.Errorf("a GitLab host without an access token must be rejected: %d", code)
	}
	if code, _ := e.do(t, "DELETE", fmt.Sprintf("/admin/api/hosts/%d", hv.ID), csrf, nil); code != 200 {
		t.Errorf("delete host: %d", code)
	}

	// Reviews: history with filters, details, review now, re-run, metrics.
	now := time.Now()
	_, _ = e.st.InsertJob(context.Background(), store.Job{Kind: "review", Platform: "gitlab", Host: "gitlab.com", Repo: "g/api", Number: 4,
		State: "done", Finished: now, Started: now.Add(-time.Minute), Comments: 2, Result: []byte(`{"summary":"Adds A.","findings":[]}`)})
	_, _ = e.st.InsertJob(context.Background(), store.Job{Kind: "review", Platform: "gitlab", Host: "gitlab.com", Repo: "g/web", Number: 5, State: "failed", Finished: now, Error: "boom"})
	if code, body := e.do(t, "GET", "/admin/api/reviews?state=failed&repo=web", "", nil); code != 200 || !bytes.Contains(body, []byte(`"error":"boom"`)) || bytes.Contains(body, []byte(`g/api`)) {
		t.Errorf("filtered reviews: %d %s", code, body)
	}
	var list struct{ Reviews []store.Job }
	_, body = e.do(t, "GET", "/admin/api/reviews?repo=g/api", "", nil)
	_ = json.Unmarshal(body, &list)
	if len(list.Reviews) != 1 || list.Reviews[0].Result != nil {
		t.Fatalf("the list must not carry results: %s", body)
	}
	id := list.Reviews[0].ID
	if code, body := e.do(t, "GET", fmt.Sprintf("/admin/api/reviews/%d", id), "", nil); code != 200 || !bytes.Contains(body, []byte("Adds A.")) {
		t.Errorf("review detail: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", fmt.Sprintf("/admin/api/reviews/%d/rerun", id), csrf, map[string]any{"full": true}); code != http.StatusServiceUnavailable {
		t.Errorf("without a reviewer, re-runs are unavailable: %d", code)
	}
	fr := &fakeReviewer{}
	e.adm.Reviewer = fr
	if code, _ := e.do(t, "POST", fmt.Sprintf("/admin/api/reviews/%d/rerun", id), csrf, map[string]any{"full": true}); code != http.StatusAccepted || len(fr.refs) != 1 || fr.refs[0].Repo != "g/api" {
		t.Errorf("re-run: %d %+v", code, fr.refs)
	}
	if code, _ := e.do(t, "POST", "/admin/api/reviews", csrf, map[string]any{"url": "https://gitlab.com/g/api/-/merge_requests/9"}); code != http.StatusAccepted || fr.refs[1].Number != 9 {
		t.Errorf("review by URL: %d %+v", code, fr.refs)
	}
	if code, _ := e.do(t, "POST", "/admin/api/reviews", csrf, map[string]any{"url": "not a url"}); code != 400 {
		t.Errorf("bad URL: %d", code)
	}
	if code, body := e.do(t, "POST", "/admin/api/reviews", csrf, map[string]any{"url": "https://unknown.example/g/p/-/merge_requests/1"}); code != 400 || !bytes.Contains(body, []byte("no enabled")) {
		t.Errorf("unknown host: %d %s", code, body)
	}
	if code, body := e.do(t, "GET", "/admin/api/metrics?granularity=day", "", nil); code != 200 || !bytes.Contains(body, []byte(`"jobs":2`)) {
		t.Errorf("metrics: %d %s", code, body)
	}

	// Learnings: add, widen the scope, disable, delete.
	code, body = e.do(t, "POST", "/admin/api/learnings", csrf, map[string]any{"platform": "gitlab", "host": "gitlab.example.com", "scope": "Acme/Pay/", "text": "Zeros are dropped on purpose."})
	if code != 201 || !bytes.Contains(body, []byte(`"scope":"acme/pay"`)) {
		t.Fatalf("create learning: %d %s", code, body)
	}
	var lv struct{ ID int64 }
	_ = json.Unmarshal(body, &lv)
	if code, body := e.do(t, "PUT", fmt.Sprintf("/admin/api/learnings/%d", lv.ID), csrf, map[string]any{"scope": "acme", "text": "Zeros are dropped on purpose.", "status": "disabled"}); code != 200 || !bytes.Contains(body, []byte(`"status":"disabled"`)) {
		t.Errorf("update learning: %d %s", code, body)
	}
	if code, _ := e.do(t, "POST", "/admin/api/learnings", csrf, map[string]any{"platform": "gitlab", "host": "gitlab.example.com", "text": ""}); code != 400 {
		t.Errorf("empty learning: %d", code)
	}
	if code, body := e.do(t, "GET", "/admin/api/learnings", "", nil); code != 200 || !bytes.Contains(body, []byte("Zeros are dropped")) {
		t.Errorf("list learnings: %d %s", code, body)
	}
	if code, _ := e.do(t, "DELETE", fmt.Sprintf("/admin/api/learnings/%d", lv.ID), csrf, nil); code != 200 {
		t.Errorf("delete learning: %d", code)
	}

	r, _ := e.st.FindRepo(context.Background(), "github", "github.com", "org/api")
	if r.Mode != store.ModeEnabled || *r.Settings.Model != "smart" || r.UpdatedBy != "Ana@Divmora.com" {
		t.Errorf("stored repo = %+v", r)
	}
	code, body = e.do(t, "GET", "/admin/api/audit", "", nil)
	if code != http.StatusOK {
		t.Fatalf("audit: %d %s", code, body)
	}
	for _, want := range []string{`"login"`, `"repo.update"`, `"repo.create"`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("audit missing %s: %s", want, body)
		}
	}

	if code, _ := e.do(t, "POST", "/admin/logout", csrf, nil); code != 200 {
		t.Errorf("logout: %d", code)
	}
	if code, _ := e.do(t, "GET", "/admin/api/repos", "", nil); code != http.StatusUnauthorized {
		t.Errorf("session must end at logout: %d", code)
	}
}

func TestLoginDenied(t *testing.T) {
	cases := []struct {
		name   string
		cfg    config.OIDC
		claims map[string]any
		want   int
	}{
		{"wrong domain", config.OIDC{AllowedDomains: []string{"divmora.com"}}, map[string]any{"sub": "x", "email": "eve@evil.com", "email_verified": true}, 403},
		{"unverified email", config.OIDC{AllowedDomains: []string{"divmora.com"}}, map[string]any{"sub": "x", "email": "eve@divmora.com", "email_verified": false}, 403},
		{"group grants access", config.OIDC{AllowedGroups: []string{"platform-admins"}}, map[string]any{"sub": "x", "email": "g@other.com", "groups": []string{"devs", "platform-admins"}}, 200},
		{"exact email", config.OIDC{AllowedEmails: []string{"boss@x.io"}}, map[string]any{"sub": "x", "email": "BOSS@x.io"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, c.cfg)
			e.idp.as(c.claims)
			resp := e.login(t)
			_ = resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("status %d, want %d", resp.StatusCode, c.want)
			}
			if c.want == 403 {
				entries, _ := e.st.ListAudit(context.Background(), 10)
				if len(entries) == 0 || entries[0].Action != "login.denied" {
					t.Errorf("denied login should be audited: %+v", entries)
				}
			}
		})
	}
}

func TestCallbackRejectsForgery(t *testing.T) {
	e := setup(t, config.OIDC{AllowedDomains: []string{"divmora.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "a@divmora.com"})

	// No flow cookie: a forged callback is refused.
	resp, _ := e.client.Get(e.app.URL + "/admin/callback?code=x&state=y")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback without flow cookie: %d", resp.StatusCode)
	}

	// Nonce replay/mismatch is refused.
	e.idp.tamperNonce = true
	resp = e.login(t)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad nonce: %d", resp.StatusCode)
	}

	// Cross-origin writes are refused even with a valid CSRF token.
	e.idp.tamperNonce = false
	resp = e.login(t)
	_ = resp.Body.Close()
	csrf := e.csrf(t)
	req, _ := http.NewRequest("POST", e.app.URL+"/admin/api/repos", strings.NewReader(`{"platform":"github","path":"a/b"}`))
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Origin", "https://evil.example")
	r2, _ := e.client.Do(req)
	_ = r2.Body.Close()
	if r2.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin write: %d", r2.StatusCode)
	}
}

func TestNewValidation(t *testing.T) {
	st := storetest.New()
	if _, err := New(context.Background(), config.Admin{BaseURL: "https://x", OIDC: config.OIDC{Issuer: "https://i", ClientID: "c"}}, st, quiet); err == nil || !strings.Contains(err.Error(), "allowed_") {
		t.Errorf("missing allow rules should fail: %v", err)
	}
	if _, err := New(context.Background(), config.Admin{OIDC: config.OIDC{Issuer: "https://i", ClientID: "c", AllowedEmails: []string{"a@b"}}}, st, quiet); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("missing base_url should fail: %v", err)
	}
	if _, err := New(context.Background(), config.Admin{BaseURL: "https://x", CookieSecret: "short", OIDC: config.OIDC{Issuer: "https://i", ClientID: "c", AllowedEmails: []string{"a@b"}}}, st, quiet); err == nil {
		t.Errorf("a short cookie_secret should fail (or discovery fails first)")
	}
}

// randomSigningToken returns a fresh GitLab webhook signing token
// ("whsec_" + base64 of 32 random bytes), so no fixed token is in the code.
func randomSigningToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

// The app page carries a fresh CSP nonce on every load, in both the CSP
// header (styles only) and the page's <meta name="csp-nonce">.
func TestPageNonce(t *testing.T) {
	e := setup(t, config.OIDC{AllowedDomains: []string{"divmora.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "ana@divmora.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	re := regexp.MustCompile(`<meta name="csp-nonce" content="([^"]+)">`)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		resp, err := e.client.Get(e.app.URL + "/admin/reviews")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		m := re.FindSubmatch(body)
		if m == nil || string(m[1]) == "__CSP_NONCE__" {
			t.Fatalf("page has no nonce: %s", body)
		}
		nonce := string(m[1])
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "style-src 'self' 'nonce-"+nonce+"'") || !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "unsafe") {
			t.Errorf("CSP %q does not match nonce %q", csp, nonce)
		}
		if seen[nonce] {
			t.Error("nonce reused across requests")
		}
		seen[nonce] = true
	}
}

func TestSetupChecklist(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	ctx := context.Background()
	type status struct {
		Steps []struct {
			ID     string
			Done   bool
			Detail string
		}
		Done, Total int
		Complete    bool
	}
	check := func() (status, map[string]bool) {
		t.Helper()
		code, body := e.do(t, "GET", "/admin/api/setup", "", nil)
		var st status
		if code != 200 || json.Unmarshal(body, &st) != nil {
			t.Fatalf("setup: %d %s", code, body)
		}
		done := map[string]bool{}
		for _, s := range st.Steps {
			done[s.ID] = s.Done
		}
		return st, done
	}
	// The test admin has two config profiles ("fast", "smart") and a license.
	st, done := check()
	if st.Complete || done["host"] || done["webhook"] || done["delivery"] || done["review"] || !done["model"] || !done["license"] || st.Total != 6 {
		t.Fatalf("fresh install: %+v", st)
	}
	if code, body := e.do(t, "GET", "/admin/api/me", "", nil); code != 200 || !bytes.Contains(body, []byte(`"setup":{"complete":false`)) {
		t.Errorf("/me carries setup progress: %s", body)
	}

	h, err := e.adm.Hosts.Save(ctx, hosts.Host{Platform: hosts.GitLab, BaseURL: "https://gitlab.example.com", Enabled: true, Secrets: hosts.Secrets{Token: "t"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	if _, done = check(); !done["host"] || done["webhook"] {
		t.Errorf("a host without webhooks: %v", done)
	}
	if _, _, err := e.adm.Hosts.AddEndpoint(ctx, h.ID, "group", "a@x"); err != nil {
		t.Fatal(err)
	}
	// A rejected delivery explains itself; a verified one completes the step.
	_ = e.st.InsertWebhookEvents(ctx, []store.WebhookEvent{{Platform: "gitlab", Auth: "failed", Reason: "invalid token", Outcome: store.HookRejected}})
	st, done = check()
	if !done["webhook"] || done["delivery"] || !strings.Contains(fmt.Sprint(st.Steps), "invalid token") {
		t.Errorf("rejected delivery: %+v", st)
	}
	_ = e.st.InsertWebhookEvents(ctx, []store.WebhookEvent{{Platform: "gitlab", Project: "g/app", Auth: "token", Outcome: store.HookIgnored}})
	if _, done = check(); !done["delivery"] || !done["repo"] {
		t.Errorf("verified delivery; with the allowlist off every repository counts: %v", done)
	}
	if code, body := e.do(t, "PUT", "/admin/api/settings", e.csrf(t), map[string]any{"allowlist": true}); code != 200 {
		t.Fatalf("allowlist on: %d %s", code, body)
	}
	if _, done = check(); done["repo"] {
		t.Errorf("allowlist on and no repository enabled: %v", done)
	}
	if _, err := e.st.CreateRepo(ctx, "gitlab", "gitlab.example.com", "g/app", store.ModeEnabled, "a@x"); err != nil {
		t.Fatal(err)
	}
	_, _ = e.st.InsertJob(ctx, store.Job{Kind: "review", Platform: "gitlab", Host: "gitlab.example.com", Repo: "g/app", Number: 1, State: "done", Finished: time.Now()})
	if st, done = check(); !st.Complete || st.Done != 6 || !done["repo"] || !done["review"] {
		t.Errorf("complete: %+v", st)
	}
	if code, _ := e.do(t, "GET", "/admin/setup", "", nil); code != 200 {
		t.Errorf("reloading /admin/setup must serve the app: %d", code)
	}
}

func TestCommentEventsWarning(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	ctx := context.Background()
	gl, err := e.adm.Hosts.Save(ctx, hosts.Host{Platform: hosts.GitLab, BaseURL: "https://gitlab.example.com", Enabled: true,
		Settings: hosts.Settings{BotName: "review-bot"}, Secrets: hosts.Secrets{Token: "t"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	hostWarning := func() string {
		t.Helper()
		_, body := e.do(t, "GET", "/admin/api/hosts", "", nil)
		var list struct {
			Hosts []struct {
				ID             int64
				CommentWarning string `json:"comment_warning"`
			}
		}
		_ = json.Unmarshal(body, &list)
		for _, h := range list.Hosts {
			if h.ID == gl.ID {
				return h.CommentWarning
			}
		}
		t.Fatalf("host missing: %s", body)
		return ""
	}
	if w := hostWarning(); w != "" {
		t.Errorf("no deliveries yet: no warning, got %q", w)
	}
	// Only system hook deliveries (merge requests): the issue #25 setup.
	_ = e.st.InsertWebhookEvents(ctx, []store.WebhookEvent{
		{Platform: "gitlab", Host: "gitlab.example.com", Event: "System Hook", Kind: "merge_request", Auth: "signature", Outcome: store.HookQueued},
		{Platform: "gitlab", Host: "gitlab.example.com", Event: "Note Hook", Kind: "note", Auth: "failed", Outcome: store.HookRejected}, // unverified: doesn't count
	})
	if w := hostWarning(); !strings.Contains(w, "system hooks don't send comments") || !strings.Contains(w, "@review-bot") {
		t.Errorf("system hook only: %q", w)
	}
	code, body := e.do(t, "GET", "/admin/api/setup", "", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"id":"comments","title":"Receive comments"`)) || !bytes.Contains(body, []byte("system hooks")) {
		t.Errorf("setup comments step: %s", body)
	}
	// A verified comment clears it.
	_ = e.st.InsertWebhookEvents(ctx, []store.WebhookEvent{{Platform: "gitlab", Host: "gitlab.example.com", Event: "Note Hook", Kind: "note", Auth: "token", Outcome: store.HookIgnored}})
	if w := hostWarning(); w != "" {
		t.Errorf("after a comment event: %q", w)
	}
	if _, body := e.do(t, "GET", "/admin/api/setup", "", nil); !bytes.Contains(body, []byte("1 comment event received")) {
		t.Errorf("setup after a comment: %s", body)
	}
}

func TestCommentWarningText(t *testing.T) {
	gh := hosts.Host{Platform: hosts.GitHub}
	if w := commentWarning(gh, store.HostHookStats{Verified: 3}); !strings.Contains(w, "Issue comment") || !strings.Contains(w, "@bot") {
		t.Errorf("github: %q", w)
	}
	if w := commentWarning(hosts.Host{Platform: hosts.Bitbucket}, store.HostHookStats{Verified: 1}); !strings.Contains(w, "Comment created") {
		t.Errorf("bitbucket: %q", w)
	}
	if w := commentWarning(hosts.Host{Platform: hosts.GitLab}, store.HostHookStats{Verified: 2, SystemHook: 1}); !strings.Contains(w, "Turn on Comments") {
		t.Errorf("gitlab with a group hook but no comments: %q", w)
	}
}

func TestReviewsPausedWithoutExecutorLicense(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	e.adm.ExecutorType = "kubernetes"
	e.adm.Lic = licensetest.Manager(t, license.Hosts) // no executors feature
	if _, body := e.do(t, "GET", "/admin/api/me", "", nil); !bytes.Contains(body, []byte(`"reviews_paused":"reviews are paused: the kubernetes executor needs a license`)) {
		t.Errorf("/me must say reviews are paused: %s", body)
	}
	_, body := e.do(t, "GET", "/admin/api/setup", "", nil)
	var st struct {
		Steps []struct {
			ID       string
			Done     bool
			Optional bool
		}
		Total int
	}
	_ = json.Unmarshal(body, &st)
	if st.Steps[0].ID != "license" || st.Steps[0].Done || st.Steps[0].Optional || st.Total != 7 {
		t.Errorf("the license step is required while reviews are paused: %s", body)
	}
	e.adm.Lic = licensetest.Full(t)
	if _, body := e.do(t, "GET", "/admin/api/me", "", nil); !bytes.Contains(body, []byte(`"reviews_paused":""`)) {
		t.Errorf("licensed: %s", body)
	}
	e.adm.ExecutorType, e.adm.Lic = "local", nil
	if _, body := e.do(t, "GET", "/admin/api/me", "", nil); !bytes.Contains(body, []byte(`"reviews_paused":""`)) {
		t.Errorf("the local executor never needs a license: %s", body)
	}
}

func TestModelsAPI(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	t.Setenv("TEAM_CLAUDE_KEY", "sk-ant")

	if code, body := e.do(t, "PUT", "/admin/api/models/claude", csrf, map[string]any{"config": map[string]any{"provider": "anthropic", "model": "claude-sonnet-5-5", "api_key": "sk-ant"}}); code != 400 || !bytes.Contains(body, []byte("not stored")) {
		t.Errorf("literal keys are refused: %d %s", code, body)
	}
	if code, _ := e.do(t, "PUT", "/admin/api/models/claude", "", map[string]any{"config": map[string]any{}}); code != 403 {
		t.Errorf("writes need the CSRF token: %d", code)
	}
	code, body := e.do(t, "PUT", "/admin/api/models/claude", csrf, map[string]any{"config": map[string]any{"provider": "anthropic", "model": "claude-sonnet-5-5", "api_key_env": "TEAM_CLAUDE_KEY"}})
	if code != 200 || !bytes.Contains(body, []byte(`"name":"claude"`)) || !bytes.Contains(body, []byte(`"key_set":true`)) {
		t.Fatalf("put: %d %s", code, body)
	}
	if code, body := e.do(t, "PUT", "/admin/api/model-choice", csrf, map[string]any{"default": "claude", "fallback": []string{"fast"}}); code != 200 || !bytes.Contains(body, []byte(`"choice_source":"console"`)) {
		t.Fatalf("choice: %d %s", code, body)
	}
	if code, _ := e.do(t, "DELETE", "/admin/api/models/claude", csrf, nil); code != 409 {
		t.Errorf("the default can't be deleted: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/admin/api/models/fast", csrf, nil); code != 409 {
		t.Errorf("a fallback can't be deleted: %d", code)
	}
	// The repository form offers console profiles too.
	if code, body := e.do(t, "GET", "/admin/api/me", "", nil); code != 200 || !bytes.Contains(body, []byte(`"profiles":["claude","fast","smart"]`)) {
		t.Errorf("/me profiles: %s", body)
	}
	repo, _ := e.st.CreateRepo(context.Background(), "gitlab", "g.example", "team/app", "enabled", "a@x")
	if code, body := e.do(t, "PUT", "/admin/api/repos/"+strconv.FormatInt(repo.ID, 10), csrf, map[string]any{"mode": "enabled", "settings": map[string]any{"model": "claude"}}); code != 200 {
		t.Errorf("a repository can use a console profile: %d %s", code, body)
	}
}

func TestExecutorAPI(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	e.adm.Executor = execsettings.New(config.Executor{Type: "kubernetes", Kubernetes: config.KubeExecutor{
		Image: "ghcr.io/divmora/code-review-bot:0.5.0", Namespace: "reviews", ServiceAccount: "crb", TTL: 10 * time.Minute}}, e.st)
	// No network in tests: the registry can't be reached, so the page falls
	// back to typing the version.
	e.adm.Tags = execsettings.NewTags()
	e.adm.Tags.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}

	code, body := e.do(t, "GET", "/admin/api/executor", "", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"source":"config"`)) || !bytes.Contains(body, []byte(`"image_repo":"ghcr.io/divmora/code-review-bot"`)) || !bytes.Contains(body, []byte(`"namespace":"reviews"`)) ||
		!bytes.Contains(body, []byte(`"tags":[]`)) || !bytes.Contains(body, []byte(`"tags_error":"registry: `)) {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, body := e.do(t, "PUT", "/admin/api/executor", csrf, map[string]any{"version": "evil.example.com/x:1"}); code != 400 {
		t.Errorf("the panel picks a tag, not an image: %d %s", code, body)
	}
	if code, _ := e.do(t, "PUT", "/admin/api/executor", csrf, map[string]any{"service_account": "cluster-admin", "version": "0.6.0"}); code != 400 {
		t.Errorf("connection settings aren't part of the panel's settings: %d", code)
	}
	code, body = e.do(t, "PUT", "/admin/api/executor", csrf, map[string]any{"version": "0.6.0", "cpu": "1", "node_selector": map[string]string{"pool": "batch"}})
	if code != 200 || !bytes.Contains(body, []byte(`"image":"ghcr.io/divmora/code-review-bot:0.6.0"`)) || !bytes.Contains(body, []byte(`"source":"console"`)) {
		t.Fatalf("put: %d %s", code, body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestSessionRecheckedAgainstRules: a session ends as soon as its identity
// no longer matches the sign-in rules or the license (issue #35).
func TestSessionRecheckedAgainstRules(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}, AllowedGroups: []string{"admins"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	if code, _ := e.do(t, "GET", "/admin/api/me", "", nil); code != 200 {
		t.Fatalf("signed in: %d", code)
	}
	// Alice is removed from allowed_emails (config change + restart).
	e.adm.cfg.OIDC.AllowedEmails = []string{"bob@example.com"}
	if code, _ := e.do(t, "GET", "/admin/api/me", "", nil); code != 401 {
		t.Errorf("a removed admin's session must end at once: %d", code)
	}
	if code, _ := e.do(t, "GET", "/admin/api/me", "", nil); code != 401 {
		t.Error("and stay ended")
	}
	entries, _ := e.st.ListAudit(context.Background(), 10)
	if len(entries) == 0 || entries[0].Action != "session.ended" || entries[0].Actor != "alice@example.com" {
		t.Errorf("audit: %+v", entries)
	}

	// Group membership is stored with the session; the license with
	// sso_rules ending ends group-based sessions.
	e2 := setup(t, config.OIDC{AllowedGroups: []string{"admins"}})
	e2.idp.as(map[string]any{"sub": "u2", "email": "gina@example.com", "email_verified": true, "groups": []any{"admins"}})
	_ = e2.login(t).Body.Close()
	if code, _ := e2.do(t, "GET", "/admin/api/me", "", nil); code != 200 {
		t.Fatalf("group sign-in: %d", code)
	}
	e2.adm.Lic = nil // the free core: group rules need sso_rules
	if code, _ := e2.do(t, "GET", "/admin/api/me", "", nil); code != 401 {
		t.Errorf("a group session must end without sso_rules: %d", code)
	}
}

func TestSessionsAPI(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com", "bob@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)

	// Bob signs in from another browser.
	jar, _ := cookiejar.New(nil)
	bob := &env{idp: e.idp, app: e.app, st: e.st, adm: e.adm, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	e.idp.as(map[string]any{"sub": "u2", "email": "bob@example.com", "email_verified": true})
	_ = bob.login(t).Body.Close()

	var list []struct {
		ID      int64  `json:"id"`
		Email   string `json:"email"`
		Current bool   `json:"current"`
	}
	code, body := e.do(t, "GET", "/admin/api/sessions", "", nil)
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	var bobID int64
	for _, s := range list {
		if s.Email == "bob@example.com" {
			bobID = s.ID
			if s.Current {
				t.Error("bob's session is not alice's current one")
			}
		} else if !s.Current {
			t.Error("alice's own session is marked current")
		}
	}
	if code, _ := e.do(t, "DELETE", "/admin/api/sessions/"+strconv.FormatInt(bobID, 10), "", nil); code != 403 {
		t.Errorf("revoking needs the CSRF token: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/admin/api/sessions/"+strconv.FormatInt(bobID, 10), csrf, nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := bob.do(t, "GET", "/admin/api/me", "", nil); code != 401 {
		t.Errorf("bob is signed out: %d", code)
	}

	// Bob signs in again; "sign out everyone else" keeps only alice.
	_ = bob.login(t).Body.Close()
	if code, body := e.do(t, "POST", "/admin/api/sessions/end-others", csrf, nil); code != 200 || json.Unmarshal(body, &list) != nil || len(list) != 1 || !list[0].Current {
		t.Fatalf("end others: %d %s", code, body)
	}
	if code, _ := bob.do(t, "GET", "/admin/api/me", "", nil); code != 401 {
		t.Errorf("bob is signed out again: %d", code)
	}
	if code, _ := e.do(t, "GET", "/admin/api/me", "", nil); code != 200 {
		t.Errorf("alice stays signed in: %d", code)
	}
	entries, _ := e.st.ListAudit(context.Background(), 5)
	if len(entries) < 2 || entries[0].Action != "session.revoke_others" || entries[0].Actor != "alice@example.com" {
		t.Errorf("audit: %+v", entries)
	}
}

// TestAdminURLsPeopleReach: /admin/ and trailing slashes redirect, an
// unknown section shows the panel's "not found" view (404), and a GET
// /admin/logout shows the panel (its Sign out button POSTs) (issue #60).
func TestAdminURLsPeopleReach(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	noFollow := *e.client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) *http.Response {
		resp, err := noFollow.Get(e.app.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	for path, want := range map[string]string{"/admin/": "/admin", "/admin/?x=1": "/admin?x=1", "/admin/settings/": "/admin/settings"} {
		if r := get(path); r.StatusCode != http.StatusPermanentRedirect || r.Header.Get("Location") != want {
			t.Errorf("%s: %d %q", path, r.StatusCode, r.Header.Get("Location"))
		}
	}
	if r := get("/admin/review"); r.StatusCode != http.StatusNotFound || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Errorf("unknown section: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if r := get("/admin/logout"); r.StatusCode != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET /admin/logout: %d", r.StatusCode)
	}
	if code, _ := e.do(t, "GET", "/admin/api/me", "", nil); code != 200 {
		t.Error("GET /admin/logout must not sign out")
	}
	// Assets still load.
	if r := get("/admin/assets/theme.js"); r.StatusCode != http.StatusOK {
		t.Errorf("assets: %d", r.StatusCode)
	}
}

func TestTuningAPI(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	t.Setenv("MY_PROXY_CA", "pem")

	code, body := e.do(t, "GET", "/admin/api/tuning", "", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"workers":4`)) || !bytes.Contains(body, []byte(`"dashboard_token_set":false`)) {
		t.Fatalf("get: %d %s", code, body)
	}
	put := map[string]any{"workers": 6, "queue_size": 50, "job_timeout": "30m", "debounce": "10s", "history": 100,
		"bot_name": "@acme-bot", "extra_env": []string{"MY_PROXY_CA"}, "engine": map[string]any{"passes": 2, "verify_model": "fast"},
		"dashboard_token": "a-dashboard-token-of-length"}
	if code, _ := e.do(t, "PUT", "/admin/api/tuning", "", put); code != 403 {
		t.Errorf("writes need the CSRF token: %d", code)
	}
	code, body = e.do(t, "PUT", "/admin/api/tuning", csrf, put)
	if code != 200 || !bytes.Contains(body, []byte(`"workers":6`)) || !bytes.Contains(body, []byte(`"bot_name":"acme-bot"`)) ||
		!bytes.Contains(body, []byte(`"dashboard_token_set":true`)) || !bytes.Contains(body, []byte(`"MY_PROXY_CA":true`)) {
		t.Fatalf("put: %d %s", code, body)
	}
	if bytes.Contains(body, []byte("a-dashboard-token")) || bytes.Contains(body, []byte("dashboard_token_hash")) {
		t.Errorf("the token (or its hash) is never sent back: %s", body)
	}
	put["extra_env"] = []string{"DATABASE_URL"}
	if code, body := e.do(t, "PUT", "/admin/api/tuning", csrf, put); code != 400 || !bytes.Contains(body, []byte("console's own")) {
		t.Errorf("the console's secrets can't be passed to jobs: %d %s", code, body)
	}
	if !e.adm.tuning().Get(context.Background()).DashboardTokenOK("a-dashboard-token-of-length") {
		t.Error("token saved")
	}
}

func TestSignedInForServerPages(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	page, _ := http.NewRequest("GET", e.app.URL+"/admin/dashboard", nil)
	if e.adm.SignedIn(page) {
		t.Fatal("no cookie: not signed in")
	}
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	u, _ := url.Parse(e.app.URL + "/admin/dashboard")
	for _, c := range e.client.Jar.Cookies(u) {
		page.AddCookie(c)
	}
	if !e.adm.SignedIn(page) {
		t.Fatal("the admin session cookie reaches /admin/dashboard")
	}
	if got := safeNext("/admin/dashboard"); got != "/admin/dashboard" {
		t.Errorf("sign-in returns to the dashboard: %s", got)
	}
	if got := safeNext("/dashboard"); got != "/admin" {
		t.Errorf("only /admin pages are return targets: %s", got)
	}
}

func TestTokenExpiryWarnings(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	soon := time.Now().UTC().Add(5 * 24 * time.Hour).Format("2006-01-02")
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-soon" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v4/user":
			_, _ = io.WriteString(w, `{"username":"gitlab-bot"}`)
		case "/api/v4/personal_access_tokens/self":
			_, _ = io.WriteString(w, `{"name":"bot","expires_at":"`+soon+`","scopes":["api"]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer gl.Close()
	code, body := e.do(t, "POST", "/admin/api/hosts", csrf, map[string]any{"platform": "gitlab", "name": "Our GitLab", "base_url": gl.URL, "token": "glpat-soon",
		"signing_tokens": []string{randomSigningToken(t)}})
	if code != 201 || !bytes.Contains(body, []byte(`"token_expiry":{"expires":"`)) {
		t.Fatalf("saving a host reads its token's expiry: %d %s", code, body)
	}
	var hv struct{ ID int64 }
	_ = json.Unmarshal(body, &hv)
	_, body = e.do(t, "GET", "/admin/api/me", "", nil)
	if !bytes.Contains(body, []byte(`{"kind":"host","host_id":`)) || !bytes.Contains(body, []byte(`Our GitLab expires on`)) || !bytes.Contains(body, []byte(`"days":6`)) {
		t.Errorf("the banner warns about the token: %s", body)
	}
	_, body = e.do(t, "GET", "/admin/api/setup", "", nil)
	if !bytes.Contains(body, []byte(`"warning":"The token of Our GitLab expires on`)) {
		t.Errorf("Setup's host step warns: %s", body)
	}
	_, body = e.do(t, "POST", fmt.Sprintf("/admin/api/hosts/%d/test", hv.ID), csrf, nil)
	if !bytes.Contains(body, []byte("the token expires on")) || !bytes.Contains(body, []byte("replace it soon")) {
		t.Errorf("Test reports the expiry: %s", body)
	}
}

func TestSignInsRecordWhereFrom(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	_, body := e.do(t, "GET", "/admin/api/sessions", "", nil)
	if !bytes.Contains(body, []byte(`"ip":"127.0.0.1"`)) || !bytes.Contains(body, []byte(`"user_agent":"Go-http-client`)) || !bytes.Contains(body, []byte(`"last_seen":"`)) {
		t.Errorf("sessions show where they signed in from: %s", body)
	}
	entries, _ := e.st.ListAudit(context.Background(), 20)
	var login *store.AuditEntry
	for i := range entries {
		if entries[i].Action == "login" {
			login = &entries[i]
		}
	}
	if login == nil || login.IP != "127.0.0.1" || !strings.HasPrefix(login.UserAgent, "Go-http-client") {
		t.Errorf("the sign-in audit entry has the IP and browser: %+v", login)
	}
	e.idp.as(map[string]any{"sub": "u2", "email": "mallory@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	entries, _ = e.st.ListAudit(context.Background(), 20)
	if entries[0].Action != "login.denied" || entries[0].IP != "127.0.0.1" {
		t.Errorf("denied sign-ins too: %+v", entries[0])
	}
}

func TestAuditSearchAndExport(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.adm.Lic = licensetest.Full(t)
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		_ = e.st.Audit(ctx, "bob@example.com", "rule.update", fmt.Sprintf("gitlab:g/team-%d", i), map[string]any{"n": i})
	}
	_ = e.st.Audit(ctx, "eve@example.com", "host.put", "gitlab:g", map[string]any{"after": map[string]any{"name": "=HYPERLINK(1)"}})
	var page struct {
		Entries []store.AuditEntry
		Next    int64
	}
	_, body := e.do(t, "GET", "/admin/api/audit?action=rule.*&limit=5", "", nil)
	_ = json.Unmarshal(body, &page)
	if len(page.Entries) != 5 || page.Next == 0 || page.Entries[0].Target != "gitlab:g/team-6" {
		t.Fatalf("first page: %s", body)
	}
	_, body = e.do(t, "GET", fmt.Sprintf("/admin/api/audit?action=rule.*&limit=5&before=%d", page.Next), "", nil)
	_ = json.Unmarshal(body, &page)
	if len(page.Entries) != 2 || page.Next != 0 {
		t.Errorf("second page: %s", body)
	}
	_, body = e.do(t, "GET", "/admin/api/audit?actor=EVE&q=hyperlink", "", nil)
	_ = json.Unmarshal(body, &page)
	if len(page.Entries) != 1 || page.Entries[0].Action != "host.put" {
		t.Errorf("actor and search: %s", body)
	}

	code, csvBody := e.do(t, "GET", "/admin/api/audit/export?format=csv&actor=eve", "", nil)
	if code != 200 || !bytes.HasPrefix(csvBody, []byte("id,at,actor,action,target,ip,user_agent,detail\n")) || !bytes.Contains(csvBody, []byte("eve@example.com")) || bytes.Contains(csvBody, []byte("bob@")) {
		t.Errorf("csv: %d %s", code, csvBody)
	}
	code, jl := e.do(t, "GET", "/admin/api/audit/export?format=jsonl&action=rule.update", "", nil)
	if lines := bytes.Count(jl, []byte("\n")); code != 200 || lines != 7 {
		t.Errorf("jsonl: %d lines %d", code, lines)
	}
	if code, _ := e.do(t, "GET", "/admin/api/audit/export?format=xml", "", nil); code != 400 {
		t.Errorf("unknown format: %d", code)
	}
	entries, _ := e.st.SearchAudit(ctx, store.AuditFilter{Action: "audit.export"})
	if len(entries) != 2 {
		t.Errorf("exports are audited: %d", len(entries))
	}
	if got := csvSafe("=HYPERLINK(1)"); got != "'=HYPERLINK(1)" {
		t.Errorf("formula guard: %s", got)
	}
}

func TestHostSaveNamesChangedSecrets(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	csrf := e.csrf(t)
	code, body := e.do(t, "POST", "/admin/api/hosts", csrf, map[string]any{"platform": "gitlab", "base_url": "https://gitlab.audit.example", "token": "glpat-first",
		"signing_tokens": []string{randomSigningToken(t)}})
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var hv struct{ ID int64 }
	_ = json.Unmarshal(body, &hv)
	if code, body := e.do(t, "PUT", fmt.Sprintf("/admin/api/hosts/%d", hv.ID), csrf, map[string]any{"token": "glpat-second", "enabled": true}); code != 200 {
		t.Fatalf("update: %d %s", code, body)
	}
	puts, _ := e.st.SearchAudit(context.Background(), store.AuditFilter{Action: "host.put"})
	if len(puts) != 2 {
		t.Fatalf("host.put entries: %d", len(puts))
	}
	if !bytes.Contains(puts[0].Detail, []byte(`"secrets_changed":["token:changed"]`)) || !bytes.Contains(puts[1].Detail, []byte(`"token:set","signing_tokens:+1"`)) {
		t.Errorf("secret changes are named: %s / %s", puts[0].Detail, puts[1].Detail)
	}
	for _, p := range puts {
		if bytes.Contains(p.Detail, []byte("glpat-")) || bytes.Contains(p.Detail, []byte("whsec_")) {
			t.Errorf("never the values: %s", p.Detail)
		}
	}
}
