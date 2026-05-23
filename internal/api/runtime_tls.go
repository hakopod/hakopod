package api

import (
	"net/http"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
)

func (s *Server) serviceTLS(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	name := r.PathValue("service")
	if _, ok = a.Spec.Services[name]; !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	value, err := s.Cluster.ServiceTLS(r.Context(), cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, name)
	if err != nil {
		problem(w, 503, "unavailable", "TLS observations are unavailable")
		return
	}
	write(w, 200, value)
}
func (s *Server) attachTLS(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		Certificate      string `json:"certificate_pem,omitempty"`
		PrivateKey       string `json:"private_key_pem,omitempty"`
		Issuer           string `json:"issuer,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 1 || len(idem) < 8 || len(idem) > 128 {
		problem(w, 400, "invalid_request", "expected_revision and an 8–128 character Idempotency-Key are required")
		return
	}
	if in.Issuer != "" && (in.Certificate != "" || in.PrivateKey != "") || in.Issuer == "" && (in.Certificate == "" || in.PrivateKey == "") {
		problem(w, 400, "invalid_request", "provide either issuer or both certificate_pem and private_key_pem")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	base, err := s.Store.RuntimeBase(r.Context(), a.ID, *in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	if base.Status != "succeeded" || base.ResolvedSpec == nil {
		problem(w, 409, "revision_not_ready", "TLS attachment requires a successful deployed revision")
		return
	}
	next, err := spec.Normalize(base.Spec)
	if err != nil {
		failure(w, err)
		return
	}
	resolved, err := spec.Normalize(*base.ResolvedSpec)
	if err != nil {
		failure(w, err)
		return
	}
	name := r.PathValue("service")
	svc, ok := next.Services[name]
	if !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	if !svc.Public {
		problem(w, 400, "invalid_service", "TLS requires a public service")
		return
	}
	tls := &spec.TLSConfig{Issuer: in.Issuer}
	if in.Issuer != "" {
		if err = s.Cluster.TLSIssuerExists(r.Context(), in.Issuer); err != nil {
			problem(w, 409, "issuer_unavailable", err.Error())
			return
		}
	} else {
		tls.Certificate, err = s.Cluster.PutTLSCertificate(r.Context(), cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: next, Revision: a.Revision}, name, []byte(in.Certificate), []byte(in.PrivateKey))
		if err != nil {
			problem(w, 400, "invalid_certificate", err.Error())
			return
		}
	}
	svc.TLS = tls
	next.Services[name] = svc
	artifact := resolved.Services[name]
	artifact.TLS = tls
	resolved.Services[name] = artifact
	next, err = spec.Normalize(next)
	if err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	value, err := s.Store.Accept(r.Context(), who(r), a.Project, a.Environment, next, *in.ExpectedRevision, idem, resolved)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, value)
}
func (s *Server) tlsIssuers(w http.ResponseWriter, r *http.Request) {
	// Issuer names, ACME endpoints and conditions are nonsecret and usable by
	// any authenticated deployer; modification remains administrator-only.
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	value, err := s.Cluster.TLSIssuers(r.Context())
	if err != nil {
		problem(w, 503, "unavailable", "cert-manager observations are unavailable")
		return
	}
	write(w, 200, value)
}
func (s *Server) createTLSIssuer(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		Production bool   `json:"production,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slug.MatchString(in.Name) {
		problem(w, 400, "invalid_issuer", "a valid issuer name is required")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	if err := s.Store.RuntimeAudit(r.Context(), who(r), "tls-issuer.requested", in.Name, map[string]any{"production": in.Production}); err != nil {
		failure(w, err)
		return
	}
	value, err := s.Cluster.CreateTLSIssuer(r.Context(), in.Name, in.Email, in.Production)
	if err != nil {
		problem(w, 409, "issuer_unavailable", err.Error())
		return
	}
	if _, err = s.Store.PutRuntimeResource(r.Context(), who(r), "tls-issuer", "", "", in.Name, 0, map[string]any{"name": value.Name, "email": value.Email, "server": value.Server}); err != nil {
		failure(w, err)
		return
	}
	write(w, 201, value)
}
