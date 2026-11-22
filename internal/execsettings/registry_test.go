package execsettings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRegistry serves /v2/<name>/tags/list in two pages behind auth:
// "bearer" (token from /token, anonymous unless creds are required) or
// "basic".
func fakeRegistry(t *testing.T, mode string, needCreds bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, hasBasic := r.BasicAuth()
		credsOK := hasBasic && user == "robot" && pass == "s3cret"
		switch {
		case r.URL.Path == "/token":
			if needCreds && !credsOK {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("scope") != "repository:team/crb:pull" {
				t.Errorf("scope = %q", r.URL.Query().Get("scope"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
			return
		case r.URL.Path != "/v2/team/crb/tags/list":
			http.NotFound(w, r)
			return
		}
		authed := (mode == "bearer" && r.Header.Get("Authorization") == "Bearer tok") || (mode == "basic" && credsOK)
		if !authed {
			if mode == "bearer" {
				w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="fake"`)
			} else {
				w.Header().Set("Www-Authenticate", `Basic realm="fake"`)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("last") == "" {
			w.Header().Set("Link", `</v2/team/crb/tags/list?n=1000&last=0.4.0>; rel="next"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"0.3.0", "0.4.0", "latest", "0.4.1-rc.1"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"0.10.0", "v0.5.2", "0.4.1", "sha256-abc.sig"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tagsFor(srv *httptest.Server, env map[string]string) *Tags {
	tg := NewTags()
	tg.Client = srv.Client()
	tg.Getenv = func(k string) string { return env[k] }
	return tg
}

func TestTagsFromYourRegistry(t *testing.T) {
	ctx := context.Background()
	creds := map[string]string{EnvRegistryUser: "robot", EnvRegistryPassword: "s3cret"}
	want := "0.10.0,v0.5.2,0.4.1,0.4.0" // releases at or above the minimum, newest first; across pages
	for name, c := range map[string]struct {
		mode  string
		need  bool
		env   map[string]string
		fails bool
	}{
		"public (anonymous token)": {mode: "bearer"},
		"private token registry":   {mode: "bearer", need: true, env: creds},
		"private basic registry":   {mode: "basic", env: creds},
		"private without creds":    {mode: "bearer", need: true, fails: true},
		"basic without creds":      {mode: "basic", fails: true},
	} {
		srv := fakeRegistry(t, c.mode, c.need)
		repo := strings.TrimPrefix(srv.URL, "https://") + "/team/crb"
		got, err := tagsFor(srv, c.env).List(ctx, repo)
		if c.fails {
			if err == nil || !strings.Contains(err.Error(), EnvRegistryUser) {
				t.Errorf("%s: want an error naming the credential env vars, got %v", name, err)
			}
			continue
		}
		if err != nil || strings.Join(got, ",") != want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestTagsCacheKeepsLastGoodList(t *testing.T) {
	ctx := context.Background()
	srv := fakeRegistry(t, "bearer", false)
	repo := strings.TrimPrefix(srv.URL, "https://") + "/team/crb"
	tg := tagsFor(srv, nil)
	if l, err := tg.List(ctx, repo); err != nil || len(l) != 4 {
		t.Fatalf("%v %v", l, err)
	}
	srv.Close()
	tg.TTL = 0 // expire the cache: the registry is down now
	if l, err := tg.List(ctx, repo); err == nil || len(l) != 4 {
		t.Errorf("a failed refresh keeps the last list and reports the error: %v %v", l, err)
	}
}

func TestRegistryRef(t *testing.T) {
	for repo, want := range map[string][2]string{
		"ghcr.io/divmora/code-review-bot":   {"https://ghcr.io", "divmora/code-review-bot"},
		"harbor.example.com:8443/tools/crb": {"https://harbor.example.com:8443", "tools/crb"},
		"localhost/crb":                     {"https://localhost", "crb"},
		"divmora/code-review-bot":           {"https://registry-1.docker.io", "divmora/code-review-bot"},
		"alpine":                            {"https://registry-1.docker.io", "library/alpine"},
		"docker.io/divmora/code-review-bot": {"https://registry-1.docker.io", "divmora/code-review-bot"},
	} {
		if b, n := registryRef(repo); b != want[0] || n != want[1] {
			t.Errorf("%s: %s %s", repo, b, n)
		}
	}
	if got := nextPage("https://r.example", `<https://evil.example/v2/x/tags/list>; rel="next"`); got != "" {
		t.Errorf("a next link to another host is not followed: %s", got)
	}
}
