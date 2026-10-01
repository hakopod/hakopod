package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerDatabasePublicEndpointRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/databases/{id}/public-endpoint-capabilities", s.databasePublicEndpointCapabilities)
	mux.HandleFunc("GET /api/v1/databases/{id}/public-endpoints", s.databasePublicEndpoints)
	mux.HandleFunc("POST /api/v1/databases/{id}/public-endpoint-plan", s.databasePublicEndpointPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/public-endpoints", s.publishDatabasePublicEndpoint)
	mux.HandleFunc("DELETE /api/v1/databases/{id}/public-endpoints/{endpoint}", s.revokeDatabasePublicEndpoint)
	mux.HandleFunc("GET /api/v1/database-public-endpoint-operations/{id}", s.databasePublicEndpointOperation)
}

func (s *Server) databasePublicEndpointCapabilities(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	capabilities := database.PublicEndpointCapabilitiesFor(d.Spec)
	if capabilities.Available {
		if s.Cluster == nil {
			capabilities.Available = false
			capabilities.UnavailableReason = "Database public endpoints are not configured."
		} else {
			capabilities = s.Cluster.DatabasePublicEndpointCapabilities(d.Spec)
		}
	}
	write(w, http.StatusOK, capabilities)
}

func (s *Server) databasePublicEndpoints(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.DatabasePublicEndpoints(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) databasePublicEndpointPlan(w http.ResponseWriter, r *http.Request) {
	var input database.PublicEndpointSpec
	if !decode(w, r, &input) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if err = database.PublicEndpointAvailability(d.Spec); err != nil {
		problem(w, http.StatusConflict, "database_public_endpoints_unavailable", err.Error())
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_public_endpoints_unavailable", "Database public endpoints are not configured.")
		return
	}
	capabilities := s.Cluster.DatabasePublicEndpointCapabilities(d.Spec)
	if !capabilities.Available {
		problem(w, http.StatusConflict, "database_public_endpoints_unavailable", capabilities.UnavailableReason)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	observed, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil {
		problem(w, 503, "database_observation_unavailable", "A current verified database observation is required.")
		return
	}
	d.Observation = observed
	_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
	allocations := s.Cluster.DatabasePublicEndpointAllocations()
	if len(allocations) == 0 {
		problem(w, 409, "database_public_endpoints_disabled", "The installation operator has not provisioned database public endpoints.")
		return
	}
	authorityFingerprint := ""
	if s.DatabasePublicEndpointAuthority != nil {
		authorityFingerprint, err = s.DatabasePublicEndpointAuthority.Preflight(ctx, d, allocations)
		if err != nil {
			problem(w, 409, "database_public_endpoint_authority", err.Error())
			return
		}
	} else {
		// Self-hosted installations continue to require every operator-provided
		// hostname to resolve before a review can reserve an allocation.
		for _, allocation := range allocations {
			if err = s.Cluster.VerifyDatabasePublicEndpointDNS(ctx, allocation); err != nil {
				problem(w, 409, "database_public_endpoint_dns", err.Error())
				return
			}
		}
	}
	reviewID, plan, err := s.Store.ReserveDatabasePublicEndpointReview(ctx, who(r), d, input, allocations, time.Now().UTC(), authorityFingerprint)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"id": reviewID, "plan": plan})
}

func (s *Server) publishDatabasePublicEndpoint(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ReviewID                 string `json:"review_id"`
		ExpectedDatabaseRevision int64  `json:"expected_database_revision"`
		ExpectedEndpointRevision int64  `json:"expected_endpoint_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptDatabasePublicEndpoint(r.Context(), who(r), r.PathValue("id"), input.ReviewID, idem, input.ExpectedDatabaseRevision, input.ExpectedEndpointRevision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, op)
}

func (s *Server) revokeDatabasePublicEndpoint(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedEndpointRevision int64 `json:"expected_endpoint_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.RevokeDatabasePublicEndpoint(r.Context(), who(r), r.PathValue("id"), r.PathValue("endpoint"), idem, input.ExpectedEndpointRevision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, op)
}

func (s *Server) databasePublicEndpointOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Store.DatabasePublicEndpointOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, op)
}

func databasePublicEndpointIdentityStarted(phase string) bool {
	switch phase {
	case "identity", "identity_issuing", "identity_rolling", "identity_converging", "tls", "provider", "publishing", "observing", "identity_closing":
		return true
	}
	return false
}

func databasePublicEndpointProviderOwned(phase string) bool {
	switch phase {
	case "provider", "publishing", "observing", "failing_provider", "cancelling_provider", "provider_delete":
		return true
	}
	return false
}

func databasePublicEndpointRouteMayBePublished(phase string) bool {
	return phase == "publishing" || phase == "observing"
}

func databasePublicEndpointHealthRetryPhase(phase string) string {
	if databasePublicEndpointIdentityStarted(phase) {
		return "identity"
	}
	return "health"
}

func databasePublicEndpointReviewIdentityChanged(d database.Resource, observed database.Observation, review *database.PublicEndpointReview) bool {
	return review != nil && (d.Revision != review.DatabaseRevision || d.Status != "ready" || d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) || observed.TopologyFingerprint != "" && observed.TopologyFingerprint != review.TopologyFingerprint)
}

func databasePublicEndpointIdentityConverging(d database.Resource, observed database.Observation, review *database.PublicEndpointReview, phase string) bool {
	if !databasePublicEndpointIdentityStarted(phase) || review == nil || databasePublicEndpointReviewIdentityChanged(d, observed, review) {
		return false
	}
	return observed.Status != "ready" || observed.TLS == nil || !observed.TLS.Verified || !observed.TLS.PlaintextRejected || observed.TLS.Fingerprint == ""
}

func databaseOracleFreePublicEndpointTransition(d database.Resource) bool {
	return d.Spec.Engine == "oracle" && d.Spec.Oracle != nil && d.Spec.Oracle.Edition == "free" && d.Spec.Mode == "standalone" && d.Spec.Replicas == 0 && d.Spec.Shards == 1 && d.Spec.TLSRequired()
}

func oraclePublicEndpointMemberTopology(member database.PublicEndpointTransitionMember) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(member.Name+":"+member.UID)))
}

type databasePublicEndpointRuntime interface {
	ObserveDatabase(context.Context, database.Resource) (database.Observation, error)
	PrepareDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint, func() error) error
	RemoveDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint, func() error) error
	ReleaseDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint, func() error) error
	ValidateDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint) error
	ObserveDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint) (database.PublicEndpointObservation, error)
	ReconcileDatabasePublicEndpointAccess(context.Context, database.Resource, []string, bool, func() error) error
	VerifyDatabasePublicEndpointBackend(context.Context, database.Resource, database.PublicEndpoint) error
	VerifyDatabasePublicEndpointDNS(context.Context, database.PublicEndpointAllocation) error
	ReconcileDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint, func() error) error
}

func verifyDatabasePublicEndpointDNS(ctx context.Context, runtime databasePublicEndpointRuntime, endpoint database.PublicEndpoint) error {
	members, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	for _, member := range members {
		if err = runtime.VerifyDatabasePublicEndpointDNS(ctx, member.Allocation); err != nil {
			return err
		}
	}
	return nil
}

type databasePublicEndpointPublicationValidator interface {
	ValidateDatabasePublicEndpointPublication(database.Resource) error
}

// DatabasePublicEndpointAuthority owns the managed service resources outside
// Kubernetes. Preflight is read-only. Every mutation receives the durable
// engine operation and an authority fence that must run immediately before a
// provider write.
type DatabasePublicEndpointAuthority interface {
	Preflight(context.Context, database.Resource, []database.PublicEndpointAllocation) (string, error)
	Ensure(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint, func() error) error
	Verify(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint) error
	Delete(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint, func() error) error
	VerifyDeleted(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint) error
}

func (s *Server) ensureDatabasePublicEndpointAuthority(ctx context.Context, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	if s.DatabasePublicEndpointAuthority == nil {
		return nil
	}
	if operation.Review == nil || operation.Review.AuthorityFingerprint == "" {
		return fmt.Errorf("managed database public endpoint review has no authority fingerprint")
	}
	if err := s.DatabasePublicEndpointAuthority.Ensure(ctx, operation, d, endpoint, before); err != nil {
		return err
	}
	return s.DatabasePublicEndpointAuthority.Verify(ctx, operation, d, endpoint)
}

func (s *Server) deleteDatabasePublicEndpointAuthority(ctx context.Context, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	if s.DatabasePublicEndpointAuthority == nil {
		return nil
	}
	if err := s.DatabasePublicEndpointAuthority.Delete(ctx, operation, d, endpoint, before); err != nil {
		return err
	}
	return s.DatabasePublicEndpointAuthority.VerifyDeleted(ctx, operation, d, endpoint)
}

type oraclePublicEndpointTransitionRuntime interface {
	BeginOraclePublicEndpointIdentityTransition(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint, []string) (database.PublicEndpointIdentityTransition, error)
	IssueOraclePublicEndpointIdentity(context.Context, database.Resource, database.PublicEndpointIdentityTransition, bool, func() error) (database.PublicEndpointIdentityTransition, error)
	RollOraclePublicEndpointIdentity(context.Context, database.Resource, database.PublicEndpointIdentityTransition, func() error) error
	ConvergeOraclePublicEndpointIdentity(context.Context, database.Resource, database.PublicEndpoint, database.Observation, database.PublicEndpointIdentityTransition) (database.PublicEndpointIdentityTransition, bool, error)
}

func oraclePublicEndpointTransitionValid(operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition, requireIssued, requireFinal bool) bool {
	kindValid := transition != nil && (transition.Kind == operation.Kind || operation.Kind == "publish" && operation.Phase == "cancelling_identity" && transition.Kind == "cancel")
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil || transition == nil || !kindValid || transition.SchemaVersion != database.PublicEndpointIdentityTransitionSchemaVersion || transition.OperationID != operation.ID || transition.Engine != "oracle" || transition.DatabaseRevision != d.Revision || transition.EndpointRevision != operation.Revision || endpoint.Revision != operation.Revision || transition.RouteFingerprint != route.Fingerprint() || transition.StatefulSetUID == "" || transition.OriginalGeneration < 1 || transition.OriginalTemplateHash == "" || transition.OldMember.Name == "" || transition.OldMember.UID == "" || len(transition.PVCs) != 2 {
		return false
	}
	if transition.Kind == "publish" && (operation.Review == nil || transition.RouteFingerprint != operation.Review.RouteFingerprint || transition.ReviewedTopologyFingerprint != operation.Review.TopologyFingerprint || transition.ReviewedLeafFingerprint != operation.Review.TLSFingerprint) {
		return false
	}
	if transition.ReviewedTopologyFingerprint != oraclePublicEndpointMemberTopology(transition.OldMember) {
		return false
	}
	names, err := database.NormalizePublicEndpointNames(transition.DesiredNames)
	if err != nil || !slices.Equal(names, transition.DesiredNames) {
		return false
	}
	wantedPVCs := map[string]bool{"data-database-0": false, "backup-database-0": false}
	for _, pvc := range transition.PVCs {
		if _, ok := wantedPVCs[pvc.Name]; !ok || wantedPVCs[pvc.Name] || pvc.UID == "" || pvc.VolumeName == "" || pvc.PersistentVolumeUID == "" || pvc.BackingVolumeFingerprint == "" {
			return false
		}
		wantedPVCs[pvc.Name] = true
	}
	if requireIssued && (transition.ProposedLeafFingerprint == "" || transition.ProposedCAFingerprint == "" || transition.ProposedCAFingerprint != transition.ReviewedCAFingerprint || transition.TargetTemplateHash == "" || transition.TargetGeneration != transition.OriginalGeneration+1) {
		return false
	}
	if requireFinal && (transition.FinalMember == nil || transition.FinalMember.Name != transition.OldMember.Name || transition.FinalMember.UID == "" || transition.FinalMember.UID == transition.OldMember.UID || transition.FinalTopologyFingerprint != oraclePublicEndpointMemberTopology(*transition.FinalMember) || transition.ServedLeafFingerprint != transition.ProposedLeafFingerprint || transition.ServedCAFingerprint != transition.ProposedCAFingerprint) {
		return false
	}
	return true
}

func (s *Server) reconcileOraclePublicEndpointTransition(ctx context.Context, runtime databasePublicEndpointRuntime, oracleRuntime oraclePublicEndpointTransitionRuntime, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, before, cleanupBefore func() error, finish func(string, string, string, database.PublicEndpointObservation)) {
	observed, observeErr := runtime.ObserveDatabase(ctx, d)
	if observeErr == nil {
		d.Observation = observed
		_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
	}
	fail := func(phase, message string) {
		if databasePublicEndpointRouteMayBePublished(operation.Phase) {
			if err := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
				finish("queued", operation.Phase, "Oracle Free public identity changed while publishing. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(operation.Phase) {
			finish("queued", "failing_provider", "The Oracle Free publication failed. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("failed", phase, message, endpoint.Observation)
	}
	retry := func(message string) {
		if databasePublicEndpointRouteMayBePublished(operation.Phase) {
			if err := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
				finish("queued", operation.Phase, "Oracle Free publication changed while serving. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		phase := "tls"
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(operation.Phase) {
			phase = "provider"
		}
		finish("queued", phase, message, endpoint.Observation)
	}
	if operation.Review == nil || d.Revision != operation.Review.DatabaseRevision || d.Status != "ready" || d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) {
		fail("review", "The reviewed Oracle Free database state changed. The route remains closed.")
		return
	}
	if operation.Phase == "health" {
		if observeErr != nil || observed.Status != "ready" || observed.TopologyFingerprint != operation.Review.TopologyFingerprint || observed.TLS == nil || observed.TLS.Fingerprint != operation.Review.TLSFingerprint {
			finish("queued", "health", "Waiting for the exact reviewed Oracle Free member and TLS identity while the route remains closed.", endpoint.Observation)
			return
		}
		if err := runtime.ValidateDatabasePublicEndpoint(ctx, d, endpoint); err != nil {
			fail("allocation", "The operator allocation or DNS binding no longer matches the review. The route remains closed.")
			return
		}
		names, err := s.Store.DatabasePublicEndpointNames(ctx, d.ID, "")
		if err != nil {
			return
		}
		if !slices.Contains(names, endpoint.Allocation.Host) {
			names = append(names, endpoint.Allocation.Host)
		}
		transition, err := oracleRuntime.BeginOraclePublicEndpointIdentityTransition(ctx, operation, d, endpoint, names)
		if err != nil {
			fail("review", "The reviewed Oracle Free workload, member, or persistent volumes changed. The route remains closed.")
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, transition); err != nil {
			return
		}
		finish("queued", "identity_issuing", "The Oracle Free identity intent is recorded. Issuing the exact reviewed certificate while the route remains closed.", endpoint.Observation)
		return
	}
	transition := operation.IdentityTransition
	issued := operation.Phase == "identity_rolling" || operation.Phase == "identity_converging" || operation.Phase == "tls" || operation.Phase == "provider" || operation.Phase == "publishing" || operation.Phase == "observing"
	final := operation.Phase == "tls" || operation.Phase == "provider" || operation.Phase == "publishing" || operation.Phase == "observing"
	if !oraclePublicEndpointTransitionValid(operation, d, endpoint, transition, issued, final) {
		fail("transition", "The persisted Oracle Free identity transition is invalid. The route remains closed.")
		return
	}
	switch operation.Phase {
	case "identity_issuing":
		updated, err := oracleRuntime.IssueOraclePublicEndpointIdentity(ctx, d, *transition, true, before)
		if err != nil {
			finish("queued", "identity_issuing", "Waiting for the namespace-owned Oracle Free certificate to match its durable intent.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, updated); err != nil {
			return
		}
		finish("queued", "identity_rolling", "The issued Oracle Free leaf and exact target template are recorded. Starting the singleton replacement.", endpoint.Observation)
		return
	case "identity_rolling":
		if err := oracleRuntime.RollOraclePublicEndpointIdentity(ctx, d, *transition, before); err != nil {
			finish("queued", "identity_rolling", "Waiting for the exact Oracle Free StatefulSet rollout to be accepted.", endpoint.Observation)
			return
		}
		finish("queued", "identity_converging", "The Oracle Free rollout is recorded. Waiting for the replacement member and native TCPS proof.", endpoint.Observation)
		return
	case "identity_converging":
		if observeErr != nil {
			finish("queued", "identity_converging", "Waiting for a current Oracle Free replacement observation.", endpoint.Observation)
			return
		}
		updated, ready, err := oracleRuntime.ConvergeOraclePublicEndpointIdentity(ctx, d, endpoint, observed, *transition)
		if err != nil {
			fail("transition", "The Oracle Free StatefulSet, member, volume, or TLS identity changed during replacement. The route remains closed.")
			return
		}
		if !ready {
			finish("queued", "identity_converging", "Waiting for the exact Oracle Free replacement member and native TCPS proof.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, updated); err != nil {
			return
		}
		finish("queued", "tls", "The replacement member, persistent volumes, APP database identity, and served TCPS certificate are recorded.", endpoint.Observation)
		return
	case "tls", "provider", "publishing", "observing":
		if observeErr != nil {
			retry("Waiting for a current Oracle Free member and served TCPS observation while the route remains closed.")
			return
		}
		confirmed, ready, err := oracleRuntime.ConvergeOraclePublicEndpointIdentity(ctx, d, endpoint, observed, *transition)
		if err != nil || !ready || !reflect.DeepEqual(confirmed, *transition) {
			fail("transition", "The converged Oracle Free StatefulSet, member, volumes, or served TCPS identity changed. The route remains closed.")
			return
		}
		d.Observation, d.PublicEndpointNames, d.PublicEndpointAccess = observed, append([]string(nil), transition.DesiredNames...), true
		if err := runtime.ValidateDatabasePublicEndpoint(ctx, d, endpoint); err != nil {
			fail("allocation", "The operator allocation or DNS binding no longer matches the review. The route remains closed.")
			return
		}
		if err := runtime.VerifyDatabasePublicEndpointBackend(ctx, d, endpoint); err != nil {
			retry("Waiting for the Oracle Free native TCPS proof while the route remains closed.")
			return
		}
		if err := before(); err != nil {
			return
		}
		if s.DatabasePublicEndpointAuthority != nil && operation.Phase == "tls" {
			finish("queued", "provider", "The Oracle Free identity is ready. Preparing the owned public DNS resource.", endpoint.Observation)
			return
		}
		if s.DatabasePublicEndpointAuthority != nil {
			if err := s.ensureDatabasePublicEndpointAuthority(ctx, operation, d, endpoint, before); err != nil {
				retry("Waiting for the reviewed Oracle Free endpoint provider resources while the route remains closed.")
				return
			}
		}
		if err := runtime.VerifyDatabasePublicEndpointDNS(ctx, endpoint.Allocation); err != nil {
			fail("dns", "The public hostname changed before publication. The route remains closed.")
			return
		}
		if operation.Phase == "observing" {
			result, err := runtime.ObserveDatabasePublicEndpoint(ctx, d, endpoint)
			if err != nil || !result.Configured {
				endpoint.Observation = result
				retry("The Oracle Free route observation changed. Rechecking publication from the closed provider-owned state.")
				return
			}
			finish("succeeded", "configured", "", result)
			return
		}
		if operation.Phase == "tls" || operation.Phase == "provider" {
			finish("queued", "publishing", "Publishing the reviewed Oracle Free TCPS route to every owned HAProxy worker.", endpoint.Observation)
			return
		}
		if err := runtime.ReconcileDatabasePublicEndpoint(ctx, d, endpoint, before); err != nil {
			retry("Waiting to republish the reviewed Oracle Free TCPS route from the closed provider-owned state.")
			return
		}
		finish("queued", "observing", "The accepted HAProxy reload is recorded. Verifying the configured Oracle Free TCPS route.", endpoint.Observation)
	}
}

func (s *Server) reconcileOraclePublicEndpointRevocationTransition(ctx context.Context, runtime databasePublicEndpointRuntime, oracleRuntime oraclePublicEndpointTransitionRuntime, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, before func() error, finish func(string, string, string, database.PublicEndpointObservation)) {
	observed, observeErr := runtime.ObserveDatabase(ctx, d)
	if observeErr == nil {
		d.Observation = observed
		_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
	}
	if d.Status != "ready" || d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) {
		finish("failed", "transition", "Oracle Free identity cleanup requires an inspected ready database. The public route is closed.", endpoint.Observation)
		return
	}
	if operation.Phase == "identity" {
		if observeErr != nil || observed.Status != "ready" || observed.TLS == nil || observed.TopologyFingerprint == "" || observed.TLS.Fingerprint == "" {
			finish("queued", "identity", "The route is closed. Waiting for the current Oracle Free member before identity cleanup.", endpoint.Observation)
			return
		}
		names, err := s.Store.DatabasePublicEndpointNames(ctx, d.ID, endpoint.ID)
		if err != nil {
			return
		}
		transition, err := oracleRuntime.BeginOraclePublicEndpointIdentityTransition(ctx, operation, d, endpoint, names)
		if err != nil {
			finish("failed", "transition", "The Oracle Free workload, member, or persistent volumes changed after route closure.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, transition); err != nil {
			return
		}
		finish("queued", "identity_issuing", "The Oracle Free cleanup intent is recorded. Issuing the private-only certificate.", endpoint.Observation)
		return
	}
	transition := operation.IdentityTransition
	issued := operation.Phase == "identity_rolling" || operation.Phase == "identity_converging" || operation.Phase == "release"
	final := operation.Phase == "release"
	if !oraclePublicEndpointTransitionValid(operation, d, endpoint, transition, issued, final) {
		finish("failed", "transition", "The persisted Oracle Free identity cleanup transition is invalid. The public route is closed.", endpoint.Observation)
		return
	}
	switch operation.Phase {
	case "identity_issuing":
		updated, err := oracleRuntime.IssueOraclePublicEndpointIdentity(ctx, d, *transition, true, before)
		if err != nil {
			finish("queued", "identity_issuing", "Waiting for the namespace-owned Oracle Free cleanup certificate.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, updated); err != nil {
			return
		}
		finish("queued", "identity_rolling", "The cleanup leaf and exact target template are recorded. Starting the singleton replacement.", endpoint.Observation)
	case "identity_rolling":
		if err := oracleRuntime.RollOraclePublicEndpointIdentity(ctx, d, *transition, before); err != nil {
			finish("queued", "identity_rolling", "Waiting for the exact Oracle Free cleanup rollout to be accepted.", endpoint.Observation)
			return
		}
		finish("queued", "identity_converging", "The cleanup rollout is recorded. Waiting for the replacement member and native TCPS proof.", endpoint.Observation)
	case "identity_converging":
		if observeErr != nil {
			finish("queued", "identity_converging", "Waiting for a current Oracle Free cleanup observation.", endpoint.Observation)
			return
		}
		updated, ready, err := oracleRuntime.ConvergeOraclePublicEndpointIdentity(ctx, d, endpoint, observed, *transition)
		if err != nil {
			finish("failed", "transition", "The Oracle Free StatefulSet, member, volume, or TLS identity changed during cleanup. The public route is closed.", endpoint.Observation)
			return
		}
		if !ready {
			finish("queued", "identity_converging", "Waiting for the exact Oracle Free cleanup replacement member.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, updated); err != nil {
			return
		}
		next := "release"
		if s.DatabasePublicEndpointAuthority != nil {
			next = "provider_delete"
		}
		finish("queued", next, "The Oracle Free identity cleanup is verified. Releasing owned provider resources.", endpoint.Observation)
	}
}

func (s *Server) reconcileCancelledOraclePublicEndpointTransition(ctx context.Context, runtime databasePublicEndpointRuntime, oracleRuntime oraclePublicEndpointTransitionRuntime, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, cleanupBefore func() error, finish func(string, string, string, database.PublicEndpointObservation)) {
	transition := operation.IdentityTransition
	if !oraclePublicEndpointTransitionValid(operation, d, endpoint, transition, transition != nil && transition.ProposedLeafFingerprint != "", transition != nil && transition.FinalMember != nil) {
		finish("failed", "transition", "The Oracle Free transition proof is invalid after authority revocation. The public route is closed.", endpoint.Observation)
		return
	}
	if transition.ProposedLeafFingerprint == "" {
		updated, err := oracleRuntime.IssueOraclePublicEndpointIdentity(ctx, d, *transition, transition.Kind == "cancel", cleanupBefore)
		if err != nil {
			finish("queued", "cancelling_identity", "Authority changed. Waiting for the recorded Oracle Free identity change to finish safely.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, updated); err != nil {
			return
		}
		finish("queued", "cancelling_identity", "Authority changed. The issued Oracle Free identity is recorded; waiting for its exact rollout.", endpoint.Observation)
		return
	}
	if transition.FinalMember == nil {
		if err := oracleRuntime.RollOraclePublicEndpointIdentity(ctx, d, *transition, cleanupBefore); err != nil {
			finish("queued", "cancelling_identity", "Authority changed. Waiting for the recorded Oracle Free rollout to finish.", endpoint.Observation)
			return
		}
		observed, err := runtime.ObserveDatabase(ctx, d)
		if err != nil {
			finish("queued", "cancelling_identity", "Authority changed. Waiting for the Oracle Free replacement observation.", endpoint.Observation)
			return
		}
		d.Observation = observed
		_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
		updated, ready, err := oracleRuntime.ConvergeOraclePublicEndpointIdentity(ctx, d, endpoint, observed, *transition)
		if err != nil {
			finish("failed", "transition", "The Oracle Free identity changed during fenced cancellation. The public route is closed.", endpoint.Observation)
			return
		}
		if !ready {
			finish("queued", "cancelling_identity", "Authority changed. Waiting for the exact Oracle Free replacement to converge.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, updated); err != nil {
			return
		}
		finish("queued", "cancelling_identity", "Authority changed. The Oracle Free identity transition is proven; preparing the prior private identity and policy.", endpoint.Observation)
		return
	}
	if transition.Kind != "cancel" {
		observed, err := runtime.ObserveDatabase(ctx, d)
		if err != nil {
			finish("queued", "cancelling_identity", "Authority changed. Waiting for the current Oracle Free member before private identity cleanup.", endpoint.Observation)
			return
		}
		d.Observation = observed
		_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
		names, err := s.Store.DatabasePublicEndpointNames(ctx, d.ID, endpoint.ID)
		if err != nil {
			return
		}
		cleanupOperation := operation
		cleanupOperation.Kind = "cancel"
		cleanup, err := oracleRuntime.BeginOraclePublicEndpointIdentityTransition(ctx, cleanupOperation, d, endpoint, names)
		if err != nil {
			finish("queued", "cancelling_identity", "Authority changed. Waiting to record the prior Oracle Free identity and persistent-volume proof.", endpoint.Observation)
			return
		}
		if err = s.Store.SaveDatabasePublicEndpointIdentityTransition(ctx, operation, cleanup); err != nil {
			return
		}
		finish("queued", "cancelling_identity", "Authority changed. The private identity cleanup intent is recorded; removing the cancelled hostname and ingress policy.", endpoint.Observation)
		return
	}
	observed, err := runtime.ObserveDatabase(ctx, d)
	if err != nil {
		finish("queued", "cancelling_identity", "Authority changed. Waiting to recheck the private Oracle Free identity cleanup.", endpoint.Observation)
		return
	}
	d.Observation = observed
	confirmed, ready, err := oracleRuntime.ConvergeOraclePublicEndpointIdentity(ctx, d, endpoint, observed, *transition)
	if err != nil || !ready || !reflect.DeepEqual(confirmed, *transition) {
		finish("failed", "transition", "The Oracle Free private identity or policy changed after cleanup. The public route is closed.", endpoint.Observation)
		return
	}
	if s.DatabasePublicEndpointAuthority != nil {
		if err := s.deleteDatabasePublicEndpointAuthority(ctx, operation, d, endpoint, cleanupBefore); err != nil {
			finish("queued", "cancelling_provider", "Authority changed. The route and prior Oracle Free identity are closed. Waiting for the operator to restore database endpoint cleanup credentials or provider access.", endpoint.Observation)
			return
		}
	}
	finish("cancelled", "authorization", "Database public endpoint authority is no longer valid. The route is closed and the prior Oracle Free identity and ingress policy are restored.", endpoint.Observation)
}

func (s *Server) reconcileDatabasePublicEndpoint(parent context.Context) {
	s.reconcileDatabasePublicEndpointWithRuntime(parent, s.Cluster)
}

func (s *Server) reconcileDatabasePublicEndpointWithRuntime(parent context.Context, runtime databasePublicEndpointRuntime) {
	claim, cancel := context.WithTimeout(parent, 5*time.Second)
	op, err := s.Store.ClaimDatabasePublicEndpointOperation(claim)
	cancel()
	if err != nil {
		return
	}
	// The durable lease is 90 seconds. Keep one attempt strictly shorter so the
	// final state write and any retry cannot overlap this worker's mutations.
	ctx, stop := context.WithTimeout(parent, 60*time.Second)
	defer stop()
	d, err := s.Store.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return
	}
	endpoint, err := s.Store.DatabasePublicEndpointInternal(ctx, op.EndpointID)
	if err != nil {
		return
	}
	if op.Kind == "publish" && op.Review != nil {
		endpoint.Spec = op.Review.Spec
	}
	exceptEndpointID := ""
	if op.Kind == "revoke" || op.Phase == "cancelling" || op.Phase == "cancelling_provider" {
		exceptEndpointID = endpoint.ID
	}
	d.PublicEndpointMembers, err = s.Store.DatabasePublicEndpointMembers(ctx, d.ID, exceptEndpointID)
	if err != nil {
		return
	}
	finish := func(status, phase, message string, observation database.PublicEndpointObservation) {
		done, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Store.RecordDatabasePublicEndpointStep(done, op, observation, status, phase, message)
	}
	before := func() error { return s.Store.CheckDatabasePublicEndpointOperation(ctx, op) }
	cleanupBefore := func() error {
		return s.Store.CheckDatabasePublicEndpointOperationCleanup(ctx, op)
	}
	if op.Kind == "revoke" {
		// Revocation was authorized when it was durably accepted. From that
		// point onward every mutation is fail-closed cleanup, so it remains
		// fenced by operation ownership without depending on the initiating
		// credential still being valid.
		if op.Phase == "cancelling" || op.Phase == "cancelling_provider" {
			op.Phase = "identity"
		}
		if op.Phase == "cancelling_identity" {
			switch {
			case op.IdentityTransition == nil:
				op.Phase = "identity"
			case op.IdentityTransition.ProposedLeafFingerprint == "":
				op.Phase = "identity_issuing"
			case op.IdentityTransition.FinalMember == nil:
				op.Phase = "identity_rolling"
			case s.DatabasePublicEndpointAuthority != nil:
				op.Phase = "provider_delete"
			default:
				op.Phase = "release"
			}
		}
		if op.Phase == "" || op.Phase == "accepted" || op.Phase == "closing" {
			if err = runtime.RemoveDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
				finish("queued", "closing", "Waiting for the owned route and its existing sessions to close.", endpoint.Observation)
				return
			}
			finish("queued", "identity", "The route is closed. Waiting for database certificate and network policy cleanup.", endpoint.Observation)
			return
		}
		if databaseOracleFreePublicEndpointTransition(d) && op.Phase != "release" && op.Phase != "provider_delete" {
			oracleRuntime, ok := runtime.(oraclePublicEndpointTransitionRuntime)
			if !ok {
				finish("failed", "transition", "Oracle Free public identity cleanup support is unavailable. The public route is closed.", endpoint.Observation)
				return
			}
			s.reconcileOraclePublicEndpointRevocationTransition(ctx, runtime, oracleRuntime, op, d, endpoint, cleanupBefore, finish)
			return
		}
		if op.Phase == "identity" {
			names, e := s.Store.DatabasePublicEndpointNames(ctx, d.ID, endpoint.ID)
			if e != nil {
				return
			}
			if err = runtime.ReconcileDatabasePublicEndpointAccess(ctx, d, names, len(names) > 0, cleanupBefore); err != nil {
				finish("queued", "identity", "The route is closed. Waiting for database certificate and network policy cleanup.", endpoint.Observation)
				return
			}
			// Persist cleanup before deleting the closure proof. If the claim
			// deletion succeeds but the terminal write is interrupted, release is
			// an explicit durable phase and a missing claim is an idempotent retry.
			next := "release"
			if s.DatabasePublicEndpointAuthority != nil {
				next = "provider_delete"
			}
			finish("queued", next, "The route and database identity are closed. Releasing owned provider resources.", endpoint.Observation)
			return
		}
		if op.Phase == "provider_delete" {
			if err = s.deleteDatabasePublicEndpointAuthority(ctx, op, d, endpoint, cleanupBefore); err != nil {
				finish("queued", "provider_delete", "The route is closed. Waiting for the operator to restore database endpoint cleanup credentials or provider access.", endpoint.Observation)
				return
			}
			finish("queued", "release", "Owned provider resources are absent. Releasing the operator allocation.", endpoint.Observation)
			return
		}
		if op.Phase != "release" {
			finish("queued", "closing", "Waiting for the owned route and its existing sessions to close.", endpoint.Observation)
			return
		}
		if err = runtime.ReleaseDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
			finish("queued", "release", "The route and database identity are closed. Releasing the operator allocation.", endpoint.Observation)
			return
		}
		finish("succeeded", "revoked", "", database.PublicEndpointObservation{Configured: false, ExternallyVerified: false, Message: "The public endpoint is revoked."})
		return
	}
	if op.Phase == "cancelling_provider" || op.Phase == "failing_provider" {
		if err = s.deleteDatabasePublicEndpointAuthority(ctx, op, d, endpoint, cleanupBefore); err != nil {
			finish("queued", op.Phase, "The route is closed. Waiting for the operator to restore database endpoint cleanup credentials or provider access.", endpoint.Observation)
			return
		}
		if op.Phase == "cancelling_provider" {
			finish("cancelled", "authorization", "Database public endpoint authority is no longer valid. The route and owned provider resources are closed.", endpoint.Observation)
		} else {
			finish("failed", "provider", "The public endpoint failed after its route was closed and its owned provider resources were removed.", endpoint.Observation)
		}
		return
	}
	if op.Phase == "cancelling_identity" {
		if err = runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
			finish("queued", "cancelling_identity", "Authority changed. Waiting for the public route to close before Oracle Free identity cleanup.", endpoint.Observation)
			return
		}
		oracleRuntime, ok := runtime.(oraclePublicEndpointTransitionRuntime)
		if !ok || op.IdentityTransition == nil {
			finish("failed", "transition", "Oracle Free identity transition support is unavailable after authority revocation. The public route is closed.", endpoint.Observation)
			return
		}
		s.reconcileCancelledOraclePublicEndpointTransition(ctx, runtime, oracleRuntime, op, d, endpoint, cleanupBefore, finish)
		return
	}
	if op.Phase == "cancelling" {
		if err = runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
			finish("queued", "cancelling", "Authority changed. Waiting for the public route to close before cancellation.", endpoint.Observation)
			return
		}
		if s.DatabasePublicEndpointAuthority != nil {
			finish("queued", "cancelling_provider", "Authority changed. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("cancelled", "authorization", "Database public endpoint authority is no longer valid. The route is closed.", endpoint.Observation)
		return
	}
	if err = before(); err != nil {
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrUnauthorized) {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); closeErr != nil {
				phase := "cancelling"
				if databasePublicEndpointRouteMayBePublished(op.Phase) {
					phase = op.Phase
				}
				finish("queued", phase, "Authority changed. Waiting for the public route to close before cancellation.", endpoint.Observation)
				return
			}
			if databaseOracleFreePublicEndpointTransition(d) && op.IdentityTransition != nil {
				finish("queued", "cancelling_identity", "Authority changed. The public route is closed; restoring the prior Oracle Free identity and ingress policy.", endpoint.Observation)
				return
			}
			if s.DatabasePublicEndpointAuthority != nil {
				finish("queued", "cancelling_provider", "Authority changed. The public route is closed; removing any retained provider resources.", endpoint.Observation)
				return
			}
			finish("cancelled", "authorization", "Database public endpoint authority is no longer valid. The route is closed.", endpoint.Observation)
		}
		return
	}
	expired := op.StartedAt != nil && time.Since(*op.StartedAt) > 30*time.Minute
	// A restarted worker may resume an accepted publication after its runtime
	// qualification has been withdrawn. Even the final observation phase must
	// close that in-flight publication before recording failure. Revocation above
	// remains available independently of publication capability.
	availabilityErr := database.PublicEndpointAvailability(d.Spec)
	if validator, ok := runtime.(databasePublicEndpointPublicationValidator); ok {
		availabilityErr = validator.ValidateDatabasePublicEndpointPublication(d)
	}
	if availabilityErr != nil {
		if err = runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
			phase := "closing"
			if databasePublicEndpointProviderOwned(op.Phase) {
				phase = op.Phase
			}
			finish("queued", phase, "Database public publication is unavailable. Waiting for the route and existing sessions to close.", endpoint.Observation)
			return
		}
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(op.Phase) {
			finish("queued", "failing_provider", "Database public publication is unavailable. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("failed", "qualification", availabilityErr.Error(), endpoint.Observation)
		return
	}
	switch op.Phase {
	case "", "accepted", "closing", "identity_closing", "health", "identity", "identity_issuing", "identity_rolling", "identity_converging", "tls", "provider", "publishing", "observing":
	default:
		// Unknown persisted state cannot imply that an older route is closed.
		// Preserve terminal cleanup intent across retries instead of advancing
		// through health and silently publishing an unrecognized operation.
		if err = runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); err != nil {
			finish("queued", "invalid_closing", "The operation state is not recognized. Waiting for the public route and sessions to close.", endpoint.Observation)
			return
		}
		finish("failed", "phase", "The operation state is not recognized. The public route is closed; review the endpoint again.", endpoint.Observation)
		return
	}
	if op.Phase == "observing" {
		if err = runtime.ValidateDatabasePublicEndpoint(ctx, d, endpoint); err != nil {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); closeErr != nil {
				finish("queued", op.Phase, "Database public endpoint configuration changed. Waiting for the route and existing sessions to close.", endpoint.Observation)
				return
			}
			if s.DatabasePublicEndpointAuthority != nil {
				finish("queued", "failing_provider", "The accepted public endpoint no longer matches the installation configuration. The route is closed; removing owned provider resources.", endpoint.Observation)
				return
			}
			finish("failed", "configuration", "The accepted public endpoint no longer matches the installation configuration. The route is closed.", endpoint.Observation)
			return
		}
	}
	// Close the prior revision first. A stale review, DNS change, failed health
	// probe or interrupted certificate update must never leave it serving.
	if op.Phase == "" || op.Phase == "accepted" || op.Phase == "closing" || op.Phase == "identity_closing" {
		closingPhase := "closing"
		if databasePublicEndpointIdentityStarted(op.Phase) {
			closingPhase = "identity_closing"
		}
		if err = runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); err != nil {
			finish("queued", closingPhase, "Waiting for the previous route and its existing sessions to close.", endpoint.Observation)
			return
		}
		retryPhase := databasePublicEndpointHealthRetryPhase(op.Phase)
		if databaseOracleFreePublicEndpointTransition(d) && op.IdentityTransition != nil {
			retryPhase = "identity_issuing"
			if op.IdentityTransition.ProposedLeafFingerprint != "" {
				retryPhase = "identity_rolling"
			}
			if op.IdentityTransition.FinalMember != nil {
				retryPhase = "tls"
			}
		}
		finish("queued", retryPhase, "The previous route is closed. Rechecking database health and the accepted review.", endpoint.Observation)
		return
	}
	if expired {
		if databaseOracleFreePublicEndpointTransition(d) && op.IdentityTransition != nil && op.IdentityTransition.FinalMember == nil {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); closeErr != nil {
				finish("queued", "identity_closing", "The publication attempt expired. Waiting for the route to close.", endpoint.Observation)
				return
			}
			finish("queued", "cancelling_identity", "The publication attempt expired. The route is closed; finishing the recorded Oracle Free identity transition safely.", endpoint.Observation)
			return
		}
		if op.Phase == "publishing" || op.Phase == "observing" {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); closeErr != nil {
				finish("queued", op.Phase, "The publication attempt expired. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(op.Phase) {
			finish("queued", "failing_provider", "The publication attempt expired. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("failed", "timeout", "The database public endpoint did not complete within 30 minutes. The route is closed.", endpoint.Observation)
		return
	}
	if databaseOracleFreePublicEndpointTransition(d) {
		oracleRuntime, ok := runtime.(oraclePublicEndpointTransitionRuntime)
		if !ok {
			finish("failed", "transition", "Oracle Free public identity transition support is unavailable. The route remains closed.", endpoint.Observation)
			return
		}
		s.reconcileOraclePublicEndpointTransition(ctx, runtime, oracleRuntime, op, d, endpoint, before, cleanupBefore, finish)
		return
	}
	observed, observeErr := runtime.ObserveDatabase(ctx, d)
	if observeErr == nil {
		d.Observation = observed
		_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
	}
	identityStarted := databasePublicEndpointIdentityStarted(op.Phase)
	// A SAN update changes the expected leaf before every member reloads it.
	// Keep that durable intent while health converges, but never accept another
	// database revision or a changed topology that observation can identify.
	identityConverging := databasePublicEndpointIdentityConverging(d, observed, op.Review, op.Phase)
	reviewIdentityChanged := databasePublicEndpointReviewIdentityChanged(d, observed, op.Review)
	if op.Review == nil || observeErr != nil && !reviewIdentityChanged || identityConverging {
		if databasePublicEndpointRouteMayBePublished(op.Phase) {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); closeErr != nil {
				finish("queued", op.Phase, "Database health changed while publishing. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		retryPhase := databasePublicEndpointHealthRetryPhase(op.Phase)
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(op.Phase) {
			retryPhase = "provider"
		}
		finish("queued", retryPhase, "Waiting for a current verified database observation while the route remains closed.", endpoint.Observation)
		return
	}
	reviewedEndpoint := endpoint
	reviewedEndpoint.Revision = op.Review.EndpointRevision
	plan, planErr := database.PlanPublicEndpointMembers(d, op.Review.Spec, reviewedEndpoint, time.Now().UTC())
	tlsChangedBeforeIdentity := !identityStarted && plan.TLSFingerprint != op.Review.TLSFingerprint
	if reviewIdentityChanged || planErr != nil || !op.Review.MatchesRoute(plan) || len(plan.BlockedReasons) > 0 || plan.DatabaseRevision != op.Review.DatabaseRevision || plan.TopologyFingerprint != op.Review.TopologyFingerprint || tlsChangedBeforeIdentity || !plan.Spec.Equal(op.Review.Spec) || plan.Allocation != op.Review.Allocation || !slices.Equal(plan.MemberAllocations, op.Review.MemberAllocations) {
		if databasePublicEndpointRouteMayBePublished(op.Phase) {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); closeErr != nil {
				finish("queued", op.Phase, "Database state changed while publishing. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(op.Phase) {
			finish("queued", "failing_provider", "Database state changed while publishing. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("failed", "review", "Database health, topology, TLS identity or endpoint allocation changed. Review the endpoint again.", endpoint.Observation)
		return
	}
	if err = runtime.ValidateDatabasePublicEndpoint(ctx, d, endpoint); err != nil {
		if databasePublicEndpointRouteMayBePublished(op.Phase) {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); closeErr != nil {
				finish("queued", op.Phase, "The endpoint allocation changed while publishing. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(op.Phase) {
			finish("queued", "failing_provider", "The operator allocation or DNS binding changed. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("failed", "allocation", "The operator allocation or DNS binding is no longer valid. The route remains closed.", endpoint.Observation)
		return
	}
	names, err := s.Store.DatabasePublicEndpointNames(ctx, d.ID, "")
	if err != nil {
		return
	}
	endpointNames, err := database.PublicEndpointAllocationNames(endpoint)
	if err != nil {
		return
	}
	names, err = database.NormalizePublicEndpointNames(append(names, endpointNames...))
	if err != nil {
		return
	}
	// Persist identity intent before the first certificate mutation. A crash
	// after issuing the leaf can then safely resume its expected convergence.
	if !identityStarted {
		finish("queued", "identity", "The review is current. Preparing the database certificate while the route remains closed.", endpoint.Observation)
		return
	}
	if op.Phase != "tls" && op.Phase != "provider" && op.Phase != "publishing" && op.Phase != "observing" {
		if err = runtime.ReconcileDatabasePublicEndpointAccess(ctx, d, names, true, before); err != nil {
			finish("queued", "identity", "Waiting for the database certificate and ingress-only network policy while the route remains closed.", endpoint.Observation)
			return
		}
		finish("queued", "tls", "The database identity is ready. Verifying native database TLS before publication.", endpoint.Observation)
		return
	}
	d.PublicEndpointNames, d.PublicEndpointAccess = names, true
	if err = runtime.VerifyDatabasePublicEndpointBackend(ctx, d, endpoint); err != nil {
		if databasePublicEndpointRouteMayBePublished(op.Phase) {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); closeErr != nil {
				finish("queued", op.Phase, "Native TLS changed while publishing. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		retryPhase := "tls"
		if s.DatabasePublicEndpointAuthority != nil && databasePublicEndpointProviderOwned(op.Phase) {
			retryPhase = "provider"
		}
		finish("queued", retryPhase, "Waiting for the selected database service to present the reviewed hostname with native TLS while the route remains closed.", endpoint.Observation)
		return
	}
	if err = before(); err != nil {
		return
	}
	if s.DatabasePublicEndpointAuthority != nil && op.Phase == "tls" {
		finish("queued", "provider", "The database identity is ready. Preparing the owned public DNS resource.", endpoint.Observation)
		return
	}
	if s.DatabasePublicEndpointAuthority != nil {
		if err = s.ensureDatabasePublicEndpointAuthority(ctx, op, d, endpoint, before); err != nil {
			if databasePublicEndpointRouteMayBePublished(op.Phase) {
				if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); closeErr != nil {
					finish("queued", op.Phase, "Endpoint provider verification changed while publishing. Waiting for the route to close.", endpoint.Observation)
					return
				}
			}
			finish("queued", "provider", "Waiting for the reviewed public endpoint provider resources.", endpoint.Observation)
			return
		}
	}
	if err = verifyDatabasePublicEndpointDNS(ctx, runtime, endpoint); err != nil {
		if databasePublicEndpointRouteMayBePublished(op.Phase) {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, before); closeErr != nil {
				finish("queued", op.Phase, "The public hostname changed while publishing. Waiting for the route to close.", endpoint.Observation)
				return
			}
		}
		if s.DatabasePublicEndpointAuthority != nil {
			finish("queued", "failing_provider", "The public hostname changed before publication. The route is closed; removing owned provider resources.", endpoint.Observation)
			return
		}
		finish("failed", "dns", "The public hostname changed before publication. The route remains closed.", endpoint.Observation)
		return
	}
	// A stored HAProxy acknowledgement does not prove the database is still
	// the reviewed backend. Reach this final observation only after the current
	// timeout, authority, topology, TLS and allocation checks above succeed.
	if op.Phase == "observing" {
		result, observeErr := runtime.ObserveDatabasePublicEndpoint(ctx, d, endpoint)
		if observeErr != nil || !result.Configured {
			if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); closeErr != nil {
				finish("queued", op.Phase, "The route observation changed. Waiting for the route to close before retrying.", result)
				return
			}
			retryPhase := "tls"
			if s.DatabasePublicEndpointAuthority != nil {
				retryPhase = "provider"
			}
			finish("queued", retryPhase, "The route observation changed. Rechecking publication from the closed state.", result)
			return
		}
		finish("succeeded", "configured", "", result)
		return
	}
	// Persist may-be-published before the first Kubernetes route mutation. If
	// route creation or acknowledgement outlives the following state write, the
	// next worker must still close it before any health/TLS retry or cancellation.
	if op.Phase == "tls" || op.Phase == "provider" {
		finish("queued", "publishing", "Publishing the reviewed route to every owned HAProxy worker.", endpoint.Observation)
		return
	}
	if err = runtime.ReconcileDatabasePublicEndpoint(ctx, d, endpoint, before); err != nil {
		if closeErr := runtime.PrepareDatabasePublicEndpoint(ctx, d, endpoint, cleanupBefore); closeErr != nil {
			finish("queued", op.Phase, "HAProxy publication is incomplete. Waiting for the route to close before retrying.", endpoint.Observation)
			return
		}
		retryPhase := "tls"
		if s.DatabasePublicEndpointAuthority != nil {
			retryPhase = "provider"
		}
		finish("queued", retryPhase, "Waiting to republish the reviewed route from the closed state.", endpoint.Observation)
		return
	}
	finish("queued", "observing", "The accepted HAProxy reload is recorded. Verifying the configured route.", endpoint.Observation)
}
