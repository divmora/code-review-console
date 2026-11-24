package admin

import (
	"net/http"
	"os"
	"time"

	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/tuning"
)

// tuningView is the "Queue and review engine" part of Settings. The
// dashboard token is never sent back, only whether one is set.
type tuningView struct {
	tuning.Settings
	DashboardTokenHash string `json:"dashboard_token_hash,omitempty"` // hides the embedded field
	DashboardTokenSet  bool   `json:"dashboard_token_set"`
	// DashboardTokenExpired: the token is set but no longer works.
	DashboardTokenExpired bool   `json:"dashboard_token_expired"`
	MetricsTokenHash      string `json:"metrics_token_hash,omitempty"` // hides the embedded field
	MetricsTokenSet       bool   `json:"metrics_token_set"`
	// ExtraEnv hides the embedded field: always sent, as [] when empty
	// (the stored settings omit it, and the page reads it as a list; #95).
	ExtraEnv []string `json:"extra_env"`
	// EnvSet says which extra variables the console has.
	EnvSet map[string]bool `json:"env_set"`
}

func (a *Admin) tuning() *tuning.Service {
	if a.Tuning == nil {
		a.Tuning = tuning.New(nil, a.store)
	}
	return a.Tuning
}

func viewTuning(t tuning.Settings) tuningView {
	v := tuningView{Settings: t, DashboardTokenSet: t.DashboardTokenHash != "", EnvSet: map[string]bool{}}
	v.Settings.DashboardTokenHash = ""
	v.DashboardAccess = t.Access()
	v.MetricsTokenSet, v.Settings.MetricsTokenHash = t.MetricsTokenHash != "", ""
	v.MetricsAccess = t.Metrics()
	v.DashboardTokenExpired = t.DashboardTokenHash != "" && t.TokenExpired(time.Now())
	v.ExtraEnv = append([]string{}, t.ExtraEnv...)
	for _, n := range t.ExtraEnv {
		_, ok := os.LookupEnv(n)
		v.EnvSet[n] = ok
	}
	return v
}

func (a *Admin) apiGetTuning(w http.ResponseWriter, r *http.Request, s *store.Session) {
	writeJSON(w, http.StatusOK, viewTuning(a.tuning().Get(r.Context())))
}

func (a *Admin) apiPutTuning(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var u tuning.Update
	if !decode(w, r, &u) {
		return
	}
	before := a.legacyDashboard(r.Context())
	t, err := a.tuning().Put(r.Context(), u, a.knownProfiles(r.Context()), s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.syncDefaultDashboard(r.Context(), before, s.Email)
	a.logger.Info("admin saved queue and review engine settings", "by", s.Email, "workers", t.Workers, "queue_size", t.QueueSize,
		"job_timeout", t.JobTimeout, "dashboard_token", t.DashboardTokenHash != "")
	writeJSON(w, http.StatusOK, viewTuning(t))
}
