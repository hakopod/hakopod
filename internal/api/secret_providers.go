package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hakopod/hakopod/internal/secretprovider"
)

// ConfigureSecretProviders wires one bounded resolver into deployment. It uses
// the existing persistent authentication key, never an ephemeral fallback.
func (s *Server) ConfigureSecretProviders() {
	if s.Cluster != nil {
		s.Cluster.SetExternalSecretResolver(secretprovider.NewService(s.Store, s.authEncryptionKey()).Resolve)
	}
}

func (s *Server) registerSecretProviderRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/secret-providers", s.listSecretProviders)
	routes.HandleFunc("GET /api/v1/secret-providers/{name}", s.getSecretProvider)
	routes.HandleFunc("PUT /api/v1/secret-providers/{name}", s.putSecretProvider)
	routes.HandleFunc("DELETE /api/v1/secret-providers/{name}", s.deleteSecretProvider)
}

func secretProviderFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, secretprovider.ErrInput) {
		problem(w, 400, "invalid_secret_provider", err.Error())
		return
	}
	failure(w, err)
}

func (s *Server) listSecretProviders(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	principal := who(r)
	if !principal.IsAdmin() && (!validScope(p, e) || !principal.Allows("deployments:read", p, e, r.URL.Query().Get("application"))) {
		problem(w, 403, "forbidden", "select an accessible project and environment")
		return
	}
	items, err := s.Store.SecretProviders(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if principal.IsAdmin() {
		write(w, 200, map[string]any{"items": items})
		return
	}
	// Deployment users discover only provider names granted to their scope.
	available := []map[string]string{}
	for _, provider := range items {
		if provider.Allows(p, e) {
			available = append(available, map[string]string{"name": provider.Name, "kind": provider.Kind})
		}
	}
	write(w, 200, map[string]any{"items": available})
}

func (s *Server) getSecretProvider(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	provider, err := s.Store.SecretProvider(r.Context(), r.PathValue("name"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, provider)
}

func (s *Server) putSecretProvider(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	key := s.authEncryptionKey()
	if len(key) != 32 {
		problem(w, 503, "unavailable", "Configure the persistent authentication encryption key before storing provider credentials.")
		return
	}
	var input secretprovider.Input
	if !decode(w, r, &input) {
		return
	}
	if input.Name != "" && input.Name != r.PathValue("name") {
		problem(w, 400, "invalid_secret_provider", "provider name must match the URL")
		return
	}
	input.Name = r.PathValue("name")
	input.Endpoint = strings.TrimSuffix(input.Endpoint, "/")
	if err := input.Provider.Validate(); err != nil {
		secretProviderFailure(w, err)
		return
	}
	var sealed []byte
	if input.ExpectedRevision > 0 {
		old, err := s.Store.SecretProvider(r.Context(), input.Name)
		if err != nil {
			failure(w, err)
			return
		}
		sealed = old.EncryptedCredentials
	}
	if input.Credentials != nil {
		var err error
		sealed, err = secretprovider.SealCredentials(key, input.Provider, *input.Credentials)
		if err != nil {
			secretProviderFailure(w, err)
			return
		}
	} else if input.ExpectedRevision == 0 {
		problem(w, 400, "invalid_secret_provider", "credentials are required when creating a provider")
		return
	}
	input.EncryptedCredentials = sealed
	provider, err := s.Store.PutSecretProvider(r.Context(), who(r), input.Provider, input.ExpectedRevision)
	if err != nil {
		secretProviderFailure(w, err)
		return
	}
	status := http.StatusOK
	if input.ExpectedRevision == 0 {
		status = http.StatusCreated
	}
	write(w, status, provider)
}

func (s *Server) deleteSecretProvider(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.Store.DeleteSecretProvider(r.Context(), who(r), r.PathValue("name"), input.ExpectedRevision); err != nil {
		secretProviderFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
