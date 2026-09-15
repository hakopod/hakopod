package api

import (
	"errors"
	"net/http"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// Conversion is read-only. The generated draft must still pass the ordinary
// planner and optimistic revision check before it can be deployed.
func (s *Server) convertCompose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project          string            `json:"project"`
		Environment      string            `json:"environment"`
		Name             string            `json:"name"`
		YAML             string            `json:"yaml"`
		Variables        map[string]string `json:"variables"`
		ApplicationID    string            `json:"application_id"`
		ExpectedRevision *int64            `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validScope(in.Project, in.Environment) {
		problem(w, 400, "missing_context", "Choose a project and environment")
		return
	}
	var base *spec.Application
	var revision int64
	if in.ApplicationID != "" {
		current, ok := s.authorizedApp(w, r, in.ApplicationID, "deployments:write")
		if !ok {
			return
		}
		if current.Project != in.Project || current.Environment != in.Environment {
			failure(w, store.ErrForbidden)
			return
		}
		if in.ExpectedRevision == nil || *in.ExpectedRevision != current.Revision {
			problem(w, 409, "stale_revision", "The application changed. Reload it before adding services.")
			return
		}
		base = &current.Spec
		revision = current.Revision
		in.Name = current.Name
	}
	result, err := spec.ImportCompose([]byte(in.YAML), in.Name, in.Variables, base)
	if err != nil {
		problem(w, 400, "invalid_compose", err.Error())
		return
	}
	if !who(r).Allows("deployments:write", in.Project, in.Environment, result.Spec.Name) {
		failure(w, store.ErrForbidden)
		return
	}
	if base == nil {
		_, err = s.Store.FindApplication(r.Context(), in.Project, in.Environment, result.Spec.Name)
		if err == nil {
			problem(w, 409, "application_exists", "This application already exists. Open its configuration to add services from Compose.")
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			failure(w, err)
			return
		}
	}
	write(w, 200, map[string]any{"spec": result.Spec, "toml": result.TOML, "warnings": result.Warnings, "expected_revision": revision, "application_id": in.ApplicationID})
}
