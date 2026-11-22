package execsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/divmora/code-review-console/internal/config"
)

// Registry credentials for listing tags of a private image repository
// (read-only is enough). Public repositories need none.
const (
	EnvRegistryUser     = "CODEREVIEW_CONSOLE_REGISTRY_USERNAME"
	EnvRegistryPassword = "CODEREVIEW_CONSOLE_REGISTRY_PASSWORD"
)

// Tags lists the code-review-bot versions available in the configured
// image repository, for the Executor page's version dropdown. It speaks the
// OCI distribution API, so it works with ghcr.io and with your own registry
// (Harbor, GitLab, Artifactory, Nexus, Docker Hub...). Results are cached;
// when the registry can't be reached the page falls back to typing.
type Tags struct {
	Client *http.Client
	TTL    time.Duration
	// Getenv reads the registry credentials (os.Getenv by default).
	Getenv func(string) string

	mu    sync.Mutex
	cache map[string]tagsEntry
}

type tagsEntry struct {
	list    []string
	err     error
	fetched time.Time
}

// NewTags returns a Tags with a short timeout and a 10 minute cache.
func NewTags() *Tags {
	return &Tags{Client: &http.Client{Timeout: 8 * time.Second}, TTL: 10 * time.Minute, Getenv: os.Getenv}
}

var releaseTag = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// List returns the release tags (x.y.z, at or above config.MinCLIVersion)
// in repo, newest first. A failed lookup is retried after a minute.
func (t *Tags) List(ctx context.Context, repo string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = map[string]tagsEntry{}
	}
	e := t.cache[repo]
	ttl := t.TTL
	if e.err != nil {
		ttl = time.Minute
	}
	if !e.fetched.IsZero() && time.Since(e.fetched) < ttl {
		return e.list, e.err
	}
	all, err := t.fetch(ctx, repo)
	e.fetched = time.Now()
	if err != nil {
		e.err = err // keep the last good list
		t.cache[repo] = e
		return e.list, err
	}
	list := []string{}
	for _, tag := range all {
		if !releaseTag.MatchString(tag) {
			continue
		}
		if ok, known := config.CLIVersionOK(tag); ok && known {
			list = append(list, tag)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return versionLess(list[j], list[i]) })
	t.cache[repo] = tagsEntry{list: list, fetched: e.fetched}
	return list, nil
}

// registryRef splits an image repository into the registry's API base URL
// and the repository name, the way docker does: a first part with a dot or
// a port (or "localhost") is the registry, otherwise it is Docker Hub.
func registryRef(repo string) (base, name string) {
	host, rest, ok := strings.Cut(repo, "/")
	if ok && (strings.ContainsAny(host, ".:") || host == "localhost") {
		if host == "docker.io" {
			host = "registry-1.docker.io"
		}
		return "https://" + host, rest
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return "https://registry-1.docker.io", repo
}

func (t *Tags) fetch(ctx context.Context, repo string) ([]string, error) {
	base, name := registryRef(repo)
	next := base + "/v2/" + name + "/tags/list?n=1000"
	auth := ""
	var tags []string
	for page := 0; next != "" && page < 20; page++ {
		resp, err := t.get(ctx, next, auth)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && auth == "" {
			challenge := resp.Header.Get("Www-Authenticate")
			_ = resp.Body.Close()
			if auth, err = t.authorize(ctx, challenge, name); err != nil {
				return nil, err
			}
			page--
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			hint := ""
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				hint = " (for a private repository, set " + EnvRegistryUser + " and " + EnvRegistryPassword + ")"
			}
			return nil, fmt.Errorf("list tags of %s: HTTP %d%s", repo, resp.StatusCode, hint)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body)
		link := resp.Header.Get("Link")
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list tags of %s: %w", repo, err)
		}
		tags = append(tags, body.Tags...)
		next = nextPage(base, link)
	}
	return tags, nil
}

func (t *Tags) get(ctx context.Context, u, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	return resp, nil
}

// authorize answers a registry's auth challenge: Basic with the configured
// credentials, or Bearer with a token from the realm (anonymous for public
// repositories, the credentials otherwise).
func (t *Tags) authorize(ctx context.Context, challenge, name string) (string, error) {
	user, pass := t.Getenv(EnvRegistryUser), t.Getenv(EnvRegistryPassword)
	scheme, params := parseChallenge(challenge)
	switch scheme {
	case "basic":
		if user == "" {
			return "", fmt.Errorf("the registry needs credentials: set %s and %s", EnvRegistryUser, EnvRegistryPassword)
		}
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		req.SetBasicAuth(user, pass)
		return req.Header.Get("Authorization"), nil
	case "bearer":
	default:
		return "", fmt.Errorf("registry: unsupported auth challenge %q", challenge)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") || realm.Host == "" {
		return "", fmt.Errorf("registry: bad token realm %q", params["realm"])
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", "repository:"+name+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: HTTP %d (for a private repository, set %s and %s)", resp.StatusCode, EnvRegistryUser, EnvRegistryPassword)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", errors.New("registry token: empty token")
	}
	return "Bearer " + tok.Token, nil
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// parseChallenge reads `Bearer realm="...",service="...",scope="..."`.
func parseChallenge(h string) (scheme string, params map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params = map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(rest, -1) {
		params[strings.ToLower(m[1])] = m[2]
	}
	return strings.ToLower(scheme), params
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="?next"?`)

// nextPage reads the next page from a Link header (relative to base).
func nextPage(base, link string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "/") {
		return base + m[1]
	}
	if u, err := url.Parse(m[1]); err == nil && u.IsAbs() && strings.HasPrefix(m[1], base+"/") {
		return m[1]
	}
	return "" // don't follow a link to another host
}

// versionLess compares "1.2.3" versions numerically ("v" prefix ignored).
func versionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}
