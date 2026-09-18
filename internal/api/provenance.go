package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type buildProvenance struct {
	BuildID    string    `json:"build_id"`
	RunID      string    `json:"run_id"`
	CommitSHA  string    `json:"commit_sha"`
	Image      string    `json:"image"`
	Provider   string    `json:"provider"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
	RunURL     string    `json:"run_url"`
	CreatedAt  time.Time `json:"created_at"`
}
type serviceProvenance struct {
	CommitSHA       string            `json:"commit_sha,omitempty"`
	ConfiguredImage string            `json:"configured_image"`
	AcceptedImage   string            `json:"accepted_image,omitempty"`
	SourceStatus    string            `json:"source_status"`
	Builds          []buildProvenance `json:"builds"`
}

func (s *Server) applicationProvenance(w http.ResponseWriter, r *http.Request) {
	app, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	var resolved *spec.Application
	err := s.Store.Pool.QueryRow(r.Context(), "SELECT resolved_spec FROM deployments WHERE application_id=$1 AND revision=$2", app.ID, app.Revision).Scan(&resolved)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	services := map[string]*serviceProvenance{}
	images := []string{}
	for name, svc := range app.Spec.Services {
		item := &serviceProvenance{ConfiguredImage: svc.Image, SourceStatus: "unknown", Builds: []buildProvenance{}}
		if resolved != nil {
			if accepted, ok := resolved.Services[name]; ok {
				item.AcceptedImage = accepted.Image
			}
		}
		// Never resolve a moving tag or infer provenance from a branch's current HEAD.
		if strings.Contains(item.AcceptedImage, "@sha256:") {
			images = append(images, item.AcceptedImage)
		}
		services[name] = item
	}
	truncated := false
	if len(images) > 0 {
		rows, err := s.Store.Pool.Query(r.Context(), `
   SELECT id,build_id,commit_sha,image,config->>'provider',config->>'repository',
          config->>'branch',run_url,created_at,config->>'service',COALESCE(config->'reuse_services','[]'::jsonb)
   FROM build_runs
   WHERE status='completed' AND conclusion='success' AND image=ANY($1)
     AND config->>'project'=$2 AND config->>'environment'=$3
     AND (config->>'application_id'=$4 OR (COALESCE(config->>'application_id','')='' AND config->>'name'=$5))
   ORDER BY created_at DESC,id DESC LIMIT 101`, images, app.Project, app.Environment, app.ID, app.Name)
		if err != nil {
			failure(w, err)
			return
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var build buildProvenance
			var service string
			var reuse []string
			if err := rows.Scan(&build.RunID, &build.BuildID, &build.CommitSHA, &build.Image, &build.Provider, &build.Repository, &build.Branch, &build.RunURL, &build.CreatedAt, &service, &reuse); err != nil {
				failure(w, err)
				return
			}
			count++
			if count > 100 {
				truncated = true
				break
			}
			if !commitPattern.MatchString(build.CommitSHA) {
				continue
			}
			for name, item := range services {
				if item.AcceptedImage == build.Image && (name == service || slices.Contains(reuse, name)) {
					item.Builds = append(item.Builds, build)
					item.SourceStatus = "matched_build"
				}
			}
		}
		if err := rows.Err(); err != nil {
			failure(w, err)
			return
		}
	}
	for _, item := range services {
		commits := map[string]bool{}
		for _, build := range item.Builds {
			commits[build.CommitSHA] = true
		}
		if truncated && len(commits) > 0 {
			item.SourceStatus = "incomplete"
			continue
		}
		if len(commits) == 1 {
			for commit := range commits {
				item.CommitSHA = commit
			}
		}
		if len(commits) > 1 {
			item.SourceStatus = "ambiguous"
		}
	}
	write(w, 200, map[string]any{"application_id": app.ID, "revision": app.Revision, "services": services, "truncated": truncated,
		"note": "Build records match the accepted release image exactly. They do not prove that every running pod has reached that release; inspect service_runtime image IDs and readiness. Multiple commits may produce the same digest.",
	})
}
