package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license/licensetest"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// Issue #127: a delivery signed with one GitHub host's webhook secret
// can't speak for another configured GitHub host, and a saved job runs
// with the receiving host's credentials after a restart.
func TestGitHubWebhookCantClaimAnotherHost(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitHub = config.GitHub{APIURL: "http://127.0.0.1:1", Token: "token-a"}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Server.Debounce = time.Hour
	st := storetest.New()
	box, _ := secrets.New("a test passphrase that is long enough for a key")
	reg := hosts.New(cfg, st, box, quiet)
	reg.Lic = licensetest.Full(t)
	if _, err := reg.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var a hosts.Host
	for _, h := range reg.All() {
		a = h
	}
	b, err := reg.Save(ctx, hosts.Host{Platform: hosts.GitHub, BaseURL: "http://127.0.0.2:1", Enabled: true, Secrets: hosts.Secrets{Token: "token-b"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	ep, secret, err := reg.AddEndpoint(ctx, a.ID, "a hook", "a@x")
	if err != nil {
		t.Fatal(err)
	}
	s, err := newHooked(t, cfg, &fakeExec{}, quiet, Deps{Store: st, Hosts: reg, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(ctx)
	send := func(htmlURL string, n string) int {
		body := []byte(`{"action":"opened","number":` + n + `,"repository":{"full_name":"o/r","html_url":"` + htmlURL + `"},"pull_request":{"number":` + n + `}}`)
		req := httptest.NewRequest("POST", ep.Path("github"), bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", sign(secret, body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	// Host B is configured: A's webhook can't name it.
	if c := send("http://"+b.Host+"/o/r", "1"); c != http.StatusForbidden {
		t.Errorf("a delivery naming another configured host: %d", c)
	}
	for _, q := range st.Queued() {
		t.Errorf("nothing is queued for it: %s", q.Payload)
	}
	// A web host no other host uses (a proxy, ghe.com) still works, and
	// the saved job is bound to host A.
	if c := send("https://github.com/o/r", "2"); c != http.StatusAccepted {
		t.Fatalf("a web host of its own: %d", c)
	}
	q := st.Queued()
	if len(q) != 1 {
		t.Fatalf("queued: %d", len(q))
	}
	var p queuedPayload
	if err := json.Unmarshal(q[0].Payload, &p); err != nil || p.HostID != a.ID || p.Host != "github.com" {
		t.Fatalf("saved payload: %v %s", err, q[0].Payload)
	}
	// Restored, it runs with A's credentials, even when the saved host
	// name is another configured host's (an older or tampered payload).
	p.Host = b.Host
	tgt, err := s.targetByID(p.Platform, p.HostID, p.Installation, p.Repo)
	if err != nil || tgt.host.ID != a.ID {
		t.Fatalf("by ID: %v %+v", err, tgt.host)
	}
	if env, _ := tgt.secrets(ctx); env["GITHUB_TOKEN"] != "token-a" {
		t.Errorf("A's token: %v", env)
	}
	// Payloads saved before host_id still restore by name.
	if tgt, err := s.targetFor(p.Platform, b.Host, 0, p.Repo); err != nil || tgt.host.ID != b.ID {
		t.Errorf("by name: %v", err)
	}
}
