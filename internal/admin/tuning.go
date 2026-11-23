package admin

import (
	"net/http"
	"os"

	"github.com/divmora/code-review-console/internal/store"
	"github.com/divmora/code-review-console/internal/tuning"
)

// tuningView is the "Queue and review engine" part of Settings. The
// dashboard token is never sent back, only whether one is set.
type tuningView struct {
	tuning.Settings
	DashboardTokenHash string `json:"dashboard_token_hash,omitempty"` // hides the embedded field
	DashboardTokenSet  bool   `json:"dashboard_token_set"`
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
	if v.ExtraEnv == nil {
		v.ExtraEnv = []string{}
	}
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
	t, err := a.tuning().Put(r.Context(), u, a.knownProfiles(r.Context()), s.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.logger.Info("admin saved queue and review engine settings", "by", s.Email, "workers", t.Workers, "queue_size", t.QueueSize,
		"job_timeout", t.JobTimeout, "dashboard_token", t.DashboardTokenHash != "")
	writeJSON(w, http.StatusOK, viewTuning(t))
}
