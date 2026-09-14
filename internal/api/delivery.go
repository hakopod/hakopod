package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) validateDeliveryPlan(w http.ResponseWriter, r *http.Request, project, environment string, next spec.Application, existing *store.Application) bool {
	if s.Cluster == nil && s.Auth.DeploymentMode != cluster.DeploymentManagedCloud && !spec.HasDeliveryCapabilities(next) {
		return true
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured for public TCP, certificates or workload identities")
		return false
	}
	target := cluster.Target{Project: project, Environment: environment, Spec: next}
	if existing != nil {
		target.ApplicationID = existing.ID
	}
	report, err := s.Cluster.ValidateDeliveryWithReport(r.Context(), target)
	if err != nil {
		problem(w, 409, "delivery_unavailable", err.Error())
		return false
	}
	*r = *r.WithContext(context.WithValue(r.Context(), deliveryPreflightKey{}, report))
	return true
}

type deliveryPreflightKey struct{}

func deliveryWarnings(r *http.Request, next spec.Application) []string {
	warnings := spec.Warnings(next)
	if report, ok := r.Context().Value(deliveryPreflightKey{}).(cluster.PreflightReport); ok {
		for _, check := range report.Checks {
			if check.Status == "unknown" {
				warnings = append(warnings, check.Message)
			}
		}
	}
	return warnings
}

func (s *Server) serviceDelivery(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:read")
	if !ok {
		return
	}
	name := r.PathValue("service")
	if _, ok := a.Spec.Services[name]; !ok {
		problem(w, 404, "not_found", "service not found")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "unavailable", "Kubernetes is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	target := cluster.Target{ApplicationID: a.ID, Project: a.Project, Environment: a.Environment, Spec: a.Spec, Revision: a.Revision}
	listeners, err := s.Cluster.ObservePublicTCP(ctx, target, name)
	if err != nil {
		problem(w, 503, "unavailable", "TCP listener observations are unavailable")
		return
	}
	identity, err := s.Cluster.AWSIdentityStatus(ctx, target, name)
	if err != nil {
		problem(w, 503, "unavailable", "Workload identity observations are unavailable")
		return
	}
	write(w, 200, map[string]any{"public_tcp": listeners, "public_tcp_policy": s.Cluster.PublicTCPPolicy(), "aws_identity": identity, "observed_at": time.Now().UTC()})
}
