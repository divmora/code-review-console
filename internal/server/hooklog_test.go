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
	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

func TestOutcome(t *testing.T) {
	for _, c := range []struct {
		status        int
		body, out, re string
	}{
		{202, `{"status":"accepted","job":"review"}`, store.HookQueued, "review"},
		{200, `{"status":"ignored","reason":"merge request action update"}`, store.HookIgnored, "merge request action update"},
		{200, `{"status":"duplicate delivery"}`, store.HookDuplicate, "already processed this delivery"},
		{200, `{"status":"pong"}`, store.HookIgnored, "ping"},
		{401, "invalid signature or token\n", store.HookRejected, "invalid signature or token"},
		{503, `{"error":"queue full"}`, store.HookError, "queue full"},
	} {
		if o, r := outcome(c.status, []byte(c.body)); o != c.out || r != c.re {
			t.Errorf("%d %s: got %s/%q", c.status, c.body, o, r)
		}
	}
}

func TestWebhookHistoryAndRepoRecording(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t", SigningToken: testSigningToken}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	cfg.Repos.Allowlist = true
	st := storetest.New()
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.queue.Shutdown(context.Background())
	send := func(id string, body []byte, signed bool) int {
		req := httptest.NewRequest("POST", "/webhooks/gitlab", bytes.NewReader(body))
		req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
		if signed {
			for k, v := range signedHeaders(testSigningKey, id, time.Now().Unix(), body) {
				req.Header[k] = v
			}
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	// A metadata-only update: ignored before the allowlist, but the
	// project must still be recorded so an admin can enable it.
	update := []byte(`{"object_kind":"merge_request","user":{"username":"ops-bot"},"project":{"path_with_namespace":"grp/app"},"object_attributes":{"iid":5,"action":"update","oldrev":""}}`)
	if c := send("m1", update, true); c != http.StatusOK {
		t.Fatalf("update: %d", c)
	}
	open := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"grp/app"},"object_attributes":{"iid":5,"action":"open"}}`)
	_ = send("m2", open, true)
	_ = send("m3", open, false) // unsigned: rejected
	s.Close()

	repo, err := st.FindRepo(context.Background(), "gitlab", "127.0.0.1:1", "grp/app")
	if err != nil || repo.Mode != store.ModeInherit {
		t.Fatalf("every project that sends a verified webhook should be recorded as inherit: %+v %v", repo, err)
	}
	events, _ := st.ListWebhookEvents(context.Background(), store.WebhookFilter{})
	if len(events) != 3 {
		t.Fatalf("want 3 deliveries, got %+v", events)
	}
	rej, ign2, ign1 := events[0], events[1], events[2]
	if ign1.Outcome != store.HookIgnored || ign1.Reason != "merge request action update" || ign1.Project != "grp/app" ||
		ign1.Number != 5 || ign1.Sender != "ops-bot" || ign1.Auth != "signature" || ign1.Delivery != "m1" {
		t.Errorf("metadata update: %+v", ign1)
	}
	if ign2.Outcome != store.HookIgnored || ign2.Reason != "repository is not enabled in the admin panel" {
		t.Errorf("not enabled: %+v", ign2)
	}
	if rej.Outcome != store.HookRejected || rej.Auth != "failed" || !strings.HasPrefix(rej.Reason, "no X-Gitlab-Token or Webhook-Signature header") {
		t.Errorf("rejected delivery should keep the exact reason: %+v", rej)
	}
	if rej.SourceIP != "192.0.2.1" || ign1.SourceIP != "192.0.2.1" {
		t.Errorf("deliveries record where they came from: %q %q", rej.SourceIP, ign1.SourceIP)
	}
	// Nothing from an unverified body is recorded (issue #33).
	if rej.Project != "" || rej.Number != 0 || rej.Kind != "" || rej.Action != "" || rej.Sender != "" {
		t.Errorf("rejected delivery must not carry payload fields: %+v", rej)
	}
	counts, _ := st.CountWebhookEvents(context.Background(), time.Now().Add(-time.Hour))
	if counts[store.HookIgnored] != 2 || counts[store.HookRejected] != 1 {
		t.Errorf("counts = %v", counts)
	}
}
