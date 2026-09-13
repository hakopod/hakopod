package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (s *Server) deployTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Configuration    templateConfiguration `json:"configuration"`
		TOML             string                `json:"toml"`
		ExpectedRevision *int64                `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision != 0 || in.TOML == "" {
		problem(w, 400, "review_required", "review a template for a new application before deploying")
		return
	}
	cfg := in.Configuration
	if !validScope(cfg.Project, cfg.Environment) || !slug.MatchString(cfg.Name) {
		problem(w, 400, "invalid_scope", "valid project, environment and application name are required")
		return
	}
	if !who(r).Allows("deployments:write", cfg.Project, cfg.Environment, cfg.Name) {
		failure(w, store.ErrForbidden)
		return
	}
	expected, err := spec.PlanTemplate(r.PathValue("id"), cfg.TemplateOptions)
	if err != nil {
		problem(w, 400, "invalid_template", err.Error())
		return
	}
	next, _, ok := s.prepare(w, r, input{Project: cfg.Project, Environment: cfg.Environment, TOML: in.TOML}, "deployments:write")
	if !ok {
		return
	}
	if !reflect.DeepEqual(next, expected) {
		problem(w, 409, "template_changed", "template configuration changed; review a new plan before deploying")
		return
	}
	required := spec.TemplateSecretNames(next)
	if len(required) > 0 {
		if s.Cluster == nil {
			problem(w, 503, "unavailable", "Kubernetes secret storage is unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		values, err := s.Cluster.ReadWorkloadSecrets(ctx, cfg.Project, cfg.Environment, cfg.Name, required)
		if err != nil {
			problem(w, 400, "template_secrets", err.Error())
			return
		}
		if err = spec.ValidateTemplateSecretSet(r.PathValue("id"), required, values); err != nil {
			problem(w, 400, "template_secrets", err.Error())
			return
		}
	}
	d, err := s.Store.Accept(r.Context(), who(r), cfg.Project, cfg.Environment, next, 0, r.Header.Get("Idempotency-Key"))
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrForbidden) {
			failure(w, err)
		} else {
			problem(w, 400, "invalid_deployment", err.Error())
		}
		return
	}
	write(w, 202, d)
}

func (s *Server) putTemplateSecret(w http.ResponseWriter, r *http.Request) {
	p, e, a, ok := s.secretScope(w, r, "deployments:write")
	if !ok {
		return
	}
	field, supported := spec.TemplateSecretFieldByName(r.PathValue("id"), r.PathValue("name"))
	if !supported {
		problem(w, 400, "invalid_secret", "unsupported template secret reference")
		return
	}
	var in struct {
		Value    string `json:"value"`
		Generate bool   `json:"generate"`
		Replace  bool   `json:"replace"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Generate && in.Value != "") || (!in.Generate && in.Value == "") {
		problem(w, 400, "invalid_secret", "provide a value or request generation, but not both")
		return
	}
	if _, err := s.Store.FindApplication(r.Context(), p, e, a); err == nil {
		problem(w, 409, "application_exists", "this application already exists; manage its secrets from the application page")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if in.Generate {
		if !field.Generate {
			problem(w, 400, "invalid_secret", "this credential must be supplied by its provider or certificate issuer")
			return
		}
		value, err := s.generateTemplateSecret(ctx, p, e, a, field)
		if err != nil {
			problem(w, 400, "invalid_secret", err.Error())
			return
		}
		in.Value = value
	}
	if err := spec.ValidateTemplateSecret(r.PathValue("id"), field.Name, in.Value); err != nil {
		problem(w, 400, "invalid_secret", err.Error())
		return
	}
	var err error
	if in.Replace {
		err = s.Cluster.PutWorkloadSecret(ctx, p, e, a, field.Name, in.Value)
	} else {
		err = s.Cluster.CreateWorkloadSecret(ctx, p, e, a, field.Name, in.Value)
	}
	if apierrors.IsAlreadyExists(err) {
		problem(w, 409, "secret_exists", "this reference already exists; reuse it or explicitly replace it")
		return
	}
	if err != nil {
		problem(w, 503, "secret_unavailable", "the secret could not be saved; retry when secret storage is available")
		return
	}
	_, err = s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'secret.write',$3)", who(r).ID, who(r).KeyID, p+"/"+e+"/"+a+"/"+field.Name)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, map[string]any{"name": field.Name, "saved": true})
}

func (s *Server) generateTemplateSecret(ctx context.Context, project, environment, application string, field spec.TemplateSecretField) (string, error) {
	if field.Format == "postgres-url" || field.Format == "redis-url" {
		password := "database-password"
		if field.Format == "redis-url" {
			password = "redis-password"
		}
		values, err := s.Cluster.ReadWorkloadSecrets(ctx, project, environment, application, []string{password})
		if err != nil {
			return "", fmt.Errorf("save %s before generating this connection URL", password)
		}
		if err = spec.ValidateTemplateSecret("infisical", password, values[password]); err != nil {
			return "", err
		}
		u := url.URL{Scheme: "postgresql", Host: "db:5432", Path: "/app", User: url.UserPassword("hakopod", values[password]), RawQuery: "sslmode=disable"}
		if field.Format == "redis-url" {
			u = url.URL{Scheme: "redis", Host: "redis:6379", User: url.UserPassword("", values[password])}
		}
		return u.String(), nil
	}
	size := 32
	if field.Format == "hex32" {
		size = 16
	}
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("secure credential generation is unavailable")
	}
	if field.Format == "hex32" {
		return hex.EncodeToString(value), nil
	}
	return base64.StdEncoding.EncodeToString(value), nil
}
