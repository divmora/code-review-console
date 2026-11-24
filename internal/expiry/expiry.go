// Package expiry warns before something that keeps reviews running
// expires: the Divmora license and the code host tokens. The admin panel
// shows the warnings as a banner, Setup and the host rows; the server
// logs them once a day.
package expiry

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/hosts"
)

// Soon is how far ahead warnings start.
const Soon = 14 * 24 * time.Hour

// Severity of a warning.
const (
	Warn = "warn" // expires within Soon
	Bad  = "bad"  // expired (or the license is in its grace period)
)

// Warning is one thing about to expire, or expired.
type Warning struct {
	Kind     string    `json:"kind"` // license or host
	HostID   int64     `json:"host_id,omitempty"`
	Name     string    `json:"name"`
	Expires  time.Time `json:"expires"`
	Days     int       `json:"days"` // whole days left (negative: days ago)
	Severity string    `json:"severity"`
	Message  string    `json:"message"`
}

// DaysLeft is the number of whole days from now until t (rounded up, so
// "expires tomorrow" is 1); negative once t has passed.
func DaysLeft(t, now time.Time) int {
	d := t.Sub(now).Hours() / 24
	if d >= 0 {
		return int(math.Ceil(d))
	}
	return int(math.Floor(d))
}

// TokenTime is when a token stops working, in UTC with the time, so the
// cut-off is unambiguous ("22 Dec 2026, 00:00 UTC" for a GitLab token
// dated 22 Dec: it stops at the start of that day).
func TokenTime(t time.Time) string {
	return t.UTC().Format("2 Jan 2006, 15:04 UTC")
}

// License returns a warning for a license that expires within Soon (a
// license in its grace period or expired already has its own banner).
func License(st license.Status, now time.Time) (Warning, bool) {
	if st.State != license.Active || st.Expires.IsZero() || st.Expires.Sub(now) > Soon {
		return Warning{}, false
	}
	days := DaysLeft(st.Expires, now)
	return Warning{Kind: "license", Name: "Divmora license", Expires: st.Expires, Days: days, Severity: Warn,
		Message: fmt.Sprintf("The Divmora license expires on %s (%s). After its grace period paid features stop: more hosts, rules, learnings, budgets, the Docker and Kubernetes executors.",
			st.Expires.UTC().Format("2 Jan 2006"), inDays(days))}, true
}

// Host returns a warning for a host whose token expires within Soon or
// has expired.
func Host(h hosts.Host, e hosts.TokenExpiry, now time.Time) (Warning, bool) {
	if !e.Known || e.Expires.IsZero() || e.Expires.Sub(now) > Soon {
		return Warning{}, false
	}
	name := h.Name
	if name == "" {
		name = h.Host
	}
	days := DaysLeft(e.Expires, now)
	w := Warning{Kind: "host", HostID: h.ID, Name: name, Expires: e.Expires, Days: days, Severity: Warn}
	if !now.Before(e.Expires) {
		w.Severity = Bad
		w.Message = fmt.Sprintf("The token of %s expired on %s: reviews and replies on it fail until it is replaced.", name, TokenTime(e.Expires))
		return w, true
	}
	w.Message = fmt.Sprintf("The token of %s expires on %s (%s): reviews and replies on it stop then. Create a new token and paste it into the host.",
		name, TokenTime(e.Expires), inDays(days))
	return w, true
}

// All returns the warnings for the license and every enabled host, the
// most urgent first.
func All(st license.Status, reg *hosts.Registry, now time.Time) []Warning {
	out := []Warning{}
	if w, ok := License(st, now); ok {
		out = append(out, w)
	}
	if reg != nil {
		for _, h := range reg.Enabled("") {
			if e, ok := reg.TokenExpiryOf(h.ID); ok {
				if w, ok := Host(h, e, now); ok {
					out = append(out, w)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Expires.Before(out[j].Expires) })
	return out
}

func inDays(n int) string {
	switch {
	case n <= 0:
		return "today"
	case n == 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", n)
}
