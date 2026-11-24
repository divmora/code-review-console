package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/divmora/code-review-bot/pkg/vcs"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/expiry"
	"github.com/divmora/code-review-console/internal/hosts"
	"github.com/divmora/code-review-console/internal/store"
)

// credentialView is a host credential as the admin UI sees it (issue #136):
// never its token, only whether it is set.
type credentialView struct {
	ID        int64                    `json:"id"`
	HostID    int64                    `json:"host_id"`
	Name      string                   `json:"name"`
	Pattern   string                   `json:"pattern"`
	Enabled   bool                     `json:"enabled"`
	Settings  hosts.CredentialSettings `json:"settings"`
	Secrets   hosts.CredentialStatus   `json:"secrets"`
	CreatedAt time.Time                `json:"created_at"`
	CreatedBy string                   `json:"created_by"`
	UpdatedAt time.Time                `json:"updated_at"`
	UpdatedBy string                   `json:"updated_by"`
	// TokenExpiry and Check are as for the host; Mention is the handle
	// @bot commands use in the repositories it covers.
	TokenExpiry *hosts.TokenExpiry `json:"token_expiry,omitempty"`
	Check       *hosts.Check       `json:"check,omitempty"`
	Mention     string             `json:"mention,omitempty"`
}

func (a *Admin) viewCredential(ctx context.Context, h hosts.Host, c hosts.Credential) credentialView {
	v := credentialView{ID: c.ID, HostID: c.HostID, Name: c.Name, Pattern: c.Pattern, Enabled: c.Enabled, Settings: c.Settings,
		Secrets: c.SecretStatus(), CreatedAt: c.CreatedAt, CreatedBy: c.CreatedBy, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy}
	if e, ok := a.Hosts.CredentialExpiryOf(c.ID); ok {
		v.TokenExpiry = &e
	}
	if ch, ok := a.Hosts.CredentialCheckOf(c.ID); ok {
		v.Check = &ch
	}
	user := ""
	if v.Check != nil {
		user = v.Check.User
	}
	v.Mention = firstNonEmpty(c.Settings.BotName, h.BotName, a.tuning().Get(ctx).BotName, user)
	return v
}

type credentialReq struct {
	Name     string                   `json:"name"`
	Pattern  string                   `json:"pattern"`
	Enabled  *bool                    `json:"enabled"`
	Settings hosts.CredentialSettings `json:"settings"`
	Token    *string                  `json:"token"`
	APIToken *string                  `json:"api_token"`
}

func (req credentialReq) apply(c *hosts.Credential) {
	c.Name, c.Pattern, c.Settings = req.Name, req.Pattern, req.Settings
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	if req.Token != nil {
		c.Token = *req.Token
	}
	if req.APIToken != nil {
		c.APIToken = *req.APIToken
	}
}

func (a *Admin) apiCreateCredential(w http.ResponseWriter, r *http.Request, s *store.Session) {
	h, ok := a.hostByID(w, r)
	if !ok {
		return
	}
	if !a.require(w, license.Hosts) {
		return
	}
	var req credentialReq
	if !decode(w, r, &req) {
		return
	}
	c := hosts.Credential{HostID: h.ID, Enabled: true}
	req.apply(&c)
	a.saveCredential(w, r, s, h, c, http.StatusCreated)
}

func (a *Admin) credentialByID(w http.ResponseWriter, r *http.Request) (hosts.Credential, hosts.Host, bool) {
	id, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil || a.Hosts == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential not found"})
		return hosts.Credential{}, hosts.Host{}, false
	}
	c, ok := a.Hosts.Credential(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential not found"})
		return hosts.Credential{}, hosts.Host{}, false
	}
	h, ok := a.Hosts.Get(c.HostID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential not found"})
		return hosts.Credential{}, hosts.Host{}, false
	}
	return c, h, true
}

func (a *Admin) apiUpdateCredential(w http.ResponseWriter, r *http.Request, s *store.Session) {
	c, h, ok := a.credentialByID(w, r)
	if !ok {
		return
	}
	if !a.require(w, license.Hosts) {
		return
	}
	var req credentialReq
	if !decode(w, r, &req) {
		return
	}
	req.apply(&c)
	a.saveCredential(w, r, s, h, c, http.StatusOK)
}

func (a *Admin) saveCredential(w http.ResponseWriter, r *http.Request, s *store.Session, h hosts.Host, c hosts.Credential, status int) {
	saved, err := a.Hosts.SaveCredential(r.Context(), c, s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin saved credential", "by", s.Email, "host", h.Host, "credential", saved.ID, "pattern", saved.Pattern, "enabled", saved.Enabled)
	if saved.Enabled && h.Enabled {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		a.Hosts.CheckTokenExpiry(ctx, h.WithCredential(saved))
		cancel()
	}
	writeJSON(w, status, a.viewCredential(r.Context(), h, saved))
}

func (a *Admin) apiDeleteCredential(w http.ResponseWriter, r *http.Request, s *store.Session) {
	c, h, ok := a.credentialByID(w, r)
	if !ok {
		return
	}
	if err := a.Hosts.DeleteCredential(r.Context(), c.ID, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin deleted credential", "by", s.Email, "host", h.Host, "credential", c.ID, "pattern", c.Pattern)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// apiTestCredential checks a credential the way Test checks a host.
func (a *Admin) apiTestCredential(w http.ResponseWriter, r *http.Request, s *store.Session) {
	c, h, ok := a.credentialByID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, user, e := a.checkToken(ctx, h.WithCredential(c))
	a.Hosts.RecordCredentialCheck(c.ID, res)
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK, "user": user, "summary": res.Summary, "message": res.Message, "at": res.At, "token_expiry": e})
}

// checkToken asks the code host who h's token (the host's own or a
// credential's) belongs to, and when it expires.
func (a *Admin) checkToken(ctx context.Context, h hosts.Host) (hosts.Check, string, *hosts.TokenExpiry) {
	var c interface {
		CurrentUser(context.Context) (string, error)
	}
	switch h.Platform {
	case hosts.GitHub:
		c = vcs.NewGitHub(h.BaseURL, vcs.StaticToken(h.Token))
	case hosts.GitLab:
		c = vcs.NewGitLab(h.BaseURL, h.Token, h.SkipTLSVerify)
	case hosts.Bitbucket:
		c = vcs.NewBitbucket(h.BaseURL, vcs.BitbucketAuth{Token: h.Token, Email: h.Email, APIToken: h.APIToken})
	default:
		return hosts.Check{Summary: "Unsupported platform", Message: "unsupported platform " + h.Platform, At: time.Now(), Source: "test"}, "", nil
	}
	user, err := c.CurrentUser(ctx)
	if err == nil && user == "" {
		err = errors.New("the code host returned no user for the token")
	}
	if err != nil {
		msg := err.Error()
		if h.Platform == hosts.Bitbucket && h.Token != "" {
			msg += " (access tokens have no user: set the reviewer name)"
		}
		return hosts.Check{OK: false, Summary: hosts.Summarize(err), Message: msg, At: time.Now(), Source: "test"}, "", nil
	}
	msg := "connected as @" + user
	e := a.Hosts.CheckTokenExpiry(ctx, h)
	switch {
	case e.Known && e.Expires.IsZero():
		msg += "; the token never expires"
	case e.Known:
		msg += "; the token expires on " + expiry.TokenTime(e.Expires)
		if d := expiry.DaysLeft(e.Expires, time.Now()); d <= 14 {
			msg += " (" + strconv.Itoa(d) + " days left: replace it soon)"
		}
	}
	return hosts.Check{OK: true, Summary: "OK as @" + user, Message: msg, At: time.Now(), User: user, Source: "test"}, user, &e
}

// credentialOf names the credential a repository uses (nil: the host's own).
type repoCredential struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

func (a *Admin) credentialOf(r store.Repo) *repoCredential {
	if a.Hosts == nil {
		return nil
	}
	h, ok := a.Hosts.Find(r.Platform, r.Host)
	if !ok {
		return nil
	}
	c, ok := a.Hosts.CredentialFor(h, r.Path)
	if !ok {
		return nil
	}
	return &repoCredential{ID: c.ID, Name: firstNonEmpty(c.Name, c.Pattern), Pattern: c.Pattern}
}

// credentialSummary is the change-request summary for credential routes.
func (a *Admin) credentialSummary(r *http.Request) string {
	id, _ := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	name := "#" + r.PathValue("cid")
	if a.Hosts != nil {
		if c, ok := a.Hosts.Credential(id); ok {
			name = firstNonEmpty(c.Name, c.Pattern)
		}
	}
	return strings.TrimSpace(name)
}
