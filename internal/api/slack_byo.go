package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type slackCloudSourceInput struct {
	SourceID    string `json:"source_id"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
}
type slackCloudAckInput struct {
	DeliveryID string `json:"delivery_id"`
	LeaseID    string `json:"lease_id"`
	Outcome    string `json:"outcome"`
}
type slackCloudClaim struct {
	DeliveryID string          `json:"delivery_id"`
	LeaseID    string          `json:"lease_id"`
	Event      SlackCloudEvent `json:"event"`
}

// requireSlackCloudRelayPrincipal accepts only the dedicated, scoped relay
// credential. It cannot administer an installation or access another scope.
func (s *Server) requireSlackCloudRelayPrincipal(w http.ResponseWriter, r *http.Request) bool {
	p := who(r)
	if p.CredentialType != "machine" || p.RuntimeScoped || p.Project == "" || p.Environment == "" || p.Application != "" || !p.Allows("slack:relay", p.Project, p.Environment, "") || len(p.Permissions) != 1 || p.Permissions[0] != "slack:relay" {
		failure(w, store.ErrForbidden)
		return false
	}
	if !s.slackBYORelayNode(w) {
		return false
	}
	return true
}

// slackBYORelayNode is a customer-managed Cloud node. It does not hold a
// signed installation Slack license: Cloud verifies the workspace Pro
// entitlement before accepting a forwarded event.
func (s *Server) slackBYORelayNode(w http.ResponseWriter) bool {
	if s.Auth.DeploymentMode != cluster.DeploymentManagedCloud || s.CloudControlPlane {
		problem(w, http.StatusNotFound, "not_found", "Cloud event relays are available only on managed Cloud nodes.")
		return false
	}
	return true
}

func (s *Server) slackCloudRelaySource(ctx context.Context, p store.Principal) (store.SlackCloudEventSource, error) {
	source, err := s.Store.SlackCloudEventSource(ctx)
	if err != nil {
		return source, err
	}
	if source.Project != p.Project || source.Environment != p.Environment {
		return source, store.ErrForbidden
	}
	return source, nil
}

func (s *Server) slackCloudBridgeSource(w http.ResponseWriter, r *http.Request) {
	// An administrator configures the source through the browser while issuing
	// a short-lived relay key. The relay itself can only claim and acknowledge.
	if !s.requireSlackAdmin(w, r) {
		return
	}
	if !s.slackBYORelayNode(w) {
		return
	}
	var input slackCloudSourceInput
	if !decode(w, r, &input) {
		return
	}
	if err := s.Store.ConfigureSlackCloudRelay(r.Context(), input.SourceID, input.Project, input.Environment); err != nil {
		authFailure(w, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"configured": true})
}

func (s *Server) slackCloudBridgeClaim(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackCloudRelayPrincipal(w, r) {
		return
	}
	source, err := s.slackCloudRelaySource(r.Context(), who(r))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusConflict, "slack_cloud_source_unconfigured", "Configure the Cloud event source before claiming events.")
		return
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	job, err := s.Store.ClaimSlackCloudEventForSource(r.Context(), source)
	if err != nil {
		authFailure(w, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	event, err := cloudSlackEvent(*job)
	if err != nil {
		if err = s.Store.FinishSlackCloudEventForSource(r.Context(), source, *job, "skipped", "Cloud event payload is invalid."); err != nil {
			authFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	write(w, http.StatusOK, slackCloudClaim{DeliveryID: strconv.FormatInt(job.Sequence, 10), LeaseID: job.LeaseID, Event: event})
}

func (s *Server) slackCloudBridgeAck(w http.ResponseWriter, r *http.Request) {
	if !s.requireSlackCloudRelayPrincipal(w, r) {
		return
	}
	var input slackCloudAckInput
	if !decode(w, r, &input) {
		return
	}
	sequence, err := strconv.ParseInt(input.DeliveryID, 10, 64)
	if err != nil || sequence < 1 || len(input.LeaseID) != 32 {
		problem(w, http.StatusBadRequest, "invalid_request", "Delivery and lease identifiers are invalid.")
		return
	}
	if _, err = hex.DecodeString(input.LeaseID); err != nil {
		problem(w, http.StatusBadRequest, "invalid_request", "Delivery and lease identifiers are invalid.")
		return
	}
	outcome, message := "", ""
	switch input.Outcome {
	case "accepted":
		outcome = "sent"
	case "retry":
		outcome, message = "pending", "Cloud event delivery failed; retry scheduled."
	case "skipped":
		outcome, message = "skipped", "Cloud did not map this event to an eligible workspace."
	default:
		problem(w, http.StatusBadRequest, "invalid_request", "Outcome must be accepted, retry, or skipped.")
		return
	}
	source, err := s.slackCloudRelaySource(r.Context(), who(r))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusConflict, "slack_cloud_source_unconfigured", "Configure the Cloud event source before acknowledging events.")
		return
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	if err = s.Store.AckSlackCloudEventLease(r.Context(), source, sequence, input.LeaseID, outcome, message); err != nil {
		authFailure(w, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"acknowledged": true})
}
