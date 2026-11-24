package execsettings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDigest: the digest is the hash of the manifest the tag points to,
// behind the registry's token auth, and a Docker-Content-Digest that
// doesn't match the manifest is refused (issue #135).
func TestDigest(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	sum := sha256.Sum256(manifest)
	want := "sha256:" + hex.EncodeToString(sum[:])
	header := want
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"token":"tok"}`))
		case "/v2/team/crb/manifests/0.6.1":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="fake"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				t.Errorf("Accept = %q", r.Header.Get("Accept"))
			}
			w.Header().Set("Docker-Content-Digest", header)
			_, _ = w.Write(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "https://") + "/team/crb"
	tg := tagsFor(srv, nil)
	if d, err := tg.Digest(context.Background(), repo, "0.6.1"); err != nil || d != want {
		t.Fatalf("digest = %q, %v; want %s", d, err, want)
	}
	if _, err := tg.Digest(context.Background(), repo, "9.9.9"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("unknown tag: %v", err)
	}
	header = "sha256:" + strings.Repeat("0", 64)
	if _, err := tagsFor(srv, nil).Digest(context.Background(), repo, "0.6.1"); err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Errorf("mismatched Docker-Content-Digest: %v", err)
	}
}

func TestPinnedTag(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	if tag, got, ok := PinnedTag("0.6.1@" + d); !ok || tag != "0.6.1" || got != d {
		t.Errorf("pinned: %q %q %v", tag, got, ok)
	}
	for _, v := range []string{"0.6.1", d, ""} {
		if _, _, ok := PinnedTag(v); ok {
			t.Errorf("%q is not a pinned tag", v)
		}
	}
	if !PlainTag("0.6.1") || PlainTag("0.6.1@"+d) || PlainTag(d) {
		t.Error("PlainTag")
	}
}
