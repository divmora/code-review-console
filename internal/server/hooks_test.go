package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/executor"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/secrets"
	"github.com/divmora/code-review-console/internal/store/storetest"
)

// newHooked is New for tests that post webhooks. Deliveries come in only
// on a webhook's own URL, so without deps.Hosts it builds a registry (with
// its own store) and gives each config host a webhook with the config
// file's secrets: /webhooks/<platform>/test-<platform>, and
// test-gitlab-2, -3… for further GitLab hosts (see hookPath).
func newHooked(t *testing.T, cfg *config.Config, exec executor.Executor, logger *slog.Logger, deps Deps) (*Server, error) {
	t.Helper()
	if deps.Hosts != nil {
		return New(cfg, exec, logger, deps)
	}
	ctx := context.Background()
	box, err := secrets.New("a test passphrase that is long enough for a key")
	if err != nil {
		t.Fatal(err)
	}
	reg := hosts.New(cfg, storetest.New(), box, logger)
	reg.Lic = deps.License
	if _, err := reg.Init(ctx); err != nil {
		return nil, err
	}
	add := func(platform, host string, n int, secretList, signing []string) {
		h, ok := findHost(reg, platform, host)
		if !ok {
			return
		}
		e := hosts.Endpoint{ID: hookID(platform, n), HostID: h.ID, Name: "test", Enabled: true,
			EndpointSecrets: hosts.EndpointSecrets{WebhookSecrets: secretList, SigningTokens: signing}}
		if len(clean(secretList)) == 0 {
			e.WebhookSecrets = []string{"unused-" + e.ID} // signing tokens only
		} else {
			e.AcceptSecretToken = true // the config accepted both
		}
		if _, err := reg.SaveEndpoint(ctx, e, "test"); err != nil {
			t.Fatalf("webhook for %s %s: %v", platform, host, err)
		}
	}
	if g := cfg.GitHub; g.Token != "" || g.AppID != 0 {
		add(hosts.GitHub, hosts.WebHost(hosts.GitHub, cfg.GitHub.APIURL), 1, []string{g.WebhookSecret}, nil)
	}
	for i, l := range cfg.GitLab {
		add(hosts.GitLab, hosts.WebHost(hosts.GitLab, l.BaseURL), i+1, l.AllSecrets(), l.AllSigningTokens())
	}
	for i, b := range cfg.Bitbucket {
		add(hosts.Bitbucket, hosts.WebHost(hosts.Bitbucket, firstNonEmpty(b.APIURL, "https://api.bitbucket.org/2.0")), i+1, []string{b.WebhookSecret}, nil)
	}
	deps.Hosts = reg
	return New(cfg, exec, logger, deps)
}

func findHost(reg *hosts.Registry, platform, host string) (hosts.Host, bool) {
	for _, h := range reg.All() {
		if h.Platform == platform && h.Host == host {
			return h, true
		}
	}
	return hosts.Host{}, false
}

func hookID(platform string, n int) string {
	if n <= 1 {
		return "test-" + platform
	}
	return fmt.Sprintf("test-%s-%d", platform, n)
}

// hookPath is the URL of the n-th (from 1) host's test webhook.
func hookPath(platform string, n int) string {
	return "/webhooks/" + platform + "/" + hookID(platform, n)
}

func clean(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
