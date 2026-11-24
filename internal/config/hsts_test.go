package config

import (
	"testing"
	"time"
)

func TestHSTSHeader(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name string
		a    Admin
		want string
	}{
		{"https default", Admin{BaseURL: "https://review.example.com"}, "max-age=31536000"},
		{"https upper case", Admin{BaseURL: "HTTPS://review.example.com"}, "max-age=31536000"},
		{"http", Admin{BaseURL: "http://review.example.com"}, ""},
		{"no base url", Admin{}, ""},
		{"turned off", Admin{BaseURL: "https://r.example.com", HSTS: HSTS{Enabled: &off}}, ""},
		{"max age and subdomains", Admin{BaseURL: "https://r.example.com", HSTS: HSTS{MaxAge: time.Hour, IncludeSubDomains: true}}, "max-age=3600; includeSubDomains"},
	} {
		if got := tc.a.HSTSHeader(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHSTSEnv(t *testing.T) {
	t.Setenv("CODEREVIEW_CONFIG", "")
	t.Setenv("CODEREVIEW_CONSOLE_BASE_URL", "https://r.example.com")
	t.Setenv("CODEREVIEW_CONSOLE_HSTS_MAX_AGE", "48h")
	t.Setenv("CODEREVIEW_CONSOLE_HSTS_INCLUDE_SUBDOMAINS", "true")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Admin.HSTSHeader(), "max-age=172800; includeSubDomains"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	t.Setenv("CODEREVIEW_CONSOLE_HSTS", "false")
	if c, err = Load(""); err != nil {
		t.Fatal(err)
	}
	if got := c.Admin.HSTSHeader(); got != "" {
		t.Fatalf("turned off: got %q", got)
	}
}
