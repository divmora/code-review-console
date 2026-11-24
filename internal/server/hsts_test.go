package server

import (
	"net/http/httptest"
	"testing"
)

func TestHSTS(t *testing.T) {
	s := testServer(t)
	paths := []string{"/healthz", "/dashboard", "/nope"}
	for _, p := range paths {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if got := w.Header().Get("Strict-Transport-Security"); got != "" {
			t.Fatalf("%s without an https base_url: got %q", p, got)
		}
	}
	s.cfg.Admin.BaseURL = "https://review.example.com"
	h := s.Handler()
	for _, p := range paths {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if got, want := w.Header().Get("Strict-Transport-Security"), "max-age=31536000"; got != want {
			t.Fatalf("%s: got %q, want %q", p, got, want)
		}
	}
	// A webhook delivery, even a refused one, gets it too.
	w := post(t, h, "ping", []byte(`{}`), "sha256=bad")
	if got := w.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatalf("webhook response (%d) has no Strict-Transport-Security", w.Code)
	}
}
