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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
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
	a, err := New(context.Background(), config.Admin{Enabled: true, BaseURL: app.URL, OIDC: oidcCfg, SessionTTL: time.Hour}, st, []string{"fast", "smart"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	box, _ := secrets.New("a test passphrase that is long enough for a key")
	a.Hosts = hosts.New(&config.Config{}, st, box, quiet)
	if _, err := a.Hosts.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Register(mux)
	app.Config.Handler = mux
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
		"signing_tokens": []string{"whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}}
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
	if code, _ := e.do(t, "POST", "/admin/api/hosts", csrf, map[string]any{"platform": "gitlab", "base_url": "https://other.example", "token": "x"}); code != 400 {
		t.Errorf("a GitLab host without webhook credentials must be rejected: %d", code)
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
	if _, err := New(context.Background(), config.Admin{BaseURL: "https://x", OIDC: config.OIDC{Issuer: "https://i", ClientID: "c"}}, st, nil, quiet); err == nil || !strings.Contains(err.Error(), "allowed_") {
		t.Errorf("missing allow rules should fail: %v", err)
	}
	if _, err := New(context.Background(), config.Admin{OIDC: config.OIDC{Issuer: "https://i", ClientID: "c", AllowedEmails: []string{"a@b"}}}, st, nil, quiet); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("missing base_url should fail: %v", err)
	}
	if _, err := New(context.Background(), config.Admin{BaseURL: "https://x", CookieSecret: "short", OIDC: config.OIDC{Issuer: "https://i", ClientID: "c", AllowedEmails: []string{"a@b"}}}, st, nil, quiet); err == nil {
		t.Errorf("a short cookie_secret should fail (or discovery fails first)")
	}
}
