package hosts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLookupTokenExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/personal_access_tokens/self" && r.Header.Get("PRIVATE-TOKEN") == "dated":
			_, _ = io.WriteString(w, `{"expires_at":"2027-03-01"}`)
		case r.URL.Path == "/api/v4/personal_access_tokens/self" && r.Header.Get("PRIVATE-TOKEN") == "forever":
			_, _ = io.WriteString(w, `{"expires_at":null}`)
		case r.URL.Path == "/api/v4/personal_access_tokens/self" && r.Header.Get("PRIVATE-TOKEN") == "old-gitlab":
			w.WriteHeader(404)
		case r.URL.Path == "/user" && r.Header.Get("Authorization") == "Bearer gh-dated":
			w.Header().Set("Github-Authentication-Token-Expiration", "2027-01-15 08:30:00 UTC")
			_, _ = io.WriteString(w, `{"login":"bot"}`)
		case r.URL.Path == "/user" && r.Header.Get("Authorization") == "Bearer gh-classic":
			_, _ = io.WriteString(w, `{"login":"bot"}`)
		default:
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	gl := func(tok string) TokenExpiry {
		return LookupTokenExpiry(ctx, Host{Platform: GitLab, BaseURL: srv.URL, Secrets: Secrets{Token: tok}})
	}
	if e := gl("dated"); !e.Known || !e.Expires.Equal(time.Date(2027, 3, 2, 0, 0, 0, 0, time.UTC)) || e.Error != "" {
		t.Errorf("GitLab date: works through that day: %+v", e)
	}
	if e := gl("forever"); !e.Known || !e.Expires.IsZero() {
		t.Errorf("GitLab without expiry: %+v", e)
	}
	if e := gl("old-gitlab"); e.Known || e.Error != "" {
		t.Errorf("an older GitLab doesn't say: %+v", e)
	}
	if e := gl("revoked"); e.Known || e.Error == "" {
		t.Errorf("a rejected token is an error: %+v", e)
	}
	gh := func(tok string, app int64) TokenExpiry {
		return LookupTokenExpiry(ctx, Host{Platform: GitHub, BaseURL: srv.URL, Settings: Settings{AppID: app}, Secrets: Secrets{Token: tok}})
	}
	if e := gh("gh-dated", 0); !e.Known || !e.Expires.Equal(time.Date(2027, 1, 15, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("GitHub header: %+v", e)
	}
	if e := gh("gh-classic", 0); !e.Known || !e.Expires.IsZero() {
		t.Errorf("GitHub token without expiry: %+v", e)
	}
	if e := gh("gh-dated", 7); e.Known || !e.Checked.After(time.Time{}) {
		t.Errorf("a GitHub App mints its own tokens: %+v", e)
	}
	if e := LookupTokenExpiry(ctx, Host{Platform: Bitbucket, Secrets: Secrets{Token: "x"}}); e.Known {
		t.Errorf("Bitbucket doesn't report expiry: %+v", e)
	}
}

func TestSecretChanges(t *testing.T) {
	old := Secrets{Token: "a", WebhookSecrets: []string{"s1"}, SigningTokens: []string{"t1"}}
	got := SecretChanges(old, Secrets{Token: "b", AppPrivateKey: "k", WebhookSecrets: []string{"s1"}, SigningTokens: []string{"t1", "t2"}})
	want := "token:changed app_private_key:set signing_tokens:+1"
	if j := strings.Join(got, " "); j != want {
		t.Errorf("got %q, want %q", j, want)
	}
	got = SecretChanges(old, Secrets{WebhookSecrets: []string{"s2"}})
	if j := strings.Join(got, " "); j != "token:removed webhook_secrets:replaced signing_tokens:-1" {
		t.Errorf("got %q", j)
	}
	if got := SecretChanges(old, old); len(got) != 0 {
		t.Errorf("nothing changed: %v", got)
	}
	if got := EndpointSecretChanges(EndpointSecrets{}, EndpointSecrets{WebhookSecrets: []string{"x"}}); strings.Join(got, " ") != "webhook_secrets:+1" {
		t.Errorf("endpoint: %v", got)
	}
}
