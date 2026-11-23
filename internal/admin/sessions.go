package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/divmora/code-review-console/internal/store"
)

// sessionView is a signed-in admin session as the panel lists it.
type sessionView struct {
	ID      int64     `json:"id"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Current bool      `json:"current"`
	// IP is the latest request's address; UserAgent the browser it signed
	// in with; LastSeen the latest request (to the minute).
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
}

func (a *Admin) apiListSessions(w http.ResponseWriter, r *http.Request, s *store.Session) {
	list, err := a.store.ListSessions(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	out := make([]sessionView, 0, len(list))
	for _, x := range list {
		out = append(out, sessionView{ID: x.ID, Email: x.Email, Name: x.Name, Created: x.Created, Expires: x.Expires, Current: x.ID == s.ID,
			IP: x.IP, UserAgent: x.UserAgent, LastSeen: x.LastSeen})
	}
	writeJSON(w, http.StatusOK, out)
}

// apiDeleteSession signs one session out (an admin's own included).
func (a *Admin) apiDeleteSession(w http.ResponseWriter, r *http.Request, s *store.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	if id != s.ID && s.Role != RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only an admin can sign other people out"})
		return
	}
	gone, err := a.store.DeleteSessionByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session (signed out or expired)"})
		return
	}
	if err != nil {
		a.serverError(w, err)
		return
	}
	_ = a.store.Audit(r.Context(), s.Email, "session.revoke", gone.Email, map[string]any{"session": id, "signed_in": gone.Created})
	a.apiListSessions(w, r, s)
}

// apiEndOtherSessions signs out every session except the caller's.
func (a *Admin) apiEndOtherSessions(w http.ResponseWriter, r *http.Request, s *store.Session) {
	n, err := a.store.DeleteOtherSessions(r.Context(), sessionHash(r))
	if err != nil {
		a.serverError(w, err)
		return
	}
	_ = a.store.Audit(r.Context(), s.Email, "session.revoke_others", "admin", map[string]any{"ended": n})
	a.apiListSessions(w, r, s)
}
