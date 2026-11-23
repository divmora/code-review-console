// Package clientip finds who a request comes from: the client IP
// (following X-Forwarded-For only through trusted proxies) and the user
// agent. The server puts them in each request's context, and the store
// records them with audit entries, sessions and webhook deliveries.
package clientip

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Client is where a request comes from.
type Client struct {
	IP        string
	UserAgent string
}

// maxUserAgent bounds what is stored.
const maxUserAgent = 300

type ctxKey struct{}

// With returns ctx carrying c.
func With(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// From returns the client in ctx (zero when there is none, e.g. a
// background job).
func From(ctx context.Context) Client {
	c, _ := ctx.Value(ctxKey{}).(Client)
	return c
}

// Resolver finds a request's client IP. X-Forwarded-For is followed only
// from trusted proxies (a load balancer or ingress in front of the
// console); without any, the TCP peer is the client and the header,
// which anyone can send, is ignored.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver parses trusted proxies: IP addresses or CIDR ranges.
func NewResolver(trusted []string) (Resolver, error) {
	var r Resolver
	for _, t := range trusted {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if p, err := netip.ParsePrefix(t); err == nil {
			r.trusted = append(r.trusted, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(t)
		if err != nil {
			return Resolver{}, fmt.Errorf("trusted proxy %q: want an IP address or a CIDR range such as 10.0.0.0/8", t)
		}
		r.trusted = append(r.trusted, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return r, nil
}

func (r Resolver) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IP returns the client IP of req. From a trusted proxy, it is the
// right-most X-Forwarded-For address that isn't itself a trusted proxy
// (addresses further left were written by the client and can be forged).
func (r Resolver) IP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !r.isTrusted(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // a malformed hop: stop at the last good one
		}
		a = a.Unmap()
		if !r.isTrusted(a) {
			return a.String()
		}
		peer = a
	}
	return peer.String()
}

// Middleware puts the request's Client in its context.
func (r Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ua := req.UserAgent()
		if len(ua) > maxUserAgent {
			ua = ua[:maxUserAgent]
		}
		next.ServeHTTP(w, req.WithContext(With(req.Context(), Client{IP: r.IP(req), UserAgent: ua})))
	})
}
