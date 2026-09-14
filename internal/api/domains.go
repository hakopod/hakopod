package api

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

type domainView struct {
	Hostname          string     `json:"hostname"`
	Service           string     `json:"service"`
	Active            bool       `json:"active"`
	Verified          bool       `json:"verified"`
	VerificationName  string     `json:"verification_name"`
	VerificationValue string     `json:"verification_value"`
	VerifiedAt        *time.Time `json:"verified_at,omitempty"`
	Target            string     `json:"target"`
}

func (s *Server) domainView(a store.Application, d store.DomainVerification, approved map[string]bool) domainView {
	service, configured := a.Spec.Domains[d.Hostname]
	active := configured && approved[d.Hostname]
	if !active {
		service = d.Service
	}
	target := ""
	if s.Cluster != nil {
		target = s.Cluster.ServiceHostname(cluster.Target{ApplicationID: a.ID, Spec: a.Spec}, service)
	}
	return domainView{Hostname: d.Hostname, Service: service, Active: active, Verified: d.VerifiedAt != nil && (active || time.Since(*d.VerifiedAt) < time.Hour), VerificationName: "_hakopod." + d.Hostname, VerificationValue: d.Token, VerifiedAt: d.VerifiedAt, Target: target}
}
func (s *Server) registerDomainRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/applications/{id}/domains", s.domains)
	m.HandleFunc("POST /api/v1/applications/{id}/domains", s.beginDomain)
	m.HandleFunc("POST /api/v1/applications/{id}/domains/{hostname}/verify", s.verifyDomain)
	m.HandleFunc("DELETE /api/v1/applications/{id}/domains/{hostname}", s.discardDomain)
}
func (s *Server) domains(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	items, err := s.Store.DomainVerifications(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	approved, err := s.Store.ApprovedDomains(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	views := []domainView{}
	for _, d := range items {
		views = append(views, s.domainView(a, d, approved))
	}
	write(w, 200, map[string]any{"items": views, "expected_revision": a.Revision})
}
func (s *Server) beginDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		Hostname string `json:"hostname"`
		Service  string `json:"service"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.BeginDomainVerification(r.Context(), who(r), a, strings.ToLower(strings.TrimSpace(in.Hostname)), in.Service)
	if err != nil {
		failure(w, err)
		return
	}
	approved, err := s.Store.ApprovedDomains(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, s.domainView(a, d, approved))
}
func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	items, err := s.Store.DomainVerifications(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	for _, d := range items {
		if d.Hostname != r.PathValue("hostname") {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		lookup := net.DefaultResolver.LookupTXT
		if s.domainLookupTXT != nil {
			lookup = s.domainLookupTXT
		}
		records, err := lookup(ctx, "_hakopod."+d.Hostname)
		matched := false
		if err == nil && len(records) <= 100 {
			for _, value := range records {
				if subtle.ConstantTimeCompare([]byte(value), []byte(d.Token)) == 1 {
					matched = true
				}
			}
		}
		if !matched {
			problem(w, 409, "dns_not_verified", "The expected TXT record is not visible yet. Keep your values and retry after DNS has updated.")
			return
		}
		if err = s.Store.ConfirmDomainVerification(r.Context(), who(r), a, d); err != nil {
			failure(w, err)
			return
		}
		approved, err := s.Store.ApprovedDomains(r.Context(), a.ID)
		if err != nil {
			failure(w, err)
			return
		}
		now := time.Now()
		d.VerifiedAt = &now
		write(w, 200, s.domainView(a, d, approved))
		return
	}
	problem(w, 404, "not_found", "Create a domain verification first")
}

func (s *Server) discardDomain(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.DiscardDomainProof(r.Context(), a.ID, r.PathValue("hostname"), in.ExpectedRevision); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]bool{"discarded": true})
}
