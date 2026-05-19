package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
)

type registryMetadata struct {
	Registry   string `json:"registry"`
	TokenRealm string `json:"token_realm,omitempty"`
	SecretName string `json:"secret_name"`
}
type registryInfo struct {
	Name         string    `json:"name"`
	Project      string    `json:"project"`
	Environment  string    `json:"environment"`
	Registry     string    `json:"registry"`
	TokenRealm   string    `json:"token_realm,omitempty"`
	Revision     int64     `json:"revision"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Synchronized bool      `json:"synchronized"`
	Message      string    `json:"message,omitempty"`
}

func registryView(value store.RuntimeResource) registryInfo {
	var m registryMetadata
	_ = json.Unmarshal(value.Metadata, &m)
	return registryInfo{Name: value.Name, Project: value.Project, Environment: value.Environment, Registry: m.Registry, TokenRealm: m.TokenRealm, Revision: value.Revision, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Message: "Namespace synchronization has not been checked in this response."}
}
func (s *Server) registryScope(w http.ResponseWriter, r *http.Request, p, e, permission string) bool {
	if !validScope(p, e) {
		problem(w, 400, "invalid_scope", "project and environment are required")
		return false
	}
	if !who(r).Allows(permission, p, e, "") {
		failure(w, store.ErrForbidden)
		return false
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes secret storage is unavailable")
		return false
	}
	return true
}
func (s *Server) registries(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	if !s.registryScope(w, r, p, e, "deployments:read") {
		return
	}
	rows, err := s.Store.RuntimeResources(r.Context(), "registry", p, e)
	if err != nil {
		failure(w, err)
		return
	}
	items := []registryInfo{}
	for _, row := range rows {
		items = append(items, registryView(row))
	}
	write(w, 200, map[string]any{"items": items})
}

type registryInput struct {
	Project          string `json:"project"`
	Environment      string `json:"environment"`
	Name             string `json:"name,omitempty"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	Registry         string `json:"registry"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	TokenRealm       string `json:"token_realm,omitempty"`
}

func (s *Server) putRegistry(w http.ResponseWriter, r *http.Request) {
	var in registryInput
	if !decode(w, r, &in) {
		return
	}
	if !s.registryScope(w, r, in.Project, in.Environment, "deployments:write") {
		return
	}
	name := in.Name
	expected := int64(0)
	status := http.StatusCreated
	if r.Method == http.MethodPut {
		name = r.PathValue("name")
		status = 200
		if in.ExpectedRevision == nil || *in.ExpectedRevision < 1 || in.Name != "" && in.Name != name {
			problem(w, 400, "invalid_request", "expected_revision is required and names must agree")
			return
		}
		expected = *in.ExpectedRevision
	} else if in.ExpectedRevision != nil {
		problem(w, 400, "invalid_request", "create does not accept expected_revision")
		return
	}
	if !slug.MatchString(name) {
		problem(w, 400, "invalid_request", "invalid registry credential name")
		return
	}
	host, err := cluster.NormalizeRegistry(in.Registry)
	if err != nil {
		problem(w, 400, "invalid_registry", err.Error())
		return
	}
	credential := cluster.RegistryCredential{Registry: host, Username: in.Username, Password: in.Password, TokenRealm: in.TokenRealm, Revision: expected + 1}
	if err = cluster.ValidateRegistryCredential(credential); err != nil {
		problem(w, 400, "invalid_registry", err.Error())
		return
	}
	old, err := s.Store.RuntimeResource(r.Context(), "registry", in.Project, in.Environment, name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	if old.Revision != expected {
		failure(w, store.ErrConflict)
		return
	}
	var previous registryMetadata
	if old.Revision > 0 {
		if json.Unmarshal(old.Metadata, &previous) != nil {
			problem(w, 500, "unavailable", "registry metadata is unavailable")
			return
		}
		if previous.Registry != host {
			problem(w, 409, "registry_host_changed", "create a new credential name to change the registry host")
			return
		}
	}
	next := registryMetadata{Registry: host, TokenRealm: in.TokenRealm, SecretName: "hp-registry-" + store.NewID()}
	if err = s.Cluster.PutPlatformSecret(r.Context(), next.SecretName, corev1.SecretTypeDockerConfigJson, cluster.RegistrySecretData(credential), map[string]string{"hakopod.io/registry-credential": cluster.RegistryScope(in.Project, in.Environment, name)}); err != nil {
		problem(w, 503, "unavailable", "registry credential storage is unavailable")
		return
	}
	value, err := s.Store.PutRuntimeResource(r.Context(), who(r), "registry", in.Project, in.Environment, name, expected, next)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = s.Cluster.DeletePlatformSecret(ctx, next.SecretName)
		cancel()
		failure(w, err)
		return
	}
	result := registryView(value)
	result.Synchronized = true
	result.Message = ""
	if _, err = s.Cluster.RefreshRegistryCopies(r.Context(), in.Project, in.Environment, name, false); err != nil {
		result.Synchronized = false
		result.Message = "Credential saved; existing namespace copies need another synchronization attempt."
	}
	if previous.SecretName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr := s.Cluster.DeletePlatformSecret(ctx, previous.SecretName)
		cancel()
		if cleanupErr != nil {
			result.Synchronized = false
			result.Message = "Credential saved; old secret cleanup requires retry."
		}
	}
	write(w, status, result)
}
func (s *Server) syncRegistry(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	if !s.registryScope(w, r, p, e, "deployments:write") {
		return
	}
	name := r.PathValue("name")
	if !slug.MatchString(name) {
		problem(w, 400, "invalid_request", "invalid credential name")
		return
	}
	value, err := s.Store.RuntimeResource(r.Context(), "registry", p, e, name)
	if err != nil {
		failure(w, err)
		return
	}
	count, err := s.Cluster.RefreshRegistryCopies(r.Context(), p, e, name, false)
	if err != nil {
		problem(w, 503, "sync_incomplete", "registry synchronization is incomplete; retry when Kubernetes is available")
		return
	}
	if err = s.Store.RuntimeAudit(r.Context(), who(r), "registry.synchronized", p+"/"+e+"/"+name, map[string]any{"revision": value.Revision, "copies": count}); err != nil {
		failure(w, err)
		return
	}
	result := registryView(value)
	result.Synchronized = true
	result.Message = ""
	write(w, 200, result)
}
func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project          string `json:"project"`
		Environment      string `json:"environment"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.registryScope(w, r, in.Project, in.Environment, "deployments:write") {
		return
	}
	name := r.PathValue("name")
	if !slug.MatchString(name) || in.ExpectedRevision == nil || *in.ExpectedRevision < 1 {
		problem(w, 400, "invalid_request", "valid name and expected_revision are required")
		return
	}
	value, err := s.Store.RuntimeResource(r.Context(), "registry", in.Project, in.Environment, name)
	if err != nil {
		failure(w, err)
		return
	}
	if value.Revision != *in.ExpectedRevision {
		failure(w, store.ErrConflict)
		return
	}
	used, err := s.Cluster.RegistryInUse(r.Context(), in.Project, in.Environment, name)
	if err != nil {
		problem(w, 503, "unavailable", "cannot verify credential references")
		return
	}
	if used {
		problem(w, 409, "credential_in_use", "an existing deployment or active pod still references this credential")
		return
	}
	if err = s.Store.DeleteRuntimeResource(r.Context(), who(r), "registry", in.Project, in.Environment, name, *in.ExpectedRevision); err != nil {
		failure(w, err)
		return
	}
	var metadata registryMetadata
	_ = json.Unmarshal(value.Metadata, &metadata)
	_, copyErr := s.Cluster.RefreshRegistryCopies(r.Context(), in.Project, in.Environment, name, true)
	secretErr := s.Cluster.DeletePlatformSecret(r.Context(), metadata.SecretName)
	write(w, 200, map[string]bool{"deleted": true, "cleanup_pending": copyErr != nil || secretErr != nil})
}
