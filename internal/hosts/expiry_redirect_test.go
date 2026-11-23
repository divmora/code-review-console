package hosts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #124: the GitLab token expiry lookup doesn't follow a redirect to
// another host with PRIVATE-TOKEN.
func TestGitLabExpiryRefusesCrossHostRedirect(t *testing.T) {
	var leaked atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("PRIVATE-TOKEN"))
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer gl.Close()
	_, _, err := gitlabExpiry(context.Background(), Host{Platform: GitLab, BaseURL: gl.URL, Token: "glpat-secret"})
	if err == nil || !strings.Contains(err.Error(), "another host") {
		t.Errorf("refused: %v", err)
	}
	if v, _ := leaked.Load().(string); v != "" {
		t.Errorf("the token reached the other host: %q", v)
	}
}
