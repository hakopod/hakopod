package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) prepareBuildSpec(ctx context.Context, c buildConfig, run buildRun) (spec.Application, *store.Application, error) {
	if run.Status != "completed" || run.Conclusion != "success" || run.Image == "" || run.ConfigRevision != c.Revision {
		return spec.Application{}, nil, fmt.Errorf("%w: a verified successful image from the current build configuration is required", store.ErrConflict)
	}
	app, err := s.Store.FindApplication(ctx, c.Project, c.Environment, c.Name)
	var next spec.Application
	var current *store.Application
	if errors.Is(err, pgx.ErrNoRows) {
		if c.ApplicationID != "" {
			return next, nil, err
		}
		next = spec.Application{Name: c.Name, Services: map[string]spec.Service{c.Service: {Image: run.Image, Port: int32(c.Port), Public: c.Public, Size: c.Size, RegistryCredential: c.RegistryCredential, Architecture: c.Architecture}}}
	} else if err != nil {
		return next, nil, err
	} else {
		current = &app
		if c.ApplicationID != "" && c.ApplicationID != app.ID {
			return next, current, store.ErrForbidden
		}
		next, err = spec.Normalize(app.Spec)
		if err != nil {
			return next, current, err
		}
		service, exists := next.Services[c.Service]
		if !exists {
			return next, current, fmt.Errorf("%w: the configured service no longer exists", store.ErrConflict)
		}
		var resolved *spec.Application
		if err = s.Store.Pool.QueryRow(ctx, "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2", app.ID, app.Revision).Scan(&resolved); err != nil {
			return next, current, err
		}
		if resolved == nil {
			return next, current, fmt.Errorf("%w: wait for the current release to resolve before replacing one service image", store.ErrConflict)
		}
		for name, unchanged := range next.Services {
			if name == c.Service {
				continue
			}
			prior, ok := resolved.Services[name]
			if !ok || !strings.Contains(prior.Image, "@sha256:") {
				return next, current, fmt.Errorf("%w: an unaffected service has no preserved immutable image", store.ErrConflict)
			}
			unchanged.Image = prior.Image
			next.Services[name] = unchanged
		}
		service.Image = run.Image
		service.Architecture = c.Architecture
		if c.RegistryCredential != "" {
			service.RegistryCredential = c.RegistryCredential
		}
		next.Services[c.Service] = service
	}
	next, err = spec.Normalize(next)
	return next, current, err
}
func (s *Server) planBuildRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	run, err := s.readBuildRun(r.Context(), c.ID, r.PathValue("run"))
	if err != nil {
		authFailure(w, err)
		return
	}
	next, current, err := s.prepareBuildSpec(r.Context(), c, run)
	if err != nil {
		authFailure(w, err)
		return
	}
	if !s.validateDeliveryPlan(w, r, c.Project, c.Environment, next, current) {
		return
	}
	var old *spec.Application
	var revision int64
	id := ""
	if current != nil {
		old = &current.Spec
		revision = current.Revision
		id = current.ID
	}
	warnings := spec.Warnings(next)
	if warnings == nil {
		warnings = []string{}
	}
	write(w, 200, map[string]any{"application_id": id, "expected_revision": revision, "expected_config_revision": c.Revision, "spec": next, "changes": spec.Diff(old, next), "warnings": warnings, "resource_profiles": spec.Profiles})
}
func (s *Server) finishBuildDeployment(ctx context.Context, c buildConfig, run buildRun, d store.Deployment) (store.Deployment, error) {
	_, err := s.Store.Pool.Exec(ctx, "UPDATE build_configs SET application_id=$2 WHERE id=$1 AND (application_id IS NULL OR application_id=$2)", c.ID, d.ApplicationID)
	if err == nil {
		_, err = s.Store.Pool.Exec(ctx, "UPDATE build_runs SET deployment_id=$2,updated_at=now() WHERE id=$1", run.ID, d.ID)
	}
	return d, err
}
func (s *Server) acceptBuiltImage(ctx context.Context, p store.Principal, c buildConfig, run buildRun, expected int64) (store.Deployment, error) {
	if !p.Allows("deployments:write", c.Project, c.Environment, c.Name) {
		return store.Deployment{}, store.ErrForbidden
	}
	idem := "build-deploy-" + run.ID + "-" + strconv.FormatInt(expected, 10)
	if run.Automatic {
		// Recovery may observe a newer application revision after acceptance but
		// before the build run was linked. One remote run still gets one release.
		idem = "build-deploy-" + run.ID
	}
	var previousID string
	err := s.Store.Pool.QueryRow(ctx, "SELECT id FROM deployments WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&previousID)
	if err == nil {
		d, err := s.Store.Deployment(ctx, previousID)
		if err != nil {
			return d, err
		}
		return s.finishBuildDeployment(ctx, c, run, d)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.Deployment{}, err
	}
	next, _, err := s.prepareBuildSpec(ctx, c, run)
	if err != nil {
		return store.Deployment{}, err
	}
	d, err := s.Store.Accept(ctx, p, c.Project, c.Environment, next, expected, idem)
	if err != nil {
		return d, err
	}
	return s.finishBuildDeployment(ctx, c, run, d)
}
func (s *Server) deployBuildRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision       *int64 `json:"expected_revision"`
		ExpectedConfigRevision *int64 `json:"expected_config_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || in.ExpectedConfigRevision == nil || *in.ExpectedConfigRevision != c.Revision {
		problem(w, 409, "review_required", "review the application and current build configuration revisions before deployment")
		return
	}
	run, err := s.readBuildRun(r.Context(), c.ID, r.PathValue("run"))
	if err != nil {
		authFailure(w, err)
		return
	}
	if run.ConfigRevision != c.Revision {
		authFailure(w, store.ErrConflict)
		return
	}
	d, err := s.acceptBuiltImage(r.Context(), who(r), c, run, *in.ExpectedRevision)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, d)
}
