// Copyright (c) DIVMORA Technologies. All rights reserved.
// Licensed under the Divmora Enterprise License; see LICENSE-ENTERPRISE.
// Use requires a valid Divmora license. Do not modify or remove license checks.

package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/store"
)

// LicenseSetting is the settings key that holds a key pasted in the panel.
const LicenseSetting = "license"

// StoredLicense is the value under LicenseSetting.
type StoredLicense struct {
	Key string `json:"key"`
}

// require writes 402 and returns false unless the license has f.
func (a *Admin) require(w http.ResponseWriter, f license.Feature) bool {
	if err := a.Lic.Require(f); err != nil {
		writeLicenseError(w, err)
		return false
	}
	return true
}

// writeLicenseError writes 402 Payment Required for a *license.Error, or
// 400 for any other error.
func writeLicenseError(w http.ResponseWriter, err error) {
	var le *license.Error
	if errors.As(err, &le) {
		writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": le.Error(), "feature": string(le.Feature)})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func (a *Admin) apiGetLicense(w http.ResponseWriter, r *http.Request, s *store.Session) {
	writeJSON(w, http.StatusOK, a.licenseView())
}

func (a *Admin) licenseView() map[string]any {
	type feat struct {
		ID    license.Feature `json:"id"`
		Title string          `json:"title"`
	}
	all := make([]feat, 0, len(license.All))
	for _, f := range license.All {
		all = append(all, feat{f, license.Titles[f]})
	}
	return map[string]any{"status": a.Lic.Status(), "features": all, "free_retention_days": license.FreeRetentionDays}
}

// apiPutLicense installs (or, with an empty key, removes) a key pasted in
// the panel. Keys from the environment or a file cannot be replaced here.
func (a *Admin) apiPutLicense(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if a.Lic == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "licensing is not available"})
		return
	}
	if a.Lic.Status().Managed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the license comes from CODEREVIEW_CONSOLE_LICENSE or license.file; change it there"})
		return
	}
	var req StoredLicense
	if !decode(w, r, &req) {
		return
	}
	key := strings.Join(strings.Fields(req.Key), "")
	detail := map[string]any{"removed": true}
	if key != "" {
		c, err := a.Lic.Verify(key)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		detail = map[string]any{"id": c.ID, "customer": c.Customer.Name, "plan": c.Plan, "expires_at": c.ExpiresAt}
	}
	if err := a.store.PutSettingAudited(r.Context(), LicenseSetting, StoredLicense{Key: key}, detail, s.Email); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the license"})
		return
	}
	if err := a.Lic.Install(key, "database"); err != nil { // verified above
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin installed license", "by", s.Email, "license", detail)
	writeJSON(w, http.StatusOK, a.licenseView())
}
