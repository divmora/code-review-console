package server

import (
	"bytes"
	"context"
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

// TestWebhookAddedElsewhereWorks: a webhook another replica (or "console
// hosts import") adds works on this console's first delivery, without a
// restart (issue #101).
func TestWebhookAddedElsewhereWorks(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CODEREVIEW_CONFIG", "")
	cfg, _ := config.Load("")
	cfg.GitLab = []config.GitLab{{BaseURL: "http://127.0.0.1:1", Token: "t"}}
	cfg.Models = map[string]config.ModelProfile{"m": {"provider": "ollama", "model": "x"}}
	cfg.Model = "m"
	st := storetest.New()
	box, _ := secrets.New("a test passphrase that is long enough for a key")
	registry := func() *hosts.Registry {
		r := hosts.New(cfg, st, box, quiet)
		r.Lic = licensetest.Full(t)
		if _, err := r.Init(ctx); err != nil {
			t.Fatal(err)
		}
		return r
	}
	other, mine := registry(), registry()
	s, err := New(cfg, &fakeExec{}, quiet, Deps{Store: st, Hosts: mine, License: licensetest.Full(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer stop(s)
	send := func(path, token string) int {
		body := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"g/p","web_url":"http://127.0.0.1:1/g/p"},"object_attributes":{"iid":1,"action":"approved"}}`)
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
		req.Header.Set("X-Gitlab-Token", token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w.Code
	}
	gl := other.All()[0]
	e1, secret1, err := other.AddEndpoint(ctx, gl.ID, "added on another replica", "a@x")
	if err != nil {
		t.Fatal(err)
	}
	if c := send(e1.Path("gitlab"), secret1); c != http.StatusOK {
		t.Fatalf("first delivery to a webhook added elsewhere: %d", c)
	}

	// Made-up URLs check the database at most once a second.
	e2, secret2, _ := other.AddEndpoint(ctx, gl.ID, "second", "a@x")
	if s.refreshOnMiss(ctx) {
		t.Error("a second check within a second is skipped")
	}
	// The background loop picks it up anyway.
	s.reloadChangedHosts(ctx)
	if c := send(e2.Path("gitlab"), secret2); c != http.StatusOK {
		t.Errorf("after the periodic reload: %d", c)
	}

	// A secret rotated elsewhere: the old one stops working after a reload.
	e2.WebhookSecrets = []string{"rotated-elsewhere-secret"}
	if _, err := other.SaveEndpoint(ctx, e2, "a@x"); err != nil {
		t.Fatal(err)
	}
	s.reloadChangedHosts(ctx)
	if c := send(e2.Path("gitlab"), secret2); c != http.StatusUnauthorized {
		t.Errorf("the old secret after a rotation elsewhere: %d", c)
	}
	if hostsRefreshEvery > 10*time.Second {
		t.Errorf("changes elsewhere should apply within seconds: %s", hostsRefreshEvery)
	}
}
