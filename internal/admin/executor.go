package admin

import (
	"net/http"
	"strings"

	"github.com/divmora/code-review-console/internal/config"
	"github.com/divmora/code-review-console/internal/ee/license"
	"github.com/divmora/code-review-console/internal/execsettings"
	"github.com/divmora/code-review-console/internal/store"
)

// executorView is the Executor page: the settings the panel changes, plus
// the config file's settings it shows but can't change.
type executorView struct {
	Type     string `json:"type"`
	Editable bool   `json:"editable"`
	// Source is "console" once settings are saved in the database,
	// otherwise "config".
	Source    string `json:"source"`
	ImageRepo string `json:"image_repo,omitempty"`
	// Image is the image reviews run with now.
	Image         string                `json:"image,omitempty"`
	Settings      execsettings.Settings `json:"settings"`
	Fixed         map[string]string     `json:"fixed"`
	MinCLIVersion string                `json:"min_cli_version"`
	Licensed      bool                  `json:"licensed"`
	// Tags are the versions found in the image repository, newest first;
	// TagsError says why they couldn't be listed (the page then falls back
	// to typing the version).
	Tags      []string `json:"tags"`
	TagsError string   `json:"tags_error,omitempty"`
}

func (a *Admin) executorView(r *http.Request) (executorView, error) {
	v := executorView{Type: a.ExecutorType, Fixed: map[string]string{}, MinCLIVersion: config.MinCLIVersion, Source: "config",
		Licensed: a.ExecutorType == "local" || a.Lic.Has(license.Executors)}
	if a.Executor == nil {
		return v, nil
	}
	st, found, err := a.Executor.Get(r.Context())
	if err != nil {
		return v, err
	}
	e := a.Executor.Config()
	v.Type, v.Editable, v.Settings = e.Type, a.Executor.Editable(), st
	if found {
		v.Source = "console"
	}
	eff := execsettings.Apply(e, st)
	switch e.Type {
	case "local":
		v.Fixed["binary"] = e.Local.Binary
	case "docker":
		v.ImageRepo, _ = execsettings.SplitImage(e.Docker.Image)
		v.Image = eff.Docker.Image
		v.Fixed["binary"] = e.Docker.Binary
		v.Fixed["network"] = firstNonEmpty(e.Docker.Network, "bridge")
	case "kubernetes":
		k := e.Kubernetes
		v.ImageRepo, _ = execsettings.SplitImage(k.Image)
		v.Image = eff.Kubernetes.Image
		v.Fixed["namespace"] = k.Namespace
		v.Fixed["service_account"] = firstNonEmpty(k.ServiceAccount, "default")
		v.Fixed["automount_service_account_token"] = map[bool]string{true: "yes", false: "no"}[k.AutomountServiceAccountToken]
		v.Fixed["cluster"] = map[bool]string{true: "kubeconfig file", false: "in-cluster"}[k.Kubeconfig != ""]
	}
	if v.Editable && v.ImageRepo != "" {
		if a.Tags == nil {
			a.Tags = execsettings.NewTags()
		}
		tags, err := a.Tags.List(r.Context(), v.ImageRepo)
		v.Tags = tags
		if err != nil {
			v.TagsError = err.Error()
		}
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	if len(a.Executor.Config().Env) > 0 {
		v.Fixed["env"] = strings.Join(a.Executor.Config().Env, ", ")
	}
	return v, nil
}

func (a *Admin) apiGetExecutor(w http.ResponseWriter, r *http.Request, s *store.Session) {
	v, err := a.executorView(r)
	if err != nil {
		a.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *Admin) apiPutExecutor(w http.ResponseWriter, r *http.Request, s *store.Session) {
	if a.Executor == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "executor settings are not available"})
		return
	}
	var req execsettings.Settings
	if !decode(w, r, &req) {
		return
	}
	if err := a.Executor.Put(r.Context(), req, s.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.apiGetExecutor(w, r, s)
}
