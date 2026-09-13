package api

import (
	"fmt"
	"net/http"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
)

type backendCertificateInput struct {
	Hostname       string `json:"hostname"`
	CertificatePEM string `json:"certificate_pem,omitempty"`
	PrivateKeyPEM  string `json:"private_key_pem,omitempty"`
	FromIngress    bool   `json:"from_ingress,omitempty"`
}

func (in backendCertificateInput) validate() error {
	if !spec.ValidHostname(in.Hostname) {
		return fmt.Errorf("hostname must be a lowercase DNS name without a scheme, path, port or wildcard")
	}
	if in.FromIngress {
		if in.CertificatePEM != "" || in.PrivateKeyPEM != "" {
			return fmt.Errorf("choose from_ingress or a PEM certificate and key")
		}
	} else if in.CertificatePEM == "" || in.PrivateKeyPEM == "" || len(in.CertificatePEM) > 256<<10 || len(in.PrivateKeyPEM) > 32<<10 {
		return fmt.Errorf("provide certificate_pem up to 256 KiB and private_key_pem up to 32 KiB")
	}
	return nil
}

func (s *Server) backendCertificates(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	service := r.PathValue("service")
	if _, ok = a.Spec.Services[service]; !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	items, err := s.Cluster.BackendCertificates(r.Context(), cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}, service)
	if err != nil {
		problem(w, 503, "unavailable", "backend certificate observations are unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items})
}

func (s *Server) uploadBackendCertificate(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	service := r.PathValue("service")
	if _, ok = a.Spec.Services[service]; !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	var in backendCertificateInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	source := "upload"
	if in.FromIngress {
		source = "ingress"
	}
	// Audit only public metadata. PEM bodies must not enter PostgreSQL,
	// deployment revisions, error messages or structured logs.
	if err := s.Store.RuntimeAudit(r.Context(), who(r), "backend-certificate.upload-requested", a.ID, map[string]any{"service": service, "hostname": in.Hostname, "source": source}); err != nil {
		failure(w, err)
		return
	}
	target := cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}
	var value cluster.BackendCertificateStatus
	var err error
	if in.FromIngress {
		value, err = s.Cluster.ImportBackendIngressCertificate(r.Context(), target, service, in.Hostname)
	} else {
		value, err = s.Cluster.PutBackendCertificate(r.Context(), target, service, in.Hostname, []byte(in.CertificatePEM), []byte(in.PrivateKeyPEM))
	}
	if err != nil {
		problem(w, 400, "invalid_certificate", err.Error())
		return
	}
	write(w, 201, value)
}
