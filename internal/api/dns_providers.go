package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hakopod/hakopod/internal/dnsprovider"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) registerDNSProviderRoutes(routes *http.ServeMux) {
	routes.HandleFunc("GET /api/v1/dns-providers", s.listDNSProviders)
	routes.HandleFunc("PUT /api/v1/dns-providers/{name}", s.putDNSProvider)
	routes.HandleFunc("DELETE /api/v1/dns-providers/{name}", s.deleteDNSProvider)
}

// dnsProviderFailure maps this feature's sentinels. A provider's own words never
// reach a response: internal/dnsprovider collapses every provider failure to
// ErrUnavailable, and this prints fixed prose for it rather than an error
// string. ErrInput carries hakopod's own validation text and is safe to show.
func dnsProviderFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dnsprovider.ErrInput):
		problem(w, 400, "invalid_dns_provider", err.Error())
	case errors.Is(err, dnsprovider.ErrUnavailable):
		problem(w, 503, "dns_provider_unavailable", "The DNS provider is unavailable. Check the provider's access and permissions, then retry.")
	default:
		failure(w, err)
	}
}

// dnsEncryptionKey guards every path that stores or opens a credential, exactly
// as the secret provider routes do.
func (s *Server) dnsEncryptionKey(w http.ResponseWriter) ([]byte, bool) {
	key := s.authEncryptionKey()
	if len(key) != 32 {
		problem(w, 503, "unavailable", "Configure the persistent authentication encryption key before storing provider credentials.")
		return nil, false
	}
	return key, true
}

// dnsProviderByName resolves the stored row behind a URL name. A name is unique
// per scope, not installation-wide, so the scope has to be part of the lookup.
func (s *Server) dnsProviderByName(r *http.Request, principal store.Principal, name, project, environment string) (dnsprovider.Provider, error) {
	items, err := s.Store.DNSProviders(r.Context(), principal)
	if err != nil {
		return dnsprovider.Provider{}, err
	}
	for _, item := range items {
		if strings.EqualFold(item.Name, name) && item.Project == project && item.Environment == environment {
			return item, nil
		}
	}
	return dnsprovider.Provider{}, pgx.ErrNoRows
}

func (s *Server) listDNSProviders(w http.ResponseWriter, r *http.Request) {
	principal := who(r)
	if !principal.CanManageDNSProviders() {
		failure(w, store.ErrForbidden)
		return
	}
	items, err := s.Store.DNSProviders(r.Context(), principal)
	if err != nil {
		failure(w, err)
		return
	}
	if principal.IsAdmin() {
		write(w, 200, map[string]any{"items": items})
		return
	}
	// A delegated owner discovers only names and kinds inside its own scope. The
	// zone filter is the boundary this feature enforces, so it stays with the
	// administrators who set it.
	project, environment := scope(r)
	if !validScope(project, environment) {
		problem(w, 403, "forbidden", "select an accessible project and environment")
		return
	}
	available := []map[string]string{}
	for _, provider := range items {
		if provider.Allows(project, environment) {
			available = append(available, map[string]string{"name": provider.Name, "kind": provider.Kind})
		}
	}
	write(w, 200, map[string]any{"items": available})
}

func (s *Server) putDNSProvider(w http.ResponseWriter, r *http.Request) {
	principal := who(r)
	if !principal.CanManageDNSProviders() {
		failure(w, store.ErrForbidden)
		return
	}
	key, ok := s.dnsEncryptionKey(w)
	if !ok {
		return
	}
	var in dnsprovider.Input
	if !decode(w, r, &in) {
		return
	}
	if in.Name != "" && in.Name != r.PathValue("name") {
		problem(w, 400, "invalid_dns_provider", "provider name must match the URL")
		return
	}
	in.Name = r.PathValue("name")
	if err := in.Provider.Validate(); err != nil {
		dnsProviderFailure(w, err)
		return
	}
	existing, err := s.dnsProviderByName(r, principal, in.Name, in.Project, in.Environment)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	in.ID = existing.ID
	if in.Credentials != nil {
		sealed, err := dnsprovider.SealCredentials(key, in.Provider, *in.Credentials)
		if err != nil {
			dnsProviderFailure(w, err)
			return
		}
		in.EncryptedCredentials = sealed
	}
	// An omitted credential keeps the stored bytes, and the store decides whether
	// they may carry forward. The audit event is written there too, inside the
	// same transaction as the row.
	provider, err := s.Store.PutDNSProvider(r.Context(), principal, in.Provider, in.ExpectedRevision)
	if err != nil {
		dnsProviderFailure(w, err)
		return
	}
	status := http.StatusOK
	if in.ExpectedRevision == 0 {
		status = http.StatusCreated
	}
	write(w, status, provider)
}

func (s *Server) deleteDNSProvider(w http.ResponseWriter, r *http.Request) {
	principal := who(r)
	if !principal.CanManageDNSProviders() {
		failure(w, store.ErrForbidden)
		return
	}
	var in struct {
		Project          string `json:"project"`
		Environment      string `json:"environment"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	existing, err := s.dnsProviderByName(r, principal, r.PathValue("name"), in.Project, in.Environment)
	if err != nil {
		failure(w, err)
		return
	}
	if err = s.Store.DeleteDNSProvider(r.Context(), principal, existing.ID, in.ExpectedRevision); err != nil {
		dnsProviderFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}

// applicationDNSProviders answers what one application may use. It returns the
// identifier the record endpoint takes, plus the name and kind a picker needs,
// and nothing else.
func (s *Server) applicationDNSProviders(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	// Deliberately not the management listing: a developer with deployments:write
	// needs to pick a provider without being able to manage one.
	items, err := s.Store.DNSProvidersForApplication(r.Context(), a.Project, a.Environment)
	if err != nil {
		failure(w, err)
		return
	}
	available := []map[string]string{}
	for _, provider := range items {
		if provider.Allows(a.Project, a.Environment) {
			available = append(available, map[string]string{"id": provider.ID, "name": provider.Name, "kind": provider.Kind})
		}
	}
	write(w, 200, map[string]any{"items": available})
}
