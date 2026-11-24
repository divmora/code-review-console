package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-console/internal/models"
	"github.com/divmora/code-review-console/internal/store"
)

// Roles. Admins change anything. Operators change repositories, rules
// and learnings and run reviews; their other changes (code hosts,
// webhooks, settings, models, the executor, the license) wait for an
// admin's approval. Viewers see every page and change nothing.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// What a route does, for the role check.
type routeClass int

const (
	classRead      routeClass = iota // any role
	classOperate                     // operators and admins
	classSensitive                   // admins; an operator's goes to approval
	classAdminOnly                   // admins only
	classSelf                        // any role; the handler checks it is their own
)

// operateRoutes are the day-to-day changes an operator makes directly.
var operateRoutes = map[string]bool{
	"POST /admin/api/repos":                  true,
	"PUT /admin/api/repos/{id}":              true,
	"DELETE /admin/api/repos/{id}":           true,
	"POST /admin/api/rules":                  true,
	"PUT /admin/api/rules/{id}":              true,
	"DELETE /admin/api/rules/{id}":           true,
	"POST /admin/api/reviews":                true,
	"POST /admin/api/reviews/{id}/rerun":     true,
	"POST /admin/api/learnings":              true,
	"PUT /admin/api/learnings/{id}":          true,
	"DELETE /admin/api/learnings/{id}":       true,
	"POST /admin/api/hosts/{id}/test":        true,
	"POST /admin/api/credentials/{cid}/test": true,
}

var adminOnlyRoutes = map[string]bool{
	"POST /admin/api/sessions/end-others":    true,
	"POST /admin/api/approvals/{id}/approve": true,
	"POST /admin/api/approvals/{id}/reject":  true,
}

var selfRoutes = map[string]bool{
	"DELETE /admin/api/sessions/{id}":       true,
	"POST /admin/api/approvals/{id}/cancel": true,
}

func classOf(r *http.Request) routeClass {
	switch {
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		return classRead
	case operateRoutes[r.Pattern]:
		return classOperate
	case adminOnlyRoutes[r.Pattern]:
		return classAdminOnly
	case selfRoutes[r.Pattern]:
		return classSelf
	}
	return classSensitive
}

// allowed reports whether role may call a route of class c directly, and
// whether an operator's call should be queued for approval instead.
func allowed(role string, c routeClass) (ok, queue bool) {
	switch {
	case c == classRead || c == classSelf:
		return true, false
	case role == RoleAdmin:
		return true, false
	case role == RoleOperator && c == classOperate:
		return true, false
	case role == RoleOperator && c == classSensitive:
		return false, true
	}
	return false, false
}

type replayKey struct{}

// replaySession is the session an approved change runs as.
func replaySession(r *http.Request) (*store.Session, bool) {
	s, ok := r.Context().Value(replayKey{}).(*store.Session)
	return s, ok
}

// approvalTTL is how long a request waits before it expires.
const approvalTTL = 7 * 24 * time.Hour

// applyingTTL is how long a request may stay applying: longer means the
// console stopped while replaying it, and it's marked failed.
var applyingTTL = 10 * time.Minute

// secretKeys are request fields masked in approval previews.
var secretKeys = map[string]bool{
	"token": true, "api_token": true, "app_private_key": true, "private_key": true, "webhook_secrets": true,
	"signing_tokens": true, "signing_token": true, "secret": true, "secrets": true, "key": true, "dashboard_token": true,
	"metrics_token": true, "client_secret": true, "password": true,
}

// mask replaces secret values with "(set)" (or "(N set)" for lists).
func mask(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			if secretKeys[k] {
				switch s := x.(type) {
				case string:
					if s != "" {
						x = "(set)"
					}
				case []any:
					if len(s) > 0 {
						x = "(" + strconv.Itoa(len(s)) + " set)"
					}
				default:
					if x != nil {
						x = "(set)"
					}
				}
				out[k] = x
				continue
			}
			out[k] = mask(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = mask(x)
		}
		return out
	}
	return v
}

// modelKeyNote says where a model profile change would send its key
// when that isn't the provider's own host, for the admin who approves it
// (the preview shows only the variable's name).
func modelKeyNote(body []byte) string {
	var req modelReq
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	if env, host, unusual := models.KeyTarget(req.Config); unusual {
		return " (sends " + env + " to " + host + ")"
	}
	return ""
}

// summarize says in a few words what a request changes.
func (a *Admin) summarize(r *http.Request) string {
	host := func() string {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if a.Hosts != nil {
			if h, ok := a.Hosts.Get(id); ok {
				return firstNonEmpty(h.Name, h.Host)
			}
		}
		return "#" + r.PathValue("id")
	}
	switch r.Pattern {
	case "POST /admin/api/hosts":
		return "Add a code host"
	case "PUT /admin/api/hosts/{id}":
		return "Change code host " + host()
	case "DELETE /admin/api/hosts/{id}":
		return "Delete code host " + host()
	case "POST /admin/api/hosts/{id}/webhooks":
		return "Add a webhook to " + host()
	case "PUT /admin/api/webhooks/{wid}":
		return "Change webhook " + r.PathValue("wid")
	case "DELETE /admin/api/webhooks/{wid}":
		return "Delete webhook " + r.PathValue("wid")
	case "POST /admin/api/hosts/{id}/credentials":
		return "Add a credential to " + host()
	case "PUT /admin/api/credentials/{cid}":
		return "Change credential " + a.credentialSummary(r)
	case "DELETE /admin/api/credentials/{cid}":
		return "Delete credential " + a.credentialSummary(r)
	case "PUT /admin/api/settings":
		return "Change settings"
	case "PUT /admin/api/tuning":
		return "Change queue, review engine, dashboard or metrics settings"
	case "PUT /admin/api/models/{name}":
		return "Change model profile " + r.PathValue("name")
	case "DELETE /admin/api/models/{name}":
		return "Delete model profile " + r.PathValue("name")
	case "PUT /admin/api/model-choice":
		return "Change the default model"
	case "PUT /admin/api/executor":
		return "Change executor settings"
	case "PUT /admin/api/license":
		return "Replace the license"
	}
	return r.Method + " " + r.URL.Path
}

// queueChange stores an operator's sensitive change for an admin to
// approve, encrypted, and answers 202.
func (a *Admin) queueChange(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if a.Box == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "changes that need approval need CODEREVIEW_CONSOLE_SECRET_KEY"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the request"})
		return
	}
	preview := json.RawMessage("{}")
	if len(bytes.TrimSpace(body)) > 0 {
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		preview, _ = json.Marshal(mask(v))
	}
	sealed := ""
	if len(body) > 0 {
		if sealed, err = a.Box.Seal(body); err != nil {
			a.serverError(w, err)
			return
		}
	}
	summary := a.summarize(r)
	if r.Pattern == "PUT /admin/api/models/{name}" {
		summary += modelKeyNote(body)
	}
	c, err := a.store.CreateChangeRequest(r.Context(), store.ChangeRequest{Method: r.Method, Path: r.URL.RequestURI(), Summary: summary,
		Preview: preview, Body: sealed, RequestedBy: s.Email})
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.logger.Info("operator change waits for approval", "by", s.Email, "change", c.Summary, "id", c.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"pending": true, "id": c.ID,
		"message": "Sent to an admin for approval: " + c.Summary + ". It applies once approved (Change requests)."})
}

// apiListApprovals lists change requests, newest first: pending ones
// (?status=pending) or all. Requests older than approvalTTL expire.
func (a *Admin) apiListApprovals(w http.ResponseWriter, r *http.Request, s *store.Session) {
	list, err := a.store.ListChangeRequests(r.Context(), r.URL.Query().Get("status"), 200)
	if err != nil {
		a.serverError(w, err)
		return
	}
	for i := range list {
		if list[i].Status == store.ChangeApplying && time.Since(list[i].DecidedAt) > applyingTTL {
			if c, err := a.store.FinishChangeRequest(r.Context(), list[i].ID, store.ChangeFailed, list[i].DecidedBy, 0,
				"interrupted: the console stopped while applying it; check whether the change took effect"); err == nil {
				list[i] = *c
			}
		}
		if list[i].Status == store.ChangePending && time.Since(list[i].RequestedAt) > approvalTTL {
			if c, err := a.store.DecideChangeRequest(r.Context(), list[i].ID, store.ChangeExpired, "console", 0, "not approved within 7 days"); err == nil {
				list[i] = *c
			}
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *Admin) changeByID(w http.ResponseWriter, r *http.Request) (*store.ChangeRequest, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return nil, false
	}
	c, err := a.store.GetChangeRequest(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "change request not found"})
		return nil, false
	} else if err != nil {
		a.serverError(w, err)
		return nil, false
	}
	if c.Status != store.ChangePending {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this request is already " + c.Status})
		return nil, false
	}
	if time.Since(c.RequestedAt) > approvalTTL {
		_, _ = a.store.DecideChangeRequest(r.Context(), c.ID, store.ChangeExpired, "console", 0, "not approved within 7 days")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this request expired: ask for the change again"})
		return nil, false
	}
	return c, true
}

// notPending answers a decision on a request that stopped being pending
// meanwhile (another admin approved it, its operator cancelled it): 409
// with what it is now.
func (a *Admin) notPending(w http.ResponseWriter, r *http.Request, c *store.ChangeRequest, err error) {
	if !errors.Is(err, store.ErrNotFound) || c == nil {
		a.serverError(w, err)
		return
	}
	status := "no longer pending"
	if now, err := a.store.GetChangeRequest(r.Context(), c.ID); err == nil {
		status = "already " + now.Status
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": "this request is " + status})
}

// recorder captures the response of an approved change.
type recorder struct {
	h    http.Header
	code int
	body bytes.Buffer
}

func (r *recorder) Header() http.Header {
	if r.h == nil {
		r.h = http.Header{}
	}
	return r.h
}
func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

// apiApprove applies a change request as its requester, approved by the
// admin: the change runs through the same handler, with the same checks,
// and its audit entry names both. The response (which may show a new
// webhook secret once) goes to the approving admin.
func (a *Admin) apiApprove(w http.ResponseWriter, r *http.Request, s *store.Session) {
	c, ok := a.changeByID(w, r)
	if !ok {
		return
	}
	// Claim it first: only one approval replays it, and a cancel or
	// rejection can't land while it's being applied (issue #126).
	claimed, err := a.store.ClaimChangeRequest(r.Context(), c.ID, s.Email)
	if err != nil {
		a.notPending(w, r, c, err)
		return
	}
	c = claimed
	var body []byte
	if c.Body != "" {
		if body, err = a.Box.Open(c.Body); err != nil {
			_, _ = a.store.FinishChangeRequest(r.Context(), c.ID, store.ChangeFailed, s.Email, 0, "could not decrypt the request (was the secret key changed?)")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not decrypt the request (was the secret key changed?)"})
			return
		}
	}
	as := &store.Session{ID: s.ID, Subject: s.Subject, Email: c.RequestedBy + " (approved by " + s.Email + ")", Name: s.Name, CSRF: s.CSRF, Role: RoleAdmin}
	req, err := http.NewRequestWithContext(context.WithValue(r.Context(), replayKey{}, as), c.Method, c.Path, bytes.NewReader(body))
	if err != nil {
		_, _ = a.store.FinishChangeRequest(r.Context(), c.ID, store.ChangeFailed, s.Email, 0, err.Error())
		a.serverError(w, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	rec := &recorder{}
	a.mux.ServeHTTP(rec, req)
	status, result := store.ChangeApplied, "applied ("+strconv.Itoa(rec.code)+")"
	if rec.code < 200 || rec.code > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &e)
		status, result = store.ChangeFailed, firstNonEmpty(e.Error, "HTTP "+strconv.Itoa(rec.code))
	}
	decided, err := a.store.FinishChangeRequest(context.WithoutCancel(r.Context()), c.ID, status, s.Email, rec.code, result)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.logger.Info("admin decided a change request", "by", s.Email, "id", c.ID, "status", status, "change", c.Summary)
	writeJSON(w, http.StatusOK, map[string]any{"request": decided, "response": json.RawMessage(nonEmptyJSON(rec.body.Bytes()))})
}

func nonEmptyJSON(b []byte) []byte {
	if len(bytes.TrimSpace(b)) == 0 || !json.Valid(b) {
		return []byte("null")
	}
	return b
}

func (a *Admin) apiReject(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	c, ok := a.changeByID(w, r)
	if !ok {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	decided, err := a.store.DecideChangeRequest(r.Context(), c.ID, store.ChangeRejected, s.Email, 0, firstNonEmpty(reason, "rejected"))
	if err != nil {
		a.notPending(w, r, c, err)
		return
	}
	writeJSON(w, http.StatusOK, decided)
}

// apiCancel withdraws a request: its requester's own, or any for admins.
func (a *Admin) apiCancel(w http.ResponseWriter, r *http.Request, s *store.Session) {
	c, ok := a.changeByID(w, r)
	if !ok {
		return
	}
	if c.RequestedBy != s.Email && s.Role != RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the person who asked for a change, or an admin, can cancel it"})
		return
	}
	decided, err := a.store.DecideChangeRequest(r.Context(), c.ID, store.ChangeCancelled, s.Email, 0, "cancelled")
	if err != nil {
		a.notPending(w, r, c, err)
		return
	}
	writeJSON(w, http.StatusOK, decided)
}

// pendingFor is how many requests wait: all of them for an admin, their
// own for an operator.
func (a *Admin) pendingFor(ctx context.Context, s *store.Session) int {
	if s.Role == RoleViewer {
		return 0
	}
	list, err := a.store.ListChangeRequests(ctx, store.ChangePending, 200)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range list {
		if time.Since(c.RequestedAt) <= approvalTTL && (s.Role == RoleAdmin || c.RequestedBy == s.Email) {
			n++
		}
	}
	return n
}
