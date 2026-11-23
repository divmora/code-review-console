package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
)

func bbSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestBitbucketWebhook(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.Bitbucket = []config.Bitbucket{{APIURL: "http://127.0.0.1:1", Token: "t", WebhookSecret: "bbsecret", Identity: config.Identity{Reviewer: "review-bot"}}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	send := func(event, body, secret string) (int, string) {
		req := httptest.NewRequest("POST", hookPath("bitbucket", 1), bytes.NewReader([]byte(body)))
		req.Header.Set("X-Event-Key", event)
		req.Header.Set("X-Hub-Signature", bbSign(secret, []byte(body)))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	pr := func(id, state, src string, reviewers string) string {
		return `{"actor":{"nickname":"dev"},"repository":{"full_name":"acme/web","links":{"html":{"href":"https://bitbucket.org/acme/web"}},"mainbranch":{"name":"main"}},` +
			`"pullrequest":{"id":` + id + `,"state":"` + state + `","source":{"branch":{"name":"` + src + `"}},"destination":{"branch":{"name":"release"}},"reviewers":[` + reviewers + `]}}`
	}
	if c, _ := send("pullrequest:created", pr("1", "OPEN", "feat", ""), "wrong"); c != http.StatusUnauthorized {
		t.Errorf("bad signature: %d", c)
	}
	if c, b := send("pullrequest:created", pr("1", "OPEN", "feat", ""), "bbsecret"); c != http.StatusAccepted {
		t.Errorf("new PR should queue: %d %s", c, b)
	}
	if c, b := send("pullrequest:created", pr("2", "OPEN", "main", ""), "bbsecret"); c != http.StatusOK || !strings.Contains(b, "branch sync") {
		t.Errorf("main -> release is a branch sync: %d %s", c, b)
	}
	if c, b := send("pullrequest:fulfilled", pr("1", "MERGED", "feat", ""), "bbsecret"); c != http.StatusOK || !strings.Contains(b, "cancelled") {
		t.Errorf("merge should cancel the waiting review: %d %s", c, b)
	}
	s.settings.Runtime(context.Background()) // no store: settings come from the config
	s.cfg.Repos.RequireReviewer = true
	if c, b := send("pullrequest:updated", pr("3", "OPEN", "feat", `{"nickname":"alice"}`), "bbsecret"); c != http.StatusOK || !strings.Contains(b, "@review-bot is not a reviewer") {
		t.Errorf("reviewer required: %d %s", c, b)
	}
	if c, b := send("pullrequest:updated", pr("3", "OPEN", "feat", `{"nickname":"review-bot"}`), "bbsecret"); c != http.StatusAccepted {
		t.Errorf("bot as reviewer should queue: %d %s", c, b)
	}
}
