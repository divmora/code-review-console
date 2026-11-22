package admin

import (
	"errors"
	"net/http"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/models"
	"github.com/divmora/code-review-console/internal/store"
)

// profileNames lists every model profile for the repository form.
func (a *Admin) profileNames(r *http.Request) []string {
	names, err := a.models().Names(r.Context())
	if err != nil {
		a.logger.Warn("model profiles", "error", err)
		return []string{}
	}
	return names
}

func (a *Admin) apiListModels(w http.ResponseWriter, r *http.Request, s *store.Session) {
	v, err := a.models().List(r.Context())
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type modelReq struct {
	Config config.ModelProfile `json:"config"`
}

func (a *Admin) apiPutModel(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req modelReq
	if !decode(w, r, &req) {
		return
	}
	if err := a.models().Put(r.Context(), r.PathValue("name"), req.Config, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.apiListModels(w, r, s)
}

func (a *Admin) apiDeleteModel(w http.ResponseWriter, r *http.Request, s *store.Session) {
	err := a.models().Delete(r.Context(), r.PathValue("name"), s.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such model profile in the console"})
	case errors.Is(err, models.ErrInUse):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		a.serverError(w, err)
	default:
		a.apiListModels(w, r, s)
	}
}

func (a *Admin) apiPutModelChoice(w http.ResponseWriter, r *http.Request, s *store.Session) {
	var req models.Choice
	if !decode(w, r, &req) {
		return
	}
	if err := a.models().SetChoice(r.Context(), req, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.apiListModels(w, r, s)
}
