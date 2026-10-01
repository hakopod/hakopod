package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func oraclePublicEndpointTransitionValidationFixture(t *testing.T) (database.PublicEndpointOperation, database.Resource, database.PublicEndpoint, database.PublicEndpointIdentityTransition) {
	t.Helper()
	d := database.Resource{ID: "database", Revision: 4, Status: "ready", Spec: database.Spec{SchemaVersion: 1, Engine: "oracle", Version: "23.26", Mode: "standalone", Shards: 1, TLS: &database.TLSConfig{Mode: "required"}, Oracle: &database.OracleConfig{Edition: "free"}}}
	endpoint := database.PublicEndpoint{ID: "endpoint", DatabaseID: d.ID, Revision: 2, Spec: database.PublicEndpointSpec{Purpose: "read_write"}}
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	oldTopology := fmt.Sprintf("%x", sha256.Sum256([]byte("database-0:old-member")))
	newTopology := fmt.Sprintf("%x", sha256.Sum256([]byte("database-0:new-member")))
	review := &database.PublicEndpointReview{DatabaseRevision: d.Revision, EndpointRevision: 1, TopologyFingerprint: oldTopology, TLSFingerprint: "reviewed-leaf", Route: &route, RouteFingerprint: route.Fingerprint()}
	operation := database.PublicEndpointOperation{ID: "operation", EndpointID: endpoint.ID, DatabaseID: d.ID, Revision: endpoint.Revision, Kind: "publish", Review: review}
	transition := database.PublicEndpointIdentityTransition{
		SchemaVersion: database.PublicEndpointIdentityTransitionSchemaVersion, OperationID: operation.ID, Engine: "oracle", Kind: operation.Kind,
		DatabaseRevision: d.Revision, EndpointRevision: endpoint.Revision, RouteFingerprint: route.Fingerprint(), DesiredNames: []string{"database.example.test"},
		ReviewedTopologyFingerprint: review.TopologyFingerprint, ReviewedLeafFingerprint: review.TLSFingerprint, ReviewedCAFingerprint: "reviewed-ca",
		StatefulSetUID: "statefulset", OriginalGeneration: 7, OriginalTemplateHash: "old-template",
		OldMember:               database.PublicEndpointTransitionMember{Name: "database-0", UID: "old-member"},
		PVCs:                    []database.PublicEndpointTransitionPVC{{Name: "data-database-0", UID: "data-uid", VolumeName: "data-volume", PersistentVolumeUID: "data-pv-uid", BackingVolumeFingerprint: "data-backing"}, {Name: "backup-database-0", UID: "backup-uid", VolumeName: "backup-volume", PersistentVolumeUID: "backup-pv-uid", BackingVolumeFingerprint: "backup-backing"}},
		ProposedLeafFingerprint: "new-leaf", ProposedCAFingerprint: "reviewed-ca", TargetGeneration: 8, TargetTemplateHash: "new-template",
		FinalMember: &database.PublicEndpointTransitionMember{Name: "database-0", UID: "new-member"}, FinalTopologyFingerprint: newTopology, ServedLeafFingerprint: "new-leaf", ServedCAFingerprint: "reviewed-ca",
	}
	return operation, d, endpoint, transition
}

func TestOraclePublicEndpointTransitionRejectsPersistedIdentityDrift(t *testing.T) {
	operation, d, endpoint, valid := oraclePublicEndpointTransitionValidationFixture(t)
	if !oraclePublicEndpointTransitionValid(operation, d, endpoint, &valid, true, true) {
		t.Fatal("valid Oracle transition proof was rejected")
	}
	tests := map[string]func(*database.PublicEndpointOperation, *database.Resource, *database.PublicEndpoint, *database.PublicEndpointIdentityTransition){
		"database revision": func(_ *database.PublicEndpointOperation, d *database.Resource, _ *database.PublicEndpoint, _ *database.PublicEndpointIdentityTransition) {
			d.Revision++
		},
		"endpoint revision": func(_ *database.PublicEndpointOperation, _ *database.Resource, endpoint *database.PublicEndpoint, _ *database.PublicEndpointIdentityTransition) {
			endpoint.Revision++
		},
		"route": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.RouteFingerprint = "changed"
		},
		"reviewed topology": func(operation *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, _ *database.PublicEndpointIdentityTransition) {
			operation.Review.TopologyFingerprint = "changed"
		},
		"statefulset uid": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.StatefulSetUID = ""
		},
		"generation": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.TargetGeneration = 6
		},
		"template": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.TargetTemplateHash = ""
		},
		"old member": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.OldMember.UID = ""
		},
		"final member": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.FinalMember.UID = transition.OldMember.UID
		},
		"pvc uid": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.PVCs[0].UID = ""
		},
		"bound volume": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.PVCs[1].VolumeName = ""
		},
		"persistent volume uid": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.PVCs[0].PersistentVolumeUID = ""
		},
		"backing volume": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.PVCs[1].BackingVolumeFingerprint = ""
		},
		"served leaf": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.ServedLeafFingerprint = "other"
		},
		"served ca": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.ServedCAFingerprint = "other"
		},
		"proposed ca differs from reviewed ca": func(_ *database.PublicEndpointOperation, _ *database.Resource, _ *database.PublicEndpoint, transition *database.PublicEndpointIdentityTransition) {
			transition.ProposedCAFingerprint = "other"
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidateOperation, candidateDatabase, candidateEndpoint, candidate := operation, d, endpoint, valid
			review := *operation.Review
			candidateOperation.Review = &review
			finalMember := *valid.FinalMember
			candidate.FinalMember = &finalMember
			candidate.PVCs = append([]database.PublicEndpointTransitionPVC(nil), valid.PVCs...)
			change(&candidateOperation, &candidateDatabase, &candidateEndpoint, &candidate)
			if oraclePublicEndpointTransitionValid(candidateOperation, candidateDatabase, candidateEndpoint, &candidate, true, true) {
				t.Fatal("changed Oracle transition proof was accepted")
			}
		})
	}
}

func TestOraclePublicEndpointTransitionKeepsReviewedAndFinalTopologySeparate(t *testing.T) {
	operation, d, endpoint, transition := oraclePublicEndpointTransitionValidationFixture(t)
	if transition.ReviewedTopologyFingerprint == transition.FinalTopologyFingerprint || operation.Review.TopologyFingerprint != transition.ReviewedTopologyFingerprint {
		t.Fatal("fixture did not retain the original reviewed topology")
	}
	if !oraclePublicEndpointTransitionValid(operation, d, endpoint, &transition, true, true) {
		t.Fatal("replacement topology incorrectly overwrote the reviewed topology")
	}
}

type oracleTransitionSyntheticRuntime struct {
	*publicEndpointWorkerRuntime
	begin, issue, roll, converge int
	onIssue                      func()
	issueErr                     error
	onConverge                   func(database.Observation, database.PublicEndpointIdentityTransition) (database.PublicEndpointIdentityTransition, bool, error)
	beginKinds                   []string
	beginNames                   [][]string
	issueAllowIngress            []bool
}

type oracleRevocationSyntheticRuntime struct {
	*oracleTransitionSyntheticRuntime
	releases int
}

func (r *oracleRevocationSyntheticRuntime) ReleaseDatabasePublicEndpoint(_ context.Context, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	r.releases++
	return nil
}

func (r *oracleTransitionSyntheticRuntime) BeginOraclePublicEndpointIdentityTransition(_ context.Context, operation database.PublicEndpointOperation, d database.Resource, endpoint database.PublicEndpoint, names []string) (database.PublicEndpointIdentityTransition, error) {
	r.begin++
	r.beginKinds = append(r.beginKinds, operation.Kind)
	r.beginNames = append(r.beginNames, append([]string(nil), names...))
	reviewedTopology, reviewedLeaf, reviewedCA := d.Observation.TopologyFingerprint, d.Observation.TLS.Fingerprint, d.Observation.TLS.CAFingerprint
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil {
		return database.PublicEndpointIdentityTransition{}, err
	}
	routeFingerprint := route.Fingerprint()
	if operation.Kind == "publish" {
		reviewedTopology, reviewedLeaf = operation.Review.TopologyFingerprint, operation.Review.TLSFingerprint
		routeFingerprint = operation.Review.RouteFingerprint
	}
	oldMember := database.PublicEndpointTransitionMember{Name: "database-0", UID: "old-member"}
	if operation.Kind != "publish" {
		reviewedTopology, reviewedLeaf = d.Observation.TopologyFingerprint, d.Observation.TLS.Fingerprint
		reviewedCA = d.Observation.TLS.CAFingerprint
		oldMember = database.PublicEndpointTransitionMember{Name: d.Observation.Members[0].Name, UID: d.Observation.Members[0].UID}
	}
	return database.PublicEndpointIdentityTransition{
		SchemaVersion: database.PublicEndpointIdentityTransitionSchemaVersion, OperationID: operation.ID, Engine: "oracle", Kind: operation.Kind,
		DatabaseRevision: d.Revision, EndpointRevision: endpoint.Revision, RouteFingerprint: routeFingerprint, DesiredNames: names,
		ReviewedTopologyFingerprint: reviewedTopology, ReviewedLeafFingerprint: reviewedLeaf, ReviewedCAFingerprint: reviewedCA,
		StatefulSetUID: "statefulset", OriginalGeneration: 7, OriginalTemplateHash: "old-template", OldMember: oldMember,
		PVCs: []database.PublicEndpointTransitionPVC{{Name: "data-database-0", UID: "data-uid", VolumeName: "data-volume", PersistentVolumeUID: "data-pv-uid", BackingVolumeFingerprint: "data-backing"}, {Name: "backup-database-0", UID: "backup-uid", VolumeName: "backup-volume", PersistentVolumeUID: "backup-pv-uid", BackingVolumeFingerprint: "backup-backing"}},
	}, nil
}

func (r *oracleTransitionSyntheticRuntime) IssueOraclePublicEndpointIdentity(_ context.Context, _ database.Resource, transition database.PublicEndpointIdentityTransition, allowIngress bool, before func() error) (database.PublicEndpointIdentityTransition, error) {
	if err := before(); err != nil {
		return transition, err
	}
	r.issue++
	r.issueAllowIngress = append(r.issueAllowIngress, allowIngress)
	if r.issueErr != nil {
		return transition, r.issueErr
	}
	transition.ProposedLeafFingerprint, transition.ProposedCAFingerprint = "new-leaf", transition.ReviewedCAFingerprint
	transition.TargetGeneration, transition.TargetTemplateHash = 8, "new-template"
	if r.onIssue != nil {
		r.onIssue()
	}
	return transition, nil
}

func (r *oracleTransitionSyntheticRuntime) RollOraclePublicEndpointIdentity(_ context.Context, _ database.Resource, _ database.PublicEndpointIdentityTransition, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	r.roll++
	return nil
}

func (r *oracleTransitionSyntheticRuntime) ConvergeOraclePublicEndpointIdentity(_ context.Context, _ database.Resource, _ database.PublicEndpoint, observed database.Observation, transition database.PublicEndpointIdentityTransition) (database.PublicEndpointIdentityTransition, bool, error) {
	r.converge++
	if r.onConverge != nil {
		return r.onConverge(observed, transition)
	}
	memberUID := "new-member"
	if transition.Kind == "cancel" || transition.Kind == "revoke" {
		memberUID = "cleanup-member"
	}
	transition.FinalMember = &database.PublicEndpointTransitionMember{Name: "database-0", UID: memberUID}
	transition.FinalTopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte("database-0:"+memberUID)))
	transition.ServedLeafFingerprint, transition.ServedCAFingerprint = transition.ProposedLeafFingerprint, transition.ProposedCAFingerprint
	r.observation.ObservedAt = time.Now().UTC()
	r.observation.Status, r.observation.Primary = "ready", "database-0"
	r.observation.Members = []database.Member{{Name: "database-0", UID: memberUID, Role: "primary", Ready: true}}
	r.observation.TopologyFingerprint = transition.FinalTopologyFingerprint
	r.observation.TLS = &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: transition.ServedLeafFingerprint, CAFingerprint: transition.ServedCAFingerprint}
	return transition, true, nil
}

func oracleTransitionWorkerFixture(t *testing.T) (*Server, store.Principal, string, *oracleTransitionSyntheticRuntime) {
	t.Helper()
	server, owner, id, base := publicEndpointWorkerFixture(t, "health")
	ctx := context.Background()
	op, err := server.Store.DatabasePublicEndpointOperation(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	d, err := server.Store.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	d.Spec.Engine, d.Spec.Version, d.Spec.Mode, d.Spec.Replicas, d.Spec.Shards = "oracle", "23.26", "standalone", 0, 1
	d.Spec.Oracle = &database.OracleConfig{Edition: "free"}
	d.Spec.TLS = &database.TLSConfig{Mode: "required"}
	oldTopology := fmt.Sprintf("%x", sha256.Sum256([]byte("database-0:old-member")))
	d.Observation = database.Observation{ObservedAt: time.Now().UTC(), Revision: d.Revision, Status: "ready", Primary: "database-0", Members: []database.Member{{Name: "database-0", UID: "old-member", Role: "primary", Ready: true}}, TopologyFingerprint: oldTopology, Endpoints: []database.Endpoint{{Purpose: "read_write", Port: 2484}}, TLS: &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "old-leaf", CAFingerprint: "old-ca"}}
	route, err := database.PublicEndpointRouteFor(d.Spec, "read_write")
	if err != nil {
		t.Fatal(err)
	}
	op.Review.Route, op.Review.RouteFingerprint = &route, route.Fingerprint()
	op.Review.TopologyFingerprint, op.Review.TLSFingerprint = oldTopology, "old-leaf"
	if _, err = server.Store.Pool.Exec(ctx, "UPDATE managed_databases SET spec=$2,observation=$3 WHERE id=$1", d.ID, store.JSON(d.Spec), store.JSON(d.Observation)); err != nil {
		t.Fatal(err)
	}
	if _, err = server.Store.Pool.Exec(ctx, "UPDATE managed_database_public_endpoint_operations SET review=$2 WHERE id=$1", id, store.JSON(op.Review)); err != nil {
		t.Fatal(err)
	}
	base.observation = d.Observation
	return server, owner, id, &oracleTransitionSyntheticRuntime{publicEndpointWorkerRuntime: base}
}

func runOracleTransitionAttempt(t *testing.T, server *Server, owner store.Principal, id string, runtime *oracleTransitionSyntheticRuntime) database.PublicEndpointOperation {
	t.Helper()
	ctx := context.Background()
	if _, err := server.Store.Pool.Exec(ctx, "UPDATE managed_database_public_endpoint_operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	operation, err := server.Store.ClaimDatabasePublicEndpointOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err := server.Store.DatabaseInternal(ctx, operation.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := server.Store.DatabasePublicEndpointInternal(ctx, operation.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Spec = operation.Review.Spec
	finish := func(status, phase, message string, observation database.PublicEndpointObservation) {
		_ = server.Store.RecordDatabasePublicEndpointStep(context.Background(), operation, observation, status, phase, message)
	}
	before := func() error { return server.Store.CheckDatabasePublicEndpointOperation(ctx, operation) }
	cleanup := func() error { return server.Store.CheckDatabasePublicEndpointOperationCleanup(ctx, operation) }
	server.reconcileOraclePublicEndpointTransition(ctx, runtime, runtime, operation, d, endpoint, before, cleanup, finish)
	loaded, err := server.Store.DatabasePublicEndpointOperation(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestOraclePublicEndpointIntentPrecedesMutationAndResumesAfterSecretWrite(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	op := runOracleTransitionAttempt(t, server, owner, id, runtime)
	if op.Phase != "identity_issuing" || op.IdentityTransition == nil || runtime.begin != 1 || runtime.issue != 0 {
		t.Fatal("Oracle identity mutated before its durable intent", op.Phase, runtime.begin, runtime.issue)
	}
	runtime.onIssue = func() {
		_, _ = server.Store.Pool.Exec(context.Background(), "WITH expired AS (UPDATE managed_database_public_endpoint_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 RETURNING database_id,lease) UPDATE managed_databases d SET maintenance_lease_until=clock_timestamp()-interval '1 second' FROM expired e WHERE d.id=e.database_id AND d.maintenance_lease=e.lease", id)
	}
	op = runOracleTransitionAttempt(t, server, owner, id, runtime)
	if op.Phase != "identity_issuing" || op.IdentityTransition.ProposedLeafFingerprint != "" || runtime.issue != 1 {
		t.Fatal("interrupted Secret mutation advanced unpersisted transition state", op.Phase, runtime.issue)
	}
	runtime.onIssue = nil
	for _, phase := range []string{"identity_rolling", "identity_converging", "tls"} {
		op = runOracleTransitionAttempt(t, server, owner, id, runtime)
		if op.Phase != phase {
			t.Fatal("Oracle transition did not resume through its durable phases", phase, op.Phase)
		}
	}
	if op.IdentityTransition == nil || op.IdentityTransition.ReviewedTopologyFingerprint == op.IdentityTransition.FinalTopologyFingerprint || runtime.issue != 2 || runtime.roll != 1 || runtime.converge != 1 {
		t.Fatal("Oracle transition lost reviewed or replacement proof", op.IdentityTransition, runtime)
	}
}

func TestOraclePublicEndpointIssuerMismatchDoesNotAdvanceTransition(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	operation := runOracleTransitionAttempt(t, server, owner, id, runtime)
	if operation.Phase != "identity_issuing" {
		t.Fatal("Oracle identity intent was not persisted before issuance", operation.Phase)
	}
	runtime.issueErr = fmt.Errorf("Oracle Free issuer changed during identity issuance")
	operation = runOracleTransitionAttempt(t, server, owner, id, runtime)
	if operation.Phase != "identity_issuing" || operation.IdentityTransition == nil || operation.IdentityTransition.ProposedLeafFingerprint != "" || operation.IdentityTransition.ProposedCAFingerprint != "" {
		t.Fatal("issuer mismatch advanced the Oracle identity transition", operation)
	}
	if runtime.issue != 1 || len(runtime.issueAllowIngress) != 1 || !runtime.issueAllowIngress[0] {
		t.Fatal("normal Oracle publication issuance did not request its reviewed ingress policy", runtime.issue, runtime.issueAllowIngress)
	}
}

func oracleTransitionAdvanceToTLS(t *testing.T, server *Server, owner store.Principal, id string, runtime *oracleTransitionSyntheticRuntime) database.PublicEndpointOperation {
	t.Helper()
	var operation database.PublicEndpointOperation
	for _, phase := range []string{"identity_issuing", "identity_rolling", "identity_converging", "tls"} {
		operation = runOracleTransitionAttempt(t, server, owner, id, runtime)
		if operation.Phase != phase {
			t.Fatal("Oracle transition did not reach final proof", phase, operation.Phase)
		}
	}
	return operation
}

func TestOraclePublicEndpointFinalResumeRerunsCompleteConvergenceProof(t *testing.T) {
	for _, drift := range []string{"stale observation", "TLS verification lost", "plaintext rejection lost", "StatefulSet changed", "member changed", "PVC changed"} {
		t.Run(drift, func(t *testing.T) {
			server, owner, id, runtime := oracleTransitionWorkerFixture(t)
			oracleTransitionAdvanceToTLS(t, server, owner, id, runtime)
			runtime.onConverge = func(_ database.Observation, transition database.PublicEndpointIdentityTransition) (database.PublicEndpointIdentityTransition, bool, error) {
				return transition, false, fmt.Errorf("synthetic %s", drift)
			}
			operation := runOracleTransitionAttempt(t, server, owner, id, runtime)
			if operation.Status != "failed" || operation.Phase != "transition" || runtime.converge != 2 {
				t.Fatal("final Oracle resume bypassed complete convergence proof", operation.Status, operation.Phase, runtime.converge)
			}
		})
	}
}

func TestOraclePublicEndpointFinalResumeRejectsChangedReturnedProof(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	oracleTransitionAdvanceToTLS(t, server, owner, id, runtime)
	runtime.onConverge = func(_ database.Observation, transition database.PublicEndpointIdentityTransition) (database.PublicEndpointIdentityTransition, bool, error) {
		transition.FinalMember = &database.PublicEndpointTransitionMember{Name: "database-0", UID: "other-member"}
		transition.FinalTopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte("database-0:other-member")))
		return transition, true, nil
	}
	operation := runOracleTransitionAttempt(t, server, owner, id, runtime)
	if operation.Status != "failed" || operation.Phase != "transition" {
		t.Fatal("final Oracle resume accepted proof different from the durable record", operation.Status, operation.Phase)
	}
}

func runCancelledOracleTransitionAttempt(t *testing.T, server *Server, owner store.Principal, id string, runtime *oracleTransitionSyntheticRuntime) database.PublicEndpointOperation {
	t.Helper()
	ctx := context.Background()
	if _, err := server.Store.Pool.Exec(ctx, "UPDATE managed_database_public_endpoint_operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	operation, err := server.Store.ClaimDatabasePublicEndpointOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err := server.Store.DatabaseInternal(ctx, operation.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := server.Store.DatabasePublicEndpointInternal(ctx, operation.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Spec = operation.Review.Spec
	finish := func(status, phase, message string, observation database.PublicEndpointObservation) {
		_ = server.Store.RecordDatabasePublicEndpointStep(context.Background(), operation, observation, status, phase, message)
	}
	cleanup := func() error { return server.Store.CheckDatabasePublicEndpointOperationCleanup(ctx, operation) }
	server.reconcileCancelledOraclePublicEndpointTransition(ctx, runtime, runtime, operation, d, endpoint, cleanup, finish)
	loaded, err := server.Store.DatabasePublicEndpointOperation(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestOraclePublicEndpointCancellationRestoresPriorIdentityBeforeTerminalState(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	oracleTransitionAdvanceToTLS(t, server, owner, id, runtime)
	if _, err := server.Store.Pool.Exec(context.Background(), "UPDATE managed_database_public_endpoint_operations SET phase='cancelling_identity' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	operation := runCancelledOracleTransitionAttempt(t, server, owner, id, runtime)
	if operation.Status != "queued" || operation.Phase != "cancelling_identity" || operation.IdentityTransition == nil || operation.IdentityTransition.Kind != "cancel" || len(operation.IdentityTransition.DesiredNames) != 0 {
		t.Fatal("cancellation did not persist private identity cleanup before mutation", operation)
	}
	for attempt := 0; attempt < 4 && operation.Status == "queued"; attempt++ {
		operation = runCancelledOracleTransitionAttempt(t, server, owner, id, runtime)
	}
	if operation.Status != "cancelled" || operation.Phase != "authorization" || runtime.issue != 2 || runtime.roll != 2 || runtime.converge != 3 {
		t.Fatal("cancellation became terminal before private identity cleanup was proven", operation.Status, operation.Phase, runtime.issue, runtime.roll, runtime.converge)
	}
	if len(runtime.beginKinds) != 2 || runtime.beginKinds[0] != "publish" || runtime.beginKinds[1] != "cancel" || len(runtime.beginNames[1]) != 0 {
		t.Fatal("cancellation retained the cancelled public hostname", runtime.beginKinds, runtime.beginNames)
	}
	if len(runtime.issueAllowIngress) != 2 || !runtime.issueAllowIngress[0] || !runtime.issueAllowIngress[1] {
		t.Fatal("completed publication and private rollback did not retain the required ingress policy", runtime.issueAllowIngress)
	}
}

func TestOraclePublicEndpointCancellationBeforeIssuanceKeepsIngressClosed(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	operation := runOracleTransitionAttempt(t, server, owner, id, runtime)
	if operation.Phase != "identity_issuing" || operation.IdentityTransition == nil || operation.IdentityTransition.Kind != "publish" {
		t.Fatal("Oracle publication did not stop at its durable pre-issuance intent", operation)
	}
	if _, err := server.Store.Pool.Exec(context.Background(), "UPDATE managed_database_public_endpoint_operations SET phase='cancelling_identity' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	operation = runCancelledOracleTransitionAttempt(t, server, owner, id, runtime)
	if operation.Status != "queued" || operation.Phase != "cancelling_identity" || operation.IdentityTransition == nil || operation.IdentityTransition.Kind != "publish" || operation.IdentityTransition.ProposedLeafFingerprint == "" {
		t.Fatal("fenced cancellation did not persist the original publication identity", operation)
	}
	if runtime.issue != 1 || len(runtime.issueAllowIngress) != 1 || runtime.issueAllowIngress[0] {
		t.Fatal("fenced cancellation opened ingress while completing the original publication identity", runtime.issue, runtime.issueAllowIngress)
	}
}

func TestOraclePublicEndpointCloudPublicationAndRetryRetainProvider(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	oracleTransitionAdvanceToTLS(t, server, owner, id, runtime)
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	for _, phase := range []string{"provider", "publishing"} {
		operation := runPublicEndpointWorker(t, server, owner, id, runtime)
		if operation.Phase != phase {
			t.Fatal("Oracle Cloud publication lost its provider phase", phase, operation.Status, operation.Phase)
		}
	}
	authority.ensureErr = fmt.Errorf("synthetic provider verification failure")
	runtime.prepareErr = fmt.Errorf("synthetic HAProxy drain failure")
	operation := runPublicEndpointWorker(t, server, owner, id, runtime)
	if operation.Status != "queued" || operation.Phase != "publishing" || runtime.prepared != 1 {
		t.Fatal("Oracle provider retry discarded the may-be-published phase before closure", operation.Status, operation.Phase, runtime.prepared)
	}
	runtime.prepareErr = nil
	operation = runPublicEndpointWorker(t, server, owner, id, runtime)
	if operation.Status != "queued" || operation.Phase != "provider" || runtime.prepared != 2 {
		t.Fatal("Oracle provider retry did not close the possibly published route", operation.Status, operation.Phase, runtime.prepared)
	}
	authority.ensureErr = nil
	for _, phase := range []string{"publishing", "observing", "configured"} {
		operation = runPublicEndpointWorker(t, server, owner, id, runtime)
		if operation.Phase != phase {
			t.Fatal("Oracle Cloud publication did not resume from provider ownership", phase, operation.Status, operation.Phase)
		}
	}
	if operation.Status != "succeeded" || authority.ensures != 6 || authority.verifies != 4 || runtime.published != 1 || runtime.observed != 1 {
		t.Fatal("Oracle Cloud publication skipped provider or route verification", operation.Status, authority, runtime.publicEndpointWorkerRuntime)
	}
}

func TestOraclePublicEndpointCloudCancellationRestoresIdentityBeforeProviderCleanup(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	oracleTransitionAdvanceToTLS(t, server, owner, id, runtime)
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	for _, phase := range []string{"provider", "publishing"} {
		operation := runPublicEndpointWorker(t, server, owner, id, runtime)
		if operation.Phase != phase {
			t.Fatal("Oracle cancellation fixture did not create provider ownership", phase, operation.Phase)
		}
	}
	if _, err := server.Store.Pool.Exec(context.Background(), "UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE id=$1", owner.KeyID); err != nil {
		t.Fatal(err)
	}
	operation := runPublicEndpointWorker(t, server, owner, id, runtime)
	if operation.Status != "queued" || operation.Phase != "cancelling_identity" || runtime.prepared != 1 || authority.deletes != 0 {
		t.Fatal("Oracle cancellation skipped route closure or durable identity cleanup", operation.Status, operation.Phase, runtime.prepared, authority.deletes)
	}
	for attempt := 0; attempt < 6 && operation.Status == "queued"; attempt++ {
		operation = runPublicEndpointWorker(t, server, owner, id, runtime)
	}
	if operation.Status != "cancelled" || operation.Phase != "authorization" || authority.deletes != 1 || runtime.issue != 2 || runtime.roll != 2 {
		t.Fatal("Oracle cancellation cleaned provider resources before restoring private identity", operation.Status, operation.Phase, authority.deletes, runtime.issue, runtime.roll)
	}
}

func TestOraclePublicEndpointAcceptedRevocationCompletesAfterInitiatingKeyWithdrawal(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	operation := database.PublicEndpointOperation{ID: id, Status: "queued"}
	for attempt := 0; attempt < 10 && operation.Status != "succeeded"; attempt++ {
		operation = runPublicEndpointWorker(t, server, owner, id, runtime)
	}
	if operation.Status != "succeeded" || operation.Phase != "configured" {
		t.Fatal("Oracle revocation fixture did not publish", operation.Status, operation.Phase)
	}
	endpoint, err := server.Store.DatabasePublicEndpointInternal(context.Background(), operation.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	revoke, err := server.Store.RevokeDatabasePublicEndpoint(context.Background(), owner, operation.DatabaseID, operation.EndpointID, "oracle-durable-revoke", endpoint.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.Store.Pool.Exec(context.Background(), "UPDATE api_keys SET revoked_at=now() WHERE id=$1", owner.KeyID); err != nil {
		t.Fatal(err)
	}
	authority.ensures, authority.verifies, authority.deletes = 0, 0, 0
	revokeRuntime := &oracleRevocationSyntheticRuntime{oracleTransitionSyntheticRuntime: runtime}
	for _, phase := range []string{"identity", "identity_issuing", "identity_rolling", "identity_converging", "provider_delete", "release", "revoked"} {
		revoke = runPublicEndpointWorker(t, server, owner, revoke.ID, revokeRuntime)
		if revoke.Phase != phase || revoke.Status == "cancelled" || strings.HasPrefix(revoke.Phase, "cancelling") {
			t.Fatal("withdrawn key diverted Oracle revocation", phase, revoke.Status, revoke.Phase)
		}
		if revoke.IdentityTransition != nil && revoke.IdentityTransition.Kind != "revoke" {
			t.Fatal("Oracle revocation changed identity transition kind", revoke.IdentityTransition.Kind)
		}
	}
	if revoke.Status != "succeeded" || runtime.beginKinds[len(runtime.beginKinds)-1] != "revoke" || len(runtime.beginNames[len(runtime.beginNames)-1]) != 0 || authority.deletes != 1 || revokeRuntime.releases != 1 {
		t.Fatal("Oracle revocation skipped final identity, provider, or allocation cleanup", revoke.Status, runtime.beginKinds, runtime.beginNames, authority.deletes, revokeRuntime.releases)
	}
}

func TestOraclePublicEndpointCloudFailureClosesBeforeProviderCleanup(t *testing.T) {
	server, owner, id, runtime := oracleTransitionWorkerFixture(t)
	oracleTransitionAdvanceToTLS(t, server, owner, id, runtime)
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	var operation database.PublicEndpointOperation
	for _, phase := range []string{"provider", "publishing", "observing"} {
		operation = runPublicEndpointWorker(t, server, owner, id, runtime)
		if operation.Phase != phase {
			t.Fatal("Oracle failure fixture did not reach an observed route", phase, operation.Phase)
		}
	}
	runtime.validateErr = fmt.Errorf("synthetic allocation drift")
	operation = runPublicEndpointWorker(t, server, owner, id, runtime)
	if operation.Status != "queued" || operation.Phase != "failing_provider" || runtime.prepared != 1 || authority.deletes != 0 {
		t.Fatal("Oracle permanent failure did not close before provider cleanup", operation.Status, operation.Phase, runtime.prepared, authority.deletes)
	}
	operation = runPublicEndpointWorker(t, server, owner, id, runtime)
	if operation.Status != "failed" || operation.Phase != "provider" || authority.deletes != 1 {
		t.Fatal("Oracle permanent failure skipped durable provider cleanup", operation.Status, operation.Phase, authority.deletes)
	}
}
