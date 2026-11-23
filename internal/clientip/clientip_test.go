package clientip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func req(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestIP(t *testing.T) {
	none, _ := NewResolver(nil)
	if got := none.IP(req("203.0.113.9:4000", "1.2.3.4")); got != "203.0.113.9" {
		t.Errorf("without trusted proxies the header is ignored: %s", got)
	}
	lb, err := NewResolver([]string{"10.0.0.0/8", "192.168.1.5"})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		r    *http.Request
		want string
	}{
		"through the LB":           {req("10.1.2.3:80", "198.51.100.7"), "198.51.100.7"},
		"forged left hop ignored":  {req("10.1.2.3:80", "6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		"two trusted hops":         {req("10.1.2.3:80", "198.51.100.7, 192.168.1.5"), "198.51.100.7"},
		"several headers":          {req("10.1.2.3:80", "6.6.6.6", "198.51.100.7"), "198.51.100.7"},
		"untrusted peer":           {req("203.0.113.9:80", "198.51.100.7"), "203.0.113.9"},
		"no header from the LB":    {req("10.1.2.3:80"), "10.1.2.3"},
		"malformed hop":            {req("10.1.2.3:80", "garbage"), "10.1.2.3"},
		"IPv6 peer":                {req("[2001:db8::1]:443"), "2001:db8::1"},
		"IPv4-mapped trusted peer": {req("[::ffff:10.0.0.1]:80", "198.51.100.7"), "198.51.100.7"},
	} {
		if got := lb.IP(c.r); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
	if _, err := NewResolver([]string{"not-an-ip"}); err == nil {
		t.Error("bad entries are refused")
	}
}

func TestMiddleware(t *testing.T) {
	r, _ := NewResolver(nil)
	var got Client
	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { got = From(req.Context()) }))
	rq := req("203.0.113.9:4000")
	rq.Header.Set("User-Agent", strings.Repeat("x", 500))
	h.ServeHTTP(httptest.NewRecorder(), rq)
	if got.IP != "203.0.113.9" || len(got.UserAgent) != maxUserAgent {
		t.Errorf("client = %+v", got)
	}
	if (From(context.Background()) != Client{}) {
		t.Error("no client outside a request")
	}
}

func TestHops(t *testing.T) {
	base, _ := NewResolver(nil)
	one := base.WithHops(1)
	for name, c := range map[string]struct {
		r    *http.Request
		want string
	}{
		// An ALB appends the address it saw: the client can't push it out.
		"through the LB":             {req("10.0.5.7:80", "198.51.100.7"), "198.51.100.7"},
		"forged entries are ignored": {req("10.0.5.7:80", "203.0.113.99, 6.6.6.6, 198.51.100.7"), "198.51.100.7"},
		"a VPN client in the VPC":    {req("10.0.5.7:80", "203.0.113.99, 10.0.9.20"), "10.0.9.20"},
		"skipped the LB":             {req("198.51.100.8:4000"), "198.51.100.8"},
		"malformed last hop":         {req("10.0.5.7:80", "198.51.100.7, junk"), "10.0.5.7"},
	} {
		if got := one.IP(c.r); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
	if got := base.WithHops(2).IP(req("10.0.5.7:80", "203.0.113.99, 198.51.100.7, 10.0.1.1")); got != "198.51.100.7" {
		t.Errorf("two proxies: %s", got)
	}
}

func TestWideRanges(t *testing.T) {
	r, _ := NewResolver([]string{"10.0.0.0/8", "10.0.12.0/24", "10.0.5.7", "fd00::/8", "2001:db8:1:2::/64"})
	if got := strings.Join(r.WideRanges(), " "); got != "10.0.0.0/8 fd00::/8" {
		t.Errorf("wide: %s", got)
	}
}
