package api

import (
	"encoding/json"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) registerTemplateRoutes(routes *http.ServeMux) {
	s.registerShowcaseRoutes(routes)
	routes.HandleFunc("GET /api/v1/templates", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]any{"items": spec.Templates()}) })
	routes.HandleFunc("POST /api/v1/templates/{id}/plan", s.planTemplate)
	routes.HandleFunc("POST /api/v1/templates/{id}/deploy", s.deployTemplate)
	routes.HandleFunc("PUT /api/v1/templates/{id}/secrets/{name}", s.putTemplateSecret)
}

type templateConfiguration struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	spec.TemplateOptions
}

func (s *Server) planTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateConfiguration
	if !decode(w, r, &in) {
		return
	}
	if !validScope(in.Project, in.Environment) || !slug.MatchString(in.Name) {
		problem(w, 400, "invalid_scope", "valid project, environment and application name are required")
		return
	}
	if !who(r).Allows("deployments:write", in.Project, in.Environment, in.Name) {
		problem(w, 403, "forbidden", "deployment scope is not permitted")
		return
	}
	if r.PathValue("id") == "vllm" && in.ModelRevision == "" {
		if in.UseModelToken {
			problem(w, 400, "model_revision_required", "provide the immutable model revision for a private or gated model, then save its Hugging Face token during review")
			return
		}
		// Resolve public model metadata only; never execute repository code or load
		// model weights in the management process. No trust_remote_code option.
		if !repositoryPattern.MatchString(in.Model) {
			problem(w, 400, "invalid_model", "use a Hugging Face owner/model identifier")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", "https://huggingface.co/api/models/"+in.Model, nil)
		if err != nil {
			failure(w, err)
			return
		}
		client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(req)
		if err != nil {
			problem(w, 400, "model_unavailable", "Hugging Face metadata is unavailable; provide an immutable revision or retry")
			return
		}
		defer response.Body.Close()
		var model struct {
			SHA     string `json:"sha"`
			Private bool   `json:"private"`
			Gated   any    `json:"gated"`
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
		if response.StatusCode != 200 || readErr != nil || len(body) > 256<<10 || json.Unmarshal(body, &model) != nil || model.Private {
			problem(w, 400, "model_unavailable", "choose a public model with available metadata")
			return
		}
		if gated, ok := model.Gated.(bool); (!ok && model.Gated != nil) || (ok && gated) {
			problem(w, 400, "model_gated", "obtain model access upstream, enable a model access token and provide its immutable revision")
			return
		}
		in.ModelRevision = model.SHA
	}
	next, err := spec.PlanTemplate(r.PathValue("id"), in.TemplateOptions)
	if err != nil {
		problem(w, 400, "invalid_template", err.Error())
		return
	}
	next, previous, ok := s.prepare(w, r, input{Project: in.Project, Environment: in.Environment, Spec: &next}, "deployments:write")
	if !ok {
		return
	}
	var before *spec.Application
	var revision int64
	id := ""
	if previous != nil {
		before = &previous.Spec
		revision = previous.Revision
		id = previous.ID
	}
	warnings := spec.Warnings(next)
	required := spec.TemplateSecretNames(next)
	for _, t := range spec.Templates() {
		if t.ID == r.PathValue("id") {
			warnings = append(warnings, t.Verification, t.ResourceSummary)
			warnings = append(warnings, t.Requirements...)
		}
	}
	if len(required) > 0 {
		warnings = append(warnings, fmt.Sprintf("Before deployment, save these secret references for %s/%s/%s: %s", in.Project, in.Environment, in.Name, strings.Join(required, ", ")))
	}
	canonical, err := toml.Marshal(next)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"application_id": id, "expected_revision": revision, "spec": next, "toml": string(canonical), "configuration": in, "changes": spec.Diff(before, next), "warnings": warnings, "resource_profiles": spec.Profiles, "required_secrets": required, "model_source": func() string {
		if in.Model == "" || r.PathValue("id") != "vllm" {
			return ""
		}
		return "https://huggingface.co/" + in.Model + "/tree/" + url.PathEscape(in.ModelRevision)
	}()})
}
