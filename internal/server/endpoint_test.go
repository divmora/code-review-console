package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// TestWebhookEndpoints covers per-webhook URLs: the URL picks the host and
// only that webhook's secret verifies it; the shared URL keeps using the
// host's secrets and never accepts an endpoint's.
func TestWebhookEndpoints(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub = config.GitHub{APIURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "gh-host-secret"}
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", SigningToken: testSigningToken}}
	cfg.Bitbucket = []config.Bitbucket{{APIURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "bb-host-secret"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	st := storetest.New()
	box, err := secrets.New("a test passphrase that is long enough for a key")
	if err != nil {
		t.Fatal(err)
	}
	reg := hosts.New(cfg, st, box, quiet)
	reg.Lic = licensetest.Full(t)
	if _, err := reg.Init(ctx); err != nil {
		t.Fatal(err)
	}
	hostID := map[string]int64{}
	for _, h := range reg.All() {
		hostID[h.Platform] = h.ID
	}
	add := func(platform string) (hosts.Endpoint, string) {
		e, secret, err := reg.AddEndpoint(ctx, hostID[platform], platform+" hook", "a@x")
		if err != nil {
			t.Fatal(err)
		}
		return e, secret
	}
	ghEP, ghSecret := add("github")
	glEP, glSecret := add("gitlab")
	bbEP, bbSecret := add("bitbucket")

	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, Hosts: reg, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(ctx)
	h := s.Handler()
	send := func(path string, body []byte, hdr map[string]string) int {
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}

	// GitHub.
	gh := func(n string) []byte {
		return []byte(`{"action":"opened","number":` + n + `,"repository":{"full_name":"o/r","html_url":"https://github.com/o/r"},"pull_request":{"number":` + n + `}}`)
	}
	ghSend := func(path string, body []byte, secret string) int {
		return send(path, body, map[string]string{"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": sign(secret, body)})
	}
	if c := ghSend(ghEP.Path("github"), gh("1"), ghSecret); c != http.StatusAccepted {
		t.Errorf("github endpoint with its secret: %d", c)
	}
	if c := ghSend(ghEP.Path("github"), gh("2"), "gh-host-secret"); c != http.StatusUnauthorized {
		t.Errorf("the host's secret must not verify an endpoint URL: %d", c)
	}
	if c := ghSend("/webhooks/github", gh("3"), ghSecret); c != http.StatusUnauthorized {
		t.Errorf("an endpoint's secret must not verify the shared URL: %d", c)
	}
	if c := ghSend("/webhooks/github", gh("4"), "gh-host-secret"); c != http.StatusAccepted {
		t.Errorf("the shared URL keeps working with the host's secret: %d", c)
	}

	// GitLab: the URL decides the host, whatever X-Gitlab-Instance says.
	gl := func(iid string) []byte {
		return []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"grp/app","web_url":"https://elsewhere.example/grp/app"},"object_attributes":{"iid":` + iid + `,"action":"open"}}`)
	}
	glHdr := func(token string) map[string]string {
		return map[string]string{"X-Gitlab-Event": "Merge Request Hook", "X-Gitlab-Instance": "https://unknown.example", "X-Gitlab-Token": token}
	}
	if c := send(glEP.Path("gitlab"), gl("1"), glHdr(glSecret)); c != http.StatusAccepted {
		t.Errorf("gitlab endpoint with its secret token: %d", c)
	}
	if c := send(glEP.Path("gitlab"), gl("2"), glHdr("wrong")); c != http.StatusUnauthorized {
		t.Errorf("gitlab endpoint with a wrong token: %d", c)
	}
	// The host's signing token is not the endpoint's.
	signed := func(path string, body []byte, id string) int {
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
		for k, v := range signedHeaders(testSigningKey, id, time.Now().Unix(), body) {
			req.Header[k] = v
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if c := signed(glEP.Path("gitlab"), gl("3"), "sig-1"); c != http.StatusUnauthorized {
		t.Errorf("the host's signing token must not verify an endpoint URL: %d", c)
	}
	// After the admin pastes the signing token on the endpoint, it verifies.
	glEP.SigningTokens = []string{testSigningToken}
	if _, err := reg.SaveEndpoint(ctx, glEP, "a@x"); err != nil {
		t.Fatal(err)
	}
	if c := signed(glEP.Path("gitlab"), gl("4"), "sig-2"); c != http.StatusAccepted {
		t.Errorf("gitlab endpoint with its signing token: %d", c)
	}
	// With a signing token, the old secret token alone no longer gets in
	// (issue #86)...
	if c := send(glEP.Path("gitlab"), gl("6"), glHdr(glSecret)); c != http.StatusUnauthorized {
		t.Errorf("signing token set: an unsigned delivery with the secret token must be rejected: %d", c)
	}
	// ...unless the webhook is set to accept both while hooks migrate.
	glEP.AcceptSecretToken = true
	if _, err := reg.SaveEndpoint(ctx, glEP, "a@x"); err != nil {
		t.Fatal(err)
	}
	if c := send(glEP.Path("gitlab"), gl("7"), glHdr(glSecret)); c != http.StatusAccepted {
		t.Errorf("accept_secret_token: the secret token works again: %d", c)
	}
	glEP.AcceptSecretToken = false
	if _, err := reg.SaveEndpoint(ctx, glEP, "a@x"); err != nil {
		t.Fatal(err)
	}

	// Bitbucket.
	bb := []byte(`{"actor":{"nickname":"dev"},"repository":{"full_name":"acme/web","links":{"html":{"href":"https://bitbucket.org/acme/web"}},"mainbranch":{"name":"main"}},` +
		`"pullrequest":{"id":1,"state":"OPEN","source":{"branch":{"name":"feat"}},"destination":{"branch":{"name":"main"}},"reviewers":[]}}`)
	if c := send(bbEP.Path("bitbucket"), bb, map[string]string{"X-Event-Key": "pullrequest:created", "X-Hub-Signature": bbSign(bbSecret, bb)}); c != http.StatusAccepted {
		t.Errorf("bitbucket endpoint with its secret: %d", c)
	}

	// Unusable URLs: unknown, another platform's, disabled, deleted.
	if c := ghSend("/webhooks/github/00000000-0000-4000-8000-000000000000", gh("5"), ghSecret); c != http.StatusNotFound {
		t.Errorf("unknown endpoint: %d", c)
	}
	if c := send("/webhooks/gitlab/"+ghEP.ID, gl("5"), glHdr(ghSecret)); c != http.StatusNotFound {
		t.Errorf("a GitHub endpoint on the GitLab URL: %d", c)
	}
	ghEP.Enabled = false
	if _, err := reg.SaveEndpoint(ctx, ghEP, "a@x"); err != nil {
		t.Fatal(err)
	}
	if c := ghSend(ghEP.Path("github"), gh("6"), ghSecret); c != http.StatusNotFound {
		t.Errorf("disabled endpoint: %d", c)
	}
	if err := reg.DeleteEndpoint(ctx, bbEP.ID, "a@x"); err != nil {
		t.Fatal(err)
	}
	if c := send(bbEP.Path("bitbucket"), bb, map[string]string{"X-Event-Key": "pullrequest:created", "X-Hub-Signature": bbSign(bbSecret, bb)}); c != http.StatusNotFound {
		t.Errorf("deleted endpoint: %d", c)
	}

	// The history records which URL each delivery used.
	s.Close()
	events, _ := st.ListWebhookEvents(ctx, store.WebhookFilter{Limit: 100})
	var viaEndpoint, viaShared, unknown int
	for _, e := range events {
		switch e.Endpoint {
		case ghEP.ID, glEP.ID, bbEP.ID:
			viaEndpoint++
		case "":
			viaShared++
		default:
			unknown++
		}
	}
	if viaEndpoint == 0 || viaShared != 2 || unknown != 1 {
		t.Errorf("history endpoints: via endpoint %d, shared %d, unknown %d", viaEndpoint, viaShared, unknown)
	}
	var downgraded, rejected int
	for _, e := range events {
		if e.Endpoint == glEP.ID && e.Auth == AuthUnsignedToken {
			downgraded++
		}
		if e.Endpoint == glEP.ID && e.Auth == "failed" && strings.Contains(e.Reason, "signed deliveries only") {
			rejected++
		}
	}
	if downgraded != 1 || rejected != 1 {
		t.Errorf("the Webhooks page shows the downgrade (%d) and the rejection (%d)", downgraded, rejected)
	}
}
