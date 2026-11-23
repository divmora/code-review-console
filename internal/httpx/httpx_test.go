package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSameHostRedirects(t *testing.T) {
	req := func(u string) *http.Request { r, _ := http.NewRequest("GET", u, nil); return r }
	from := []*http.Request{req("https://gitlab.example.com/api/v4/x")}
	for u, ok := range map[string]bool{
		"https://gitlab.example.com/api/v4/y": true,
		"https://GITLAB.example.com/api/v4/y": true,
		"https://evil.example/api/v4/y":       false,
		"https://sub.gitlab.example.com/y":    false,
		"http://gitlab.example.com/api/v4/y":  false,
	} {
		if err := SameHostRedirects(req(u), from); (err == nil) != ok {
			t.Errorf("%s: %v", u, err)
		}
	}
}

// Issue #124: a 3xx to another host doesn't carry PRIVATE-TOKEN there.
func TestClientKeepsTheTokenOnItsHost(t *testing.T) {
	var leaked atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("PRIVATE-TOKEN"))
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1) // another host to the rule
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/moved", http.StatusFound)
			return
		}
		http.Redirect(w, r, otherURL+"/collect", http.StatusFound)
	}))
	defer gl.Close()
	c := Client(5e9, false)
	r, _ := http.NewRequest("GET", gl.URL+"/api/v4/user", nil)
	r.Header.Set("PRIVATE-TOKEN", "glpat-secret")
	if _, err := c.Do(r); err == nil || !strings.Contains(err.Error(), "another host") {
		t.Errorf("refused: %v", err)
	}
	if v, _ := leaked.Load().(string); v != "" {
		t.Errorf("the token reached the other host: %q", v)
	}
	r, _ = http.NewRequest("GET", gl.URL+"/same", nil)
	if resp, err := c.Do(r); err != nil || resp.StatusCode != 200 {
		t.Errorf("a same-host redirect works: %v", err)
	}
}
