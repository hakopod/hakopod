package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/hakopod/hakopod/internal/spec"
)

type runtimeActionInput struct {
	ExpectedRevision *int64 `json:"expected_revision"`
	Replicas         *int32 `json:"replicas,omitempty"`
}

func (s *Server) runtimeAction(w http.ResponseWriter, r *http.Request, action string) {
	restart := action == "restart"
	power := action == "stop" || action == "resume"
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	var in runtimeActionInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision == nil {
		problem(w, 400, "missing_revision", "expected_revision is required")
		return
	}
	if (restart || power) && in.Replicas != nil || !restart && !power && (in.Replicas == nil || *in.Replicas < 1 || *in.Replicas > 20) {
		problem(w, 400, "invalid_request", "restart accepts no replicas; scale requires replicas between 1 and 20")
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 {
		problem(w, 400, "invalid_request", "Idempotency-Key must contain 8–128 characters")
		return
	}
	base, err := s.Store.RuntimeBase(r.Context(), a.ID, *in.ExpectedRevision)
	if err != nil {
		failure(w, err)
		return
	}
	if base.RecoveryState == "succeeded" && base.RecoverySpec != nil {
		base.Spec = *base.RecoverySpec
		base.ResolvedSpec = base.RecoverySpec
		base.Status = "succeeded"
	}
	if (base.Status != "succeeded" && !(action == "stop" && base.Status == "failed")) || base.ResolvedSpec == nil {
		problem(w, 409, "revision_not_ready", "This action requires a completed resolved deployment. Stop also supports failed resolved deployments; finish or cancel an active rollout first.")
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
	artifact := resolved.Services[name]
	if svc.Job != nil && (svc.Job.Schedule == nil || !power) {
		problem(w, 409, "deployment_job", "Deployment jobs do not support runtime actions")
		return
	}
	if power {
		svc.Suspended = action == "stop"
		artifact.Suspended = svc.Suspended
	} else if restart {
		nonce := fmt.Sprintf("%x", sha256.Sum256([]byte(who(r).ID+"\x00"+a.ID+"\x00"+name+"\x00"+idem)))
		svc.RestartNonce = nonce
		artifact.RestartNonce = nonce
	} else {
		if svc.Autoscaling != nil {
			problem(w, 409, "autoscaling_enabled", "HPA owns replicas; update autoscaling in the application specification first")
			return
		}
		svc.Replicas = *in.Replicas
		artifact.Replicas = *in.Replicas
	}
	next.Services[name] = svc
	resolved.Services[name] = artifact
	next, err = spec.Normalize(next)
	if err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	d, err := s.Store.Accept(r.Context(), who(r), a.Project, a.Environment, next, *in.ExpectedRevision, idem, resolved)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, d)
}
func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "restart")
}
func (s *Server) scaleService(w http.ResponseWriter, r *http.Request) { s.runtimeAction(w, r, "scale") }

func (s *Server) stopService(w http.ResponseWriter, r *http.Request) { s.runtimeAction(w, r, "stop") }
func (s *Server) resumeService(w http.ResponseWriter, r *http.Request) {
	s.runtimeAction(w, r, "resume")
}
