package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (s *Server) writeDeploymentPlan(w http.ResponseWriter, r *http.Request, project, environment string, next spec.Application, plan map[string]any) {
	missing, err := s.missingSecrets(r.Context(), project, environment, next)
	if err != nil {
		problem(w, 503, "secret_storage_unavailable", err.Error())
		return
	}
	if _, defined := plan["required_secrets"]; !defined {
		plan["required_secrets"] = spec.LocalSecretNames(next)
	}
	plan["missing_secrets"] = missing
	write(w, 200, plan)
}

func (s *Server) missingSecrets(ctx context.Context, project, environment string, next spec.Application) ([]string, error) {
	if len(spec.LocalSecretNames(next)) == 0 {
		return []string{}, nil
	}
	if s.Cluster == nil {
		return nil, errors.New("application secret storage is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.Cluster.MissingWorkloadSecrets(ctx, project, environment, next)
}

func (s *Server) secretRequirements(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project     string           `json:"project"`
		Environment string           `json:"environment"`
		Spec        spec.Application `json:"spec"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validScope(in.Project, in.Environment) || !slug.MatchString(in.Spec.Name) {
		problem(w, 400, "invalid_scope", "project, environment and application name are required")
		return
	}
	if !who(r).Allows("deployments:write", in.Project, in.Environment, in.Spec.Name) {
		failure(w, store.ErrForbidden)
		return
	}
	next, err := spec.Normalize(in.Spec)
	if err != nil {
		problem(w, 400, "invalid_spec", err.Error())
		return
	}
	missing, err := s.missingSecrets(r.Context(), in.Project, in.Environment, next)
	if err != nil {
		problem(w, 503, "secret_storage_unavailable", err.Error())
		return
	}
	write(w, 200, map[string]any{"required_secrets": spec.LocalSecretNames(next), "missing_secrets": missing})
}

// POST creates without replacing an existing value, so concurrent setup and
// retries cannot rotate credentials already in use by another deployment.
func (s *Server) createWorkloadSecret(w http.ResponseWriter, r *http.Request) {
	p, e, a, ok := s.secretScope(w, r, "deployments:write")
	if !ok {
		return
	}
	name := r.PathValue("name")
	var in struct {
		Value    string `json:"value"`
		Generate bool   `json:"generate"`
		Format   string `json:"format"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slug.MatchString(name) || (in.Generate && in.Value != "") || (!in.Generate && in.Value == "") || (in.Format != "" && !in.Generate) {
		problem(w, 400, "invalid_secret", "provide a value or request generation, but not both")
		return
	}
	if in.Generate {
		data := make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			problem(w, 503, "unavailable", "secure generation is unavailable")
			return
		}
		switch in.Format {
		case "", "base64url":
			in.Value = base64.RawURLEncoding.EncodeToString(data)
		case "hex":
			in.Value = hex.EncodeToString(data)
		default:
			problem(w, 400, "invalid_secret", "choose base64url or hex generation")
			return
		}
	}
	if len(in.Value) > 64<<10 || strings.ContainsRune(in.Value, 0) {
		problem(w, 400, "invalid_secret", "use a nonempty value up to 64 KiB without NUL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := s.Cluster.CreateWorkloadSecret(ctx, p, e, a, name, in.Value)
	if apierrors.IsAlreadyExists(err) {
		problem(w, 409, "secret_exists", "This secret already exists. Recheck the requirements to reuse it.")
		return
	}
	if err != nil {
		problem(w, 503, "secret_unavailable", "The secret could not be saved. Retry when storage is available.")
		return
	}
	_, err = s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'secret.write',$3)", who(r).ID, who(r).KeyID, p+"/"+e+"/"+a+"/"+name)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, map[string]any{"name": name, "saved": true})
}

func isMissingSecrets(err error) bool {
	var missing *spec.MissingSecretsError
	return errors.As(err, &missing)
}
