package api

import (
	"context"
	"net/http"
	"net/mail"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
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
		IssuerKind       string `json:"issuer_kind,omitempty"`
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
	if in.IssuerKind != "" && (in.Issuer == "" || in.IssuerKind != "Issuer" && in.IssuerKind != "ClusterIssuer") {
		problem(w, 400, "invalid_request", "issuer_kind requires an issuer and must be Issuer or ClusterIssuer")
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
	tls := &spec.TLSConfig{Issuer: in.Issuer, IssuerKind: in.IssuerKind}
	if in.Issuer != "" {
		if err = s.Cluster.ValidateTLSIssuer(r.Context(), cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: next, Revision: a.Revision}, in.Issuer, in.IssuerKind); err != nil {
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
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var value cluster.TLSIssuers
	var err error
	if s.Auth.DeploymentMode == cluster.DeploymentManagedCloud && !s.cloudOperator(who(r)) {
		value, err = s.Cluster.DefaultTLSIssuers(ctx)
	} else {
		value, err = s.Cluster.TLSIssuers(ctx)
	}
	if err != nil {
		problem(w, 503, "unavailable", "cert-manager observations are unavailable")
		return
	}
	write(w, 200, value)
}

func (s *Server) applicationTLSIssuers(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	value, err := s.Cluster.ApplicationTLSIssuers(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision})
	if err != nil {
		problem(w, 503, "unavailable", "application certificate issuer observations are unavailable")
		return
	}
	write(w, 200, value)
}

func (s *Server) createApplicationTLSIssuer(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
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
	if address, err := mail.ParseAddress(in.Email); err != nil || address.Address != in.Email || len(in.Email) > 254 {
		problem(w, 400, "invalid_issuer", "a valid ACME account email is required")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	// Runtime claims hold a pool connection during Kubernetes requests. Keep
	// capacity available for authorization, audit writes and ordinary API reads.
	select {
	case s.tlsIssuerChanges <- struct{}{}:
		defer func() { <-s.tlsIssuerChanges }()
	default:
		w.Header().Set("Retry-After", "2")
		problem(w, 503, "issuer_busy", "certificate issuer setup is busy; retry shortly")
		return
	}
	// The application runtime lock also fences deployment and deletion. It
	// serializes the bounded issuer count and creation across API processes.
	claim, err := s.Store.ClaimRuntime(ctx, a.ID, a.Revision)
	if err != nil {
		failure(w, err)
		return
	}
	if claim == nil {
		problem(w, 409, "application_busy", "wait for the current application deployment to succeed, then retry issuer creation")
		return
	}
	defer claim.Release()
	p, err := s.freshRuntimePrincipal(r)
	if err != nil || !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		failure(w, store.ErrForbidden)
		return
	}
	if err = claim.Check(ctx); err != nil {
		problem(w, 409, "application_changed", "the application changed; refresh it before creating an issuer")
		return
	}
	metadata := map[string]any{"name": in.Name, "kind": "Issuer", "production": in.Production}
	if err = s.Store.RuntimeAudit(ctx, p, "application-tls-issuer.requested", a.ID, metadata); err != nil {
		failure(w, err)
		return
	}
	value, err := s.Cluster.CreateApplicationTLSIssuer(ctx, cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, in.Name, in.Email, in.Production)
	if err != nil {
		problem(w, 409, "issuer_unavailable", err.Error())
		return
	}
	// Kubernetes owns the immutable issuer and account key. A repeated request
	// with the same public configuration recovers an interrupted response.
	if err = s.Store.RuntimeAudit(ctx, p, "application-tls-issuer.configured", a.ID, metadata); err != nil {
		failure(w, err)
		return
	}
	write(w, 201, value)
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
