// Package httpx builds HTTP clients for requests that carry credentials.
package httpx

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SameHostRedirects refuses a redirect to another host, or from https to
// http. Go copies custom headers such as GitLab's PRIVATE-TOKEN to any
// redirect target, and Authorization on a downgrade or to a subdomain, so
// a 3xx from a moved instance or a proxy could hand the token elsewhere
// (issue #124). Redirects on the same host still work.
func SameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	from := via[0].URL
	switch {
	case !strings.EqualFold(req.URL.Host, from.Host):
		return fmt.Errorf("refusing a redirect from %s to another host (%s)", from.Host, req.URL.Host)
	case from.Scheme == "https" && req.URL.Scheme != "https":
		return fmt.Errorf("refusing a redirect from https to %s on %s", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// Client is an http.Client for credentialed requests: same-host
// redirects only, and certificate checks skipped only when skipTLSVerify
// (a code host's own opt-in setting).
func Client(timeout time.Duration, skipTLSVerify bool) *http.Client {
	c := &http.Client{Timeout: timeout, CheckRedirect: SameHostRedirects}
	if skipTLSVerify {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // the host's own opt-in setting
	}
	return c
}
