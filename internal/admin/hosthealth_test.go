package admin

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
)

// TestHostHealth: the Code hosts page shows the latest credentials check
// with its token user, the real mention handle, and each webhook's latest
// delivery and 24-hour counts (issue #131).
func TestHostHealth(t *testing.T) {
	e := setup(t, config.OIDC{AllowedEmails: []string{"alice@example.com"}})
	e.idp.as(map[string]any{"sub": "u1", "email": "alice@example.com", "email_verified": true})
	_ = e.login(t).Body.Close()
	ctx := context.Background()
	h, err := e.adm.Hosts.Save(ctx, hosts.Host{Platform: hosts.GitLab, BaseURL: "https://gitlab.example.com", Enabled: true, Secrets: hosts.Secrets{Token: "t"}}, "a@x")
	if err != nil {
		t.Fatal(err)
	}
	ep, _, err := e.adm.Hosts.AddEndpoint(ctx, h.ID, "group", "a@x")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := e.adm.Hosts.AddEndpoint(ctx, h.ID, "unused", "a@x")
	if err != nil {
		t.Fatal(err)
	}
	type hook struct {
		ID         string                   `json:"id"`
		Deliveries *store.EndpointHookStats `json:"deliveries"`
	}
	type view struct {
		ID            int64
		Mention       string       `json:"mention"`
		MentionSource string       `json:"mention_source"`
		Check         *hosts.Check `json:"check"`
		Webhooks      []hook       `json:"webhooks"`
	}
	get := func() view {
		t.Helper()
		_, body := e.do(t, "GET", "/admin/api/hosts", "", nil)
		var list struct{ Hosts []view }
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		for _, v := range list.Hosts {
			if v.ID == h.ID {
				return v
			}
		}
		t.Fatalf("host missing: %s", body)
		return view{}
	}

	v := get()
	if v.Mention != "" || v.Check != nil || v.Webhooks[0].Deliveries != nil {
		t.Errorf("nothing known yet: %+v", v)
	}
	// The startup identity lookup gives the token user and the handle.
	e.adm.Hosts.RecordCheck(h.ID, hosts.Check{OK: true, Summary: "OK as @gitlab-bot", User: "gitlab-bot", At: time.Now(), Source: "startup"})
	if v = get(); v.Mention != "gitlab-bot" || v.MentionSource != "token" || v.Check == nil || v.Check.Source != "startup" {
		t.Errorf("after the startup check: %+v", v)
	}
	// A bot name on the host wins.
	h.BotName = "review-bot"
	if _, err := e.adm.Hosts.Save(ctx, h, "a@x"); err != nil {
		t.Fatal(err)
	}
	if v = get(); v.Mention != "review-bot" || v.MentionSource != "host" {
		t.Errorf("with a bot name: %+v", v)
	}

	old := time.Now().Add(-48 * time.Hour)
	_ = e.st.InsertWebhookEvents(ctx, []store.WebhookEvent{
		{Platform: "gitlab", Host: h.Host, Endpoint: ep.ID, Auth: "signature", Outcome: store.HookQueued, ReceivedAt: old}, // too old to count
		{Platform: "gitlab", Host: h.Host, Endpoint: ep.ID, Auth: "signature", Outcome: store.HookQueued},
		{Platform: "gitlab", Host: h.Host, Endpoint: ep.ID, Auth: "signature", Outcome: store.HookIgnored},
		{Platform: "gitlab", Host: h.Host, Endpoint: ep.ID, Auth: "failed", Outcome: store.HookRejected, Reason: "invalid token"},
	})
	v = get()
	var got, unused *store.EndpointHookStats
	for _, w := range v.Webhooks {
		switch w.ID {
		case ep.ID:
			got = w.Deliveries
		case other.ID:
			unused = w.Deliveries
		}
	}
	if got == nil || got.Accepted != 2 || got.Rejected != 1 || got.Last == nil || got.Last.Reason != "invalid token" {
		t.Errorf("deliveries = %+v", got)
	}
	if unused != nil {
		t.Errorf("a webhook without deliveries has no stats: %+v", unused)
	}

	// The test-delivery helper and the Webhooks page filter by webhook.
	q := url.Values{"endpoint": {ep.ID}}
	_, body := e.do(t, "GET", "/admin/api/webhooks?"+q.Encode(), "", nil)
	var list struct{ Events []store.WebhookEvent }
	_ = json.Unmarshal(body, &list)
	if len(list.Events) != 4 {
		t.Errorf("deliveries on the webhook: got %d: %s", len(list.Events), body)
	}
	q.Set("endpoint", other.ID)
	_, body = e.do(t, "GET", "/admin/api/webhooks?"+q.Encode(), "", nil)
	if err := json.Unmarshal(body, &list); err != nil || len(list.Events) != 0 {
		t.Errorf("another webhook: %s", body)
	}
}
