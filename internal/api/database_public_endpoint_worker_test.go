package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

// Synthetic runtime with a real durable operation store. These tests exercise
// worker claims and writes, without representing Kubernetes acceptance.
type publicEndpointWorkerRuntime struct {
	observation                                             database.Observation
	observeErr, prepareErr, validateErr, backendErr, dnsErr error
	endpointObserveErr, publishErr                          error
	prepared, identities, backends, published, observed     int
	healthChecks, removed                                   int
	onIdentity                                              func() error
}

type revocationEndpointWorkerRuntime struct {
	*publicEndpointWorkerRuntime
	releaseErr error
	releases   int
}

func (r *revocationEndpointWorkerRuntime) ReleaseDatabasePublicEndpoint(_ context.Context, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	r.releases++
	return r.releaseErr
}

type cleanupOnlyEndpointAuthority struct {
	deleteErr error
	deletes   int
}

func (*cleanupOnlyEndpointAuthority) Preflight(context.Context, database.Resource, []database.PublicEndpointAllocation) (string, error) {
	return "", errors.New("new endpoint inventory is disabled")
}
func (*cleanupOnlyEndpointAuthority) Ensure(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint, func() error) error {
	return errors.New("new endpoint inventory is disabled")
}
func (*cleanupOnlyEndpointAuthority) Verify(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint) error {
	return errors.New("new endpoint inventory is disabled")
}
func (a *cleanupOnlyEndpointAuthority) Delete(_ context.Context, _ database.PublicEndpointOperation, _ database.Resource, _ database.PublicEndpoint, _ func() error) error {
	a.deletes++
	return a.deleteErr
}
func (*cleanupOnlyEndpointAuthority) VerifyDeleted(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint) error {
	return nil
}

func (r *publicEndpointWorkerRuntime) ObserveDatabase(context.Context, database.Resource) (database.Observation, error) {
	r.healthChecks++
	return r.observation, r.observeErr
}
func (*publicEndpointWorkerRuntime) ValidateDatabasePublicEndpointPublication(database.Resource) error {
	return nil
}
func (r *publicEndpointWorkerRuntime) PrepareDatabasePublicEndpoint(_ context.Context, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	r.prepared++
	if err := before(); err != nil {
		return err
	}
	return r.prepareErr
}
func (r *publicEndpointWorkerRuntime) RemoveDatabasePublicEndpoint(_ context.Context, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	r.removed++
	if err := before(); err != nil {
		return err
	}
	return r.prepareErr
}
func (r *publicEndpointWorkerRuntime) ReleaseDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint, func() error) error {
	return errors.New("unexpected release")
}
func (r *publicEndpointWorkerRuntime) ValidateDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint) error {
	return r.validateErr
}
func (r *publicEndpointWorkerRuntime) ObserveDatabasePublicEndpoint(context.Context, database.Resource, database.PublicEndpoint) (database.PublicEndpointObservation, error) {
	r.observed++
	return database.PublicEndpointObservation{Configured: r.endpointObserveErr == nil, Message: "Synthetic acknowledged route"}, r.endpointObserveErr
}
func (r *publicEndpointWorkerRuntime) ReconcileDatabasePublicEndpointAccess(_ context.Context, _ database.Resource, _ []string, _ bool, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	r.identities++
	if r.onIdentity != nil {
		return r.onIdentity()
	}
	return nil
}
func (r *publicEndpointWorkerRuntime) VerifyDatabasePublicEndpointBackend(context.Context, database.Resource, database.PublicEndpoint) error {
	r.backends++
	return r.backendErr
}
func (r *publicEndpointWorkerRuntime) VerifyDatabasePublicEndpointDNS(context.Context, database.PublicEndpointAllocation) error {
	return r.dnsErr
}
func (r *publicEndpointWorkerRuntime) ReconcileDatabasePublicEndpoint(_ context.Context, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	r.published++
	return r.publishErr
}

type workerEndpointAuthority struct {
	ensureErr, verifyErr, deleteErr error
	ensures, verifies, deletes      int
	onEnsure                        func()
}

func (*workerEndpointAuthority) Preflight(context.Context, database.Resource, []database.PublicEndpointAllocation) (string, error) {
	return strings.Repeat("a", 64), nil
}
func (a *workerEndpointAuthority) Ensure(_ context.Context, _ database.PublicEndpointOperation, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	a.ensures++
	if a.onEnsure != nil {
		a.onEnsure()
	}
	return a.ensureErr
}
func (a *workerEndpointAuthority) Verify(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint) error {
	a.verifies++
	return a.verifyErr
}
func (a *workerEndpointAuthority) Delete(_ context.Context, _ database.PublicEndpointOperation, _ database.Resource, _ database.PublicEndpoint, before func() error) error {
	if err := before(); err != nil {
		return err
	}
	a.deletes++
	return a.deleteErr
}
func (*workerEndpointAuthority) VerifyDeleted(context.Context, database.PublicEndpointOperation, database.Resource, database.PublicEndpoint) error {
	return nil
}

func publicEndpointWorkerFixture(t *testing.T, phase string) (*Server, store.Principal, string, *publicEndpointWorkerRuntime) {
	t.Helper()
	s, _, owner, d := databasePublicEndpointAPIFixture(t, "postgresql")
	ctx := context.Background()
	d.Observation = database.Observation{Status: "ready", Revision: d.Revision, ObservedAt: time.Now().UTC(), TopologyFingerprint: "synthetic-reviewed-topology", Endpoints: []database.Endpoint{{Purpose: "read_write", Port: 5432}}, TLS: &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "synthetic-reviewed-leaf"}}
	if err := s.ObserveDatabase(ctx, d.ID, d.Revision, d.Observation); err != nil {
		t.Fatal(err)
	}
	endpoint := database.PublicEndpoint{ID: store.NewID(), DatabaseID: d.ID, Revision: 1, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 15432}, Status: "pending"}
	review, err := database.PlanPublicEndpoint(d, endpoint.Spec, endpoint.Allocation, endpoint.ID, 0, time.Now())
	if err != nil || len(review.BlockedReasons) != 0 {
		t.Fatal("invalid worker fixture review", review.BlockedReasons, err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,revision,spec,allocation,status) VALUES($1,$2,$3,$4,$5,$6)", endpoint.ID, d.ID, endpoint.Revision, store.JSON(endpoint.Spec), store.JSON(endpoint.Allocation), endpoint.Status); err != nil {
		t.Fatal(err)
	}
	id := store.NewID()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoint_operations(id,endpoint_id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,review,status,phase) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'publish',$9,'queued',$10)", id, endpoint.ID, d.ID, 1, owner.ID, owner.KeyID, "synthetic-worker-operation", []byte("synthetic-request"), store.JSON(review), phase); err != nil {
		t.Fatal(err)
	}
	return &Server{Store: s}, owner, id, &publicEndpointWorkerRuntime{observation: d.Observation}
}

func runPublicEndpointWorker(t *testing.T, server *Server, owner store.Principal, id string, runtime databasePublicEndpointRuntime) database.PublicEndpointOperation {
	t.Helper()
	ctx := context.Background()
	if _, err := server.Store.Pool.Exec(ctx, "UPDATE managed_database_public_endpoint_operations SET next_attempt_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	server.reconcileDatabasePublicEndpointWithRuntime(ctx, runtime)
	op, err := server.Store.DatabasePublicEndpointOperation(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func enableWorkerEndpointAuthority(t *testing.T, server *Server, owner store.Principal, id string, authority *workerEndpointAuthority) {
	t.Helper()
	op, err := server.Store.DatabasePublicEndpointOperation(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	op.Review.AuthorityFingerprint = strings.Repeat("a", 64)
	if _, err = server.Store.Pool.Exec(context.Background(), "UPDATE managed_database_public_endpoint_operations SET review=$2 WHERE id=$1", id, store.JSON(op.Review)); err != nil {
		t.Fatal(err)
	}
	server.DatabasePublicEndpointAuthority = authority
}

func TestDatabasePublicEndpointCloudPublicationKeepsProviderOwnership(t *testing.T) {
	server, owner, id, runtime := publicEndpointWorkerFixture(t, "health")
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	var op database.PublicEndpointOperation
	for _, phase := range []string{"identity", "tls", "provider", "publishing", "observing", "configured"} {
		op = runPublicEndpointWorker(t, server, owner, id, runtime)
		if op.Phase != phase {
			t.Fatal("Cloud publication lost its durable provider phase", phase, op.Status, op.Phase, op.Message)
		}
	}
	if op.Status != "succeeded" || authority.ensures != 3 || authority.verifies != 3 || runtime.published != 1 || runtime.observed != 1 {
		t.Fatal("Cloud publication skipped provider or route verification", op.Status, authority, runtime)
	}
}

func TestDatabasePublicEndpointCloudRetryClosesRouteAndRetainsProvider(t *testing.T) {
	for _, failure := range []string{"authority", "health", "tls", "route observation", "publication"} {
		t.Run(failure, func(t *testing.T) {
			server, owner, id, runtime := publicEndpointWorkerFixture(t, "publishing")
			authority := &workerEndpointAuthority{}
			enableWorkerEndpointAuthority(t, server, owner, id, authority)
			switch failure {
			case "authority":
				authority.ensureErr = errors.New("provider unavailable")
			case "health":
				runtime.observeErr = errors.New("observation unavailable")
			case "tls":
				runtime.backendErr = errors.New("backend TLS unavailable")
			case "route observation":
				if _, err := server.Store.Pool.Exec(context.Background(), "UPDATE managed_database_public_endpoint_operations SET phase='observing' WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
				runtime.endpointObserveErr = errors.New("route observation unavailable")
			case "publication":
				runtime.publishErr = errors.New("partial HAProxy reload")
			}
			op := runPublicEndpointWorker(t, server, owner, id, runtime)
			if op.Status != "queued" || op.Phase != "provider" || runtime.prepared != 1 {
				t.Fatal("transient Cloud retry lost provider ownership or skipped closure", op.Status, op.Phase, runtime)
			}
		})
	}
}

func TestDatabasePublicEndpointCloudClosureFailureKeepsPublishedPhase(t *testing.T) {
	server, owner, id, runtime := publicEndpointWorkerFixture(t, "publishing")
	authority := &workerEndpointAuthority{ensureErr: errors.New("provider unavailable")}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	runtime.prepareErr = errors.New("HAProxy drain unavailable")
	op := runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "queued" || op.Phase != "publishing" || runtime.prepared != 1 {
		t.Fatal("failed route closure discarded the may-be-published state", op.Status, op.Phase, runtime.prepared)
	}
	runtime.prepareErr = nil
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "queued" || op.Phase != "provider" || runtime.prepared != 2 {
		t.Fatal("provider retry advanced before the published route closed", op.Status, op.Phase, runtime.prepared)
	}
}

func TestDatabasePublicEndpointCloudClosurePrecedesFailureAndCancellation(t *testing.T) {
	server, owner, id, runtime := publicEndpointWorkerFixture(t, "observing")
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	runtime.validateErr = errors.New("allocation drift")
	op := runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "queued" || op.Phase != "failing_provider" || runtime.prepared != 1 || authority.deletes != 0 {
		t.Fatal("permanent Cloud failure did not close before provider cleanup", op.Status, op.Phase, runtime.prepared, authority.deletes)
	}
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "failed" || op.Phase != "provider" || authority.deletes != 1 {
		t.Fatal("provider cleanup did not complete the failed operation", op.Status, op.Phase, authority.deletes)
	}

	server, owner, id, runtime = publicEndpointWorkerFixture(t, "publishing")
	authority = &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, id, authority)
	if _, err := server.Store.Pool.Exec(context.Background(), "UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE id=$1", owner.KeyID); err != nil {
		t.Fatal(err)
	}
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "queued" || op.Phase != "cancelling_provider" || runtime.prepared != 1 || authority.deletes != 0 {
		t.Fatal("authorization cancellation did not close before provider cleanup", op.Status, op.Phase, runtime.prepared, authority.deletes)
	}
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "cancelled" || op.Phase != "authorization" || authority.deletes != 1 {
		t.Fatal("authorization cancellation skipped durable provider cleanup", op.Status, op.Phase, authority.deletes)
	}
}

func TestDatabasePublicEndpointObservingRevalidatesCurrentBackendBeforeSuccess(t *testing.T) {
	for _, kind := range []string{"ready", "expired", "topology", "backend_tls", "dns", "unhealthy"} {
		t.Run(kind, func(t *testing.T) {
			server, owner, id, runtime := publicEndpointWorkerFixture(t, "observing")
			wantStatus, wantPhase := "succeeded", "configured"
			switch kind {
			case "expired":
				if _, err := server.Store.Pool.Exec(context.Background(), "UPDATE managed_database_public_endpoint_operations SET started_at=now()-interval '31 minutes' WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantPhase = "failed", "timeout"
			case "topology":
				runtime.observation.TopologyFingerprint = "changed-topology"
				wantStatus, wantPhase = "failed", "review"
			case "backend_tls":
				runtime.backendErr = errors.New("synthetic changed member TLS")
				wantStatus, wantPhase = "queued", "tls"
			case "dns":
				runtime.dnsErr = errors.New("synthetic changed DNS")
				wantStatus, wantPhase = "failed", "dns"
			case "unhealthy":
				runtime.observeErr = errors.New("synthetic unavailable observation")
				wantStatus, wantPhase = "queued", "identity"
			}
			op := runPublicEndpointWorker(t, server, owner, id, runtime)
			if op.Status != wantStatus || op.Phase != wantPhase {
				t.Fatal("resumed acknowledgement bypassed current checks", op.Status, op.Phase, op.Message)
			}
			if kind == "ready" {
				if runtime.backends != 1 || runtime.observed != 1 || runtime.published != 0 {
					t.Fatal("final success skipped backend validation or republished indefinitely", runtime)
				}
			} else if runtime.prepared != 1 || runtime.observed != 0 || runtime.published != 0 {
				t.Fatal("failed final validation did not close the route first", runtime)
			}
		})
	}
}

func TestDatabasePublicEndpointIdentityIntentSurvivesInterruptedSANWrite(t *testing.T) {
	server, owner, id, runtime := publicEndpointWorkerFixture(t, "health")
	op := runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Phase != "identity" || runtime.identities != 0 {
		t.Fatal("identity mutation preceded durable intent", op.Phase, runtime.identities)
	}
	runtime.onIdentity = func() error {
		// Model a process disappearing after Secret mutation, before its next
		// operation write. Its expired lease must fence that write.
		_, err := server.Store.Pool.Exec(context.Background(), "WITH expired AS (UPDATE managed_database_public_endpoint_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 RETURNING database_id,lease) UPDATE managed_databases d SET maintenance_lease_until=clock_timestamp()-interval '1 second' FROM expired e WHERE d.id=e.database_id AND d.maintenance_lease=e.lease", id)
		return err
	}
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Phase != "identity" || op.Status != "running" || runtime.identities != 1 {
		t.Fatal("interrupted mutation lost its durable identity phase", op.Status, op.Phase)
	}
	runtime.onIdentity = nil
	runtime.observation.Status, runtime.observation.TLS = "pending", nil
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Phase != "identity" || op.Status != "queued" || runtime.identities != 1 || runtime.published != 0 {
		t.Fatal("expected leaf reload failed or published", op.Status, op.Phase)
	}
	runtime.observation.Status = "ready"
	runtime.observation.TLS = &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "synthetic-new-leaf"}
	for _, phase := range []string{"tls", "publishing", "observing", "configured"} {
		op = runPublicEndpointWorker(t, server, owner, id, runtime)
		if op.Phase != phase {
			t.Fatal("restart did not converge through checked publication", phase, op.Status, op.Phase, op.Message)
		}
	}
	if op.Status != "succeeded" || runtime.published != 1 || runtime.observed != 1 || runtime.backends != 3 {
		t.Fatal("restarted operation skipped checks or looped", op.Status, runtime)
	}
}

func TestDatabasePublicEndpointFailedClosureRetainsIdentityAndReviewChecks(t *testing.T) {
	server, owner, id, runtime := publicEndpointWorkerFixture(t, "observing")
	runtime.backendErr, runtime.prepareErr = errors.New("changed TLS"), errors.New("proxy unavailable")
	op := runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Phase != "observing" || runtime.observed != 0 {
		t.Fatal("failed closure lost identity or recorded success", op.Phase)
	}
	runtime.prepareErr = nil
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Phase != "tls" {
		t.Fatal("closed publication did not retain the post-identity retry phase", op.Phase)
	}
	runtime.observeErr = errors.New("partial observation error")
	runtime.observation.TopologyFingerprint = "changed-topology"
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "failed" || op.Phase != "review" || runtime.published != 0 || runtime.observed != 0 {
		t.Fatal("observation error bypassed changed topology", op.Status, op.Phase)
	}
}

func acceptedPublicEndpointWorkerFixture(t *testing.T) (*Server, store.Principal, database.PublicEndpointOperation, *publicEndpointWorkerRuntime) {
	t.Helper()
	s, _, owner, d := databasePublicEndpointAPIFixture(t, "postgresql")
	d.Observation = database.Observation{Status: "ready", Revision: d.Revision, ObservedAt: time.Now().UTC(), TopologyFingerprint: "synthetic-accepted-topology", Endpoints: []database.Endpoint{{Purpose: "read_write", Port: 5432}}, TLS: &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "synthetic-accepted-leaf"}}
	ctx := context.Background()
	if err := s.ObserveDatabase(ctx, d.ID, d.Revision, d.Observation); err != nil {
		t.Fatal(err)
	}
	allocation := database.PublicEndpointAllocation{ID: "allocation", Host: "database.example.test", Address: "192.0.2.10", Port: 15432}
	input := database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 8}
	reviewID, review, err := s.ReserveDatabasePublicEndpointReview(ctx, owner, d, input, []database.PublicEndpointAllocation{allocation}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.AcceptDatabasePublicEndpoint(ctx, owner, d.ID, reviewID, "synthetic-real-accept", d.Revision, review.EndpointRevision)
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != "accepted" {
		t.Fatal("real acceptance no longer uses the persisted default phase", op.Phase)
	}
	return &Server{Store: s}, owner, op, &publicEndpointWorkerRuntime{observation: d.Observation}
}

func cloudRevocationWorkerFixture(t *testing.T) (*Server, store.Principal, database.PublicEndpointOperation, *revocationEndpointWorkerRuntime, *workerEndpointAuthority) {
	t.Helper()
	server, owner, published, runtime := acceptedPublicEndpointWorkerFixture(t)
	authority := &workerEndpointAuthority{}
	enableWorkerEndpointAuthority(t, server, owner, published.ID, authority)
	for attempt := 0; attempt < 10 && published.Status != "succeeded"; attempt++ {
		published = runPublicEndpointWorker(t, server, owner, published.ID, runtime)
	}
	if published.Status != "succeeded" {
		t.Fatal("Cloud revocation fixture did not publish", published.Status, published.Phase)
	}
	endpoint, err := server.Store.DatabasePublicEndpointInternal(context.Background(), published.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	revoke, err := server.Store.RevokeDatabasePublicEndpoint(context.Background(), owner, published.DatabaseID, published.EndpointID, "durable-revoke-cleanup", endpoint.Revision)
	if err != nil {
		t.Fatal(err)
	}
	authority.ensures, authority.verifies, authority.deletes = 0, 0, 0
	return server, owner, revoke, &revocationEndpointWorkerRuntime{publicEndpointWorkerRuntime: &publicEndpointWorkerRuntime{observation: runtime.observation}}, authority
}

func TestDatabasePublicEndpointAcceptedDefaultClosesBeforeHealthOrIdentity(t *testing.T) {
	for _, kind := range []string{"new", "replacement", "revoke"} {
		t.Run(kind, func(t *testing.T) {
			server, owner, op, runtime := acceptedPublicEndpointWorkerFixture(t)
			if kind != "new" {
				for attempt := 0; attempt < 8 && op.Status != "succeeded"; attempt++ {
					op = runPublicEndpointWorker(t, server, owner, op.ID, runtime)
				}
				if op.Status != "succeeded" {
					t.Fatal("initial accepted fixture did not publish", op.Status, op.Phase)
				}
				ctx := context.Background()
				d, err := server.Store.DatabaseInternal(ctx, op.DatabaseID)
				if err != nil {
					t.Fatal(err)
				}
				endpoint, err := server.Store.DatabasePublicEndpointInternal(ctx, op.EndpointID)
				if err != nil || endpoint.Status != "active" {
					t.Fatal("replacement fixture is not active", err)
				}
				if kind == "replacement" {
					input := endpoint.Spec
					input.MaxConnections++
					reviewID, review, err := server.Store.ReserveDatabasePublicEndpointReview(ctx, owner, d, input, []database.PublicEndpointAllocation{endpoint.Allocation}, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					op, err = server.Store.AcceptDatabasePublicEndpoint(ctx, owner, d.ID, reviewID, "synthetic-real-replacement", d.Revision, review.EndpointRevision)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					op, err = server.Store.RevokeDatabasePublicEndpoint(ctx, owner, d.ID, endpoint.ID, "synthetic-real-revoke", endpoint.Revision)
					if err != nil {
						t.Fatal(err)
					}
				}
				if op.Phase != "accepted" {
					t.Fatal("replacement or revoke skipped actual phase default", op.Phase)
				}
				runtime = &publicEndpointWorkerRuntime{observation: runtime.observation}
			}
			runtime.prepareErr = errors.New("synthetic unavailable proxy")
			for attempt := 1; attempt <= 2; attempt++ {
				op = runPublicEndpointWorker(t, server, owner, op.ID, runtime)
				if op.Status != "queued" || op.Phase != "closing" || runtime.healthChecks != 0 || runtime.identities != 0 || runtime.prepared+runtime.removed != attempt {
					t.Fatal("accepted default advanced before closure", op.Status, op.Phase, runtime)
				}
			}
			runtime.prepareErr = nil
			op = runPublicEndpointWorker(t, server, owner, op.ID, runtime)
			wantPhase := "health"
			if kind == "revoke" {
				wantPhase = "identity"
			}
			if op.Phase != wantPhase || runtime.healthChecks != 0 || runtime.identities != 0 || runtime.prepared+runtime.removed != 3 {
				t.Fatal("closure and next-stage work were not separated", op.Phase, runtime)
			}
		})
	}
}

func TestDatabasePublicEndpointUnknownPhaseClosesAndFailsWithoutPublishing(t *testing.T) {
	server, owner, id, runtime := publicEndpointWorkerFixture(t, "unrecognized-persisted-phase")
	runtime.prepareErr = errors.New("synthetic unavailable proxy")
	op := runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "queued" || op.Phase != "invalid_closing" {
		t.Fatal("unknown phase did not retain terminal closure intent", op.Status, op.Phase)
	}
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Phase != "invalid_closing" {
		t.Fatal("unknown phase retry advanced toward publication", op.Phase)
	}
	runtime.prepareErr = nil
	op = runPublicEndpointWorker(t, server, owner, id, runtime)
	if op.Status != "failed" || op.Phase != "phase" || runtime.prepared != 3 || runtime.healthChecks != 0 || runtime.identities != 0 || runtime.backends != 0 || runtime.observed != 0 || runtime.published != 0 {
		t.Fatal("unknown phase published or failed before closure", op.Status, op.Phase, runtime)
	}
}

func TestDatabasePublicEndpointRevocationWaitsForWithdrawnAuthorityCleanup(t *testing.T) {
	server, owner, published, runtime := acceptedPublicEndpointWorkerFixture(t)
	for attempt := 0; attempt < 8 && published.Status != "succeeded"; attempt++ {
		published = runPublicEndpointWorker(t, server, owner, published.ID, runtime)
	}
	if published.Status != "succeeded" {
		t.Fatal("publication fixture did not converge", published.Status, published.Phase)
	}
	endpoint, err := server.Store.DatabasePublicEndpointInternal(context.Background(), published.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	revoke, err := server.Store.RevokeDatabasePublicEndpoint(context.Background(), owner, published.DatabaseID, published.EndpointID, "withdrawn-authority-cleanup", endpoint.Revision)
	if err != nil {
		t.Fatal(err)
	}
	authority := &cleanupOnlyEndpointAuthority{deleteErr: errors.New("cleanup credentials unavailable")}
	server.DatabasePublicEndpointAuthority = authority
	runtime = &publicEndpointWorkerRuntime{observation: runtime.observation}
	revoke = runPublicEndpointWorker(t, server, owner, revoke.ID, runtime)
	if revoke.Phase != "identity" {
		t.Fatal("revocation did not close the route first", revoke.Phase)
	}
	revoke = runPublicEndpointWorker(t, server, owner, revoke.ID, runtime)
	if revoke.Phase != "provider_delete" {
		t.Fatal("revocation skipped durable provider cleanup", revoke.Phase)
	}
	revoke = runPublicEndpointWorker(t, server, owner, revoke.ID, runtime)
	if revoke.Status != "queued" || revoke.Phase != "provider_delete" || authority.deletes != 1 || !strings.Contains(revoke.Message, "restore database endpoint cleanup credentials") {
		t.Fatal("missing cleanup credentials released the endpoint or hid the operator action", revoke.Status, revoke.Phase, revoke.Message, authority.deletes)
	}
}

func TestDatabasePublicEndpointAcceptedRevocationIgnoresLaterInitiatingKeyWithdrawal(t *testing.T) {
	targets := []struct {
		phase, next string
	}{
		{phase: "accepted", next: "identity"},
		{phase: "identity", next: "provider_delete"},
		{phase: "provider_delete", next: "release"},
		{phase: "release", next: "revoked"},
	}
	for _, credential := range []string{"revoked", "expired"} {
		for _, target := range targets {
			t.Run(credential+"_at_"+target.phase, func(t *testing.T) {
				server, owner, operation, runtime, authority := cloudRevocationWorkerFixture(t)
				for attempt := 0; attempt < 4 && operation.Phase != target.phase; attempt++ {
					operation = runPublicEndpointWorker(t, server, owner, operation.ID, runtime)
				}
				if operation.Phase != target.phase {
					t.Fatal("revocation fixture did not reach target phase", target.phase, operation.Status, operation.Phase)
				}
				removed, identities, deletes, releases := runtime.removed, runtime.identities, authority.deletes, runtime.releases
				query := "UPDATE api_keys SET revoked_at=now() WHERE id=$1"
				if credential == "expired" {
					query = "UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE id=$1"
				}
				if _, err := server.Store.Pool.Exec(context.Background(), query, owner.KeyID); err != nil {
					t.Fatal(err)
				}
				operation = runPublicEndpointWorker(t, server, owner, operation.ID, runtime)
				if operation.Phase != target.next || operation.Status == "cancelled" || strings.HasPrefix(operation.Phase, "cancelling") {
					t.Fatal("initiating key withdrawal diverted accepted revocation", operation.Status, operation.Phase)
				}
				switch target.phase {
				case "accepted":
					if runtime.removed != removed+1 {
						t.Fatal("accepted revocation did not close its route", runtime.removed)
					}
				case "identity":
					if runtime.identities != identities+1 {
						t.Fatal("revocation did not remove its database identity", runtime.identities)
					}
				case "provider_delete":
					if authority.deletes != deletes+1 {
						t.Fatal("revocation did not remove its provider resource", authority.deletes)
					}
				case "release":
					if runtime.releases != releases+1 || operation.Status != "succeeded" {
						t.Fatal("revocation did not release its allocation", operation.Status, runtime.releases)
					}
				}
			})
		}
	}
}
