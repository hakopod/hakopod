package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

const databasePublicEndpointCols = `id,database_id,revision,spec,allocation,member_allocations,status,observation,created_at,updated_at,revoked_at`
const databasePublicEndpointOperationCols = `id,endpoint_id,database_id,revision,kind,status,phase,message,review,created_at,started_at,finished_at,identity_id,key_id,lease,identity_transition`

var databasePublicEndpointAuthorityFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func scanDatabasePublicEndpoint(row scanner) (database.PublicEndpoint, error) {
	var endpoint database.PublicEndpoint
	err := row.Scan(&endpoint.ID, &endpoint.DatabaseID, &endpoint.Revision, &endpoint.Spec, &endpoint.Allocation, &endpoint.MemberAllocations, &endpoint.Status, &endpoint.Observation, &endpoint.CreatedAt, &endpoint.UpdatedAt, &endpoint.RevokedAt)
	if err == nil {
		_, err = database.PublicEndpointAllocations(endpoint)
	}
	return endpoint, err
}

func scanDatabasePublicEndpointOperation(row scanner) (database.PublicEndpointOperation, error) {
	var operation database.PublicEndpointOperation
	err := row.Scan(&operation.ID, &operation.EndpointID, &operation.DatabaseID, &operation.Revision, &operation.Kind, &operation.Status, &operation.Phase, &operation.Message, &operation.Review, &operation.CreatedAt, &operation.StartedAt, &operation.FinishedAt, &operation.IdentityID, &operation.KeyID, &operation.Lease, &operation.IdentityTransition)
	return operation, err
}

func (s *Store) DatabasePublicEndpoints(ctx context.Context, p Principal, databaseID string) ([]database.PublicEndpoint, error) {
	if _, err := s.Database(ctx, p, databaseID, false); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE database_id=$1 AND revoked_at IS NULL ORDER BY created_at,id LIMIT $2", databaseID, database.MaxPublicEndpoints+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	endpoints := []database.PublicEndpoint{}
	for rows.Next() {
		endpoint, err := scanDatabasePublicEndpoint(rows)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, endpoint)
	}
	if len(endpoints) > database.MaxPublicEndpoints {
		return nil, fmt.Errorf("database public endpoint inventory exceeds its bound")
	}
	return endpoints, rows.Err()
}

func (s *Store) DatabasePublicEndpoint(ctx context.Context, p Principal, databaseID, endpointID string, write bool) (database.PublicEndpoint, error) {
	d, err := s.Database(ctx, p, databaseID, write)
	if err != nil {
		return database.PublicEndpoint{}, err
	}
	endpoint, err := scanDatabasePublicEndpoint(s.Pool.QueryRow(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE id=$1 AND database_id=$2 AND revoked_at IS NULL", endpointID, d.ID))
	return endpoint, err
}

func (s *Store) DatabasePublicEndpointInternal(ctx context.Context, endpointID string) (database.PublicEndpoint, error) {
	return scanDatabasePublicEndpoint(s.Pool.QueryRow(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE id=$1", endpointID))
}

// ReserveDatabasePublicEndpointReview chooses from trusted operator allocations
// while holding one transaction lock. A new route owns its allocation in the
// closed review state before the review is returned to the caller.
func (s *Store) ReserveDatabasePublicEndpointReview(ctx context.Context, p Principal, d database.Resource, input database.PublicEndpointSpec, allocations []database.PublicEndpointAllocation, now time.Time, authorityFingerprint ...string) (string, database.PublicEndpointReview, error) {
	var empty database.PublicEndpointReview
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return "", empty, ErrForbidden
	}
	normalized, err := input.Normalize()
	if err != nil {
		return "", empty, fmt.Errorf("%w: %s", ErrInput, err)
	}
	if len(allocations) == 0 || len(allocations) > 256 {
		return "", empty, fmt.Errorf("%w: no bounded operator endpoint allocation is available", ErrConflict)
	}
	fingerprint := ""
	if len(authorityFingerprint) > 1 || len(authorityFingerprint) == 1 && authorityFingerprint[0] != "" && !databasePublicEndpointAuthorityFingerprintPattern.MatchString(authorityFingerprint[0]) {
		return "", empty, fmt.Errorf("%w: endpoint authority fingerprint is invalid", ErrInput)
	}
	if len(authorityFingerprint) == 1 {
		fingerprint = authorityFingerprint[0]
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", empty, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044266)"); err != nil {
		return "", empty, err
	}
	current, err := scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", d.ID))
	if err != nil {
		return "", empty, err
	}
	if current.Revision != d.Revision || current.Project != d.Project || current.Environment != d.Environment {
		return "", empty, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_database_public_endpoints e SET status='revoked',revoked_at=now(),updated_at=now() WHERE e.status='review' AND e.revoked_at IS NULL AND NOT EXISTS (SELECT 1 FROM managed_database_public_endpoint_reviews r WHERE r.endpoint_id=e.id AND r.consumed_at IS NULL AND r.expires_at>now())`); err != nil {
		return "", empty, err
	}
	var endpoint database.PublicEndpoint
	endpoint, err = scanDatabasePublicEndpoint(tx.QueryRow(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE database_id=$1 AND spec->>'purpose'=$2 AND revoked_at IS NULL FOR UPDATE", d.ID, normalized.Purpose))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", empty, err
	}
	if endpoint.ID == "" {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoints WHERE database_id=$1 AND revoked_at IS NULL", d.ID).Scan(&count); err != nil {
			return "", empty, err
		}
		if count >= database.MaxPublicEndpoints {
			return "", empty, fmt.Errorf("%w: a database supports at most %d public endpoints", ErrConflict, database.MaxPublicEndpoints)
		}
		members := append([]database.Member(nil), current.Observation.Members...)
		slices.SortFunc(members, func(a, b database.Member) int { return strings.Compare(a.Name, b.Name) })
		needed := 1
		if database.PublicEndpointRequiresMembers(current.Spec) {
			needed = current.Spec.Members()
			if needed < 1 || needed > database.MaxMembers || len(members) != needed {
				return "", empty, fmt.Errorf("%w: every database member needs a current observation", ErrConflict)
			}
			for i, member := range members {
				if !member.Ready || member.UID == "" || i > 0 && members[i-1].Name == member.Name {
					return "", empty, fmt.Errorf("%w: database member identities are not ready", ErrConflict)
				}
			}
		}
		selected := []database.PublicEndpointAllocation{}
		for _, candidate := range allocations {
			if _, err = database.PublicEndpointAllocations(database.PublicEndpoint{Allocation: candidate}); err != nil {
				return "", empty, fmt.Errorf("%w: invalid operator allocation", ErrInput)
			}
			var used bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_public_endpoint_allocations WHERE allocation_id=$1 OR host=$2 OR address=$3::inet AND port=$4)", candidate.ID, candidate.Host, candidate.Address, candidate.Port).Scan(&used); err != nil {
				return "", empty, err
			}
			if !used {
				for _, prior := range selected {
					if prior.ID == candidate.ID || prior.Host == candidate.Host || prior.Address == candidate.Address && prior.Port == candidate.Port {
						used = true
						break
					}
				}
				if !used {
					selected = append(selected, candidate)
				}
				if len(selected) == needed {
					break
				}
			}
		}
		if len(selected) != needed {
			return "", empty, fmt.Errorf("%w: not enough unreserved operator allocations for every database member", ErrConflict)
		}
		endpoint = database.PublicEndpoint{ID: NewID(), DatabaseID: d.ID, Spec: normalized, Allocation: selected[0], Status: "review"}
		if database.PublicEndpointRequiresMembers(current.Spec) {
			for i, member := range members {
				endpoint.MemberAllocations = append(endpoint.MemberAllocations, database.PublicEndpointMemberAllocation{MemberName: member.Name, MemberUID: member.UID, Allocation: selected[i]})
			}
		}
		memberAllocations := endpoint.MemberAllocations
		if memberAllocations == nil {
			memberAllocations = []database.PublicEndpointMemberAllocation{}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,spec,allocation,member_allocations) VALUES($1,$2,$3,$4,$5)", endpoint.ID, endpoint.DatabaseID, JSON(endpoint.Spec), JSON(endpoint.Allocation), JSON(memberAllocations)); err != nil {
			return "", empty, err
		}
	} else if endpoint.Status == "pending" || endpoint.Status == "revoking" {
		return "", empty, fmt.Errorf("%w: a public endpoint operation is already in progress", ErrConflict)
	}
	plan, err := database.PlanPublicEndpointMembers(current, normalized, endpoint, now)
	if err != nil {
		return "", empty, fmt.Errorf("%w: %s", ErrInput, err)
	}
	if len(plan.BlockedReasons) > 0 {
		return "", plan, fmt.Errorf("%w: public endpoint review is blocked: %s", ErrConflict, plan.BlockedReasons[0])
	}
	plan.AuthorityFingerprint = fingerprint
	if _, err = tx.Exec(ctx, "DELETE FROM managed_database_public_endpoint_reviews WHERE endpoint_id=$1 AND (expires_at<=now() OR consumed_at IS NOT NULL)", endpoint.ID); err != nil {
		return "", empty, err
	}
	var pending int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_database_public_endpoint_reviews WHERE endpoint_id=$1 AND consumed_at IS NULL AND expires_at>now()", endpoint.ID).Scan(&pending); err != nil {
		return "", empty, err
	}
	if pending >= 4 {
		return "", empty, fmt.Errorf("%w: wait for an existing endpoint review to expire", ErrConflict)
	}
	reviewID := NewID()
	if _, err = tx.Exec(ctx, "INSERT INTO managed_database_public_endpoint_reviews(id,endpoint_id,database_id,identity_id,database_revision,endpoint_revision,payload,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", reviewID, endpoint.ID, d.ID, p.ID, d.Revision, endpoint.Revision, JSON(plan), plan.ExpiresAt); err != nil {
		return "", empty, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.public_endpoint.review',$3,$4)", p.ID, p.KeyID, endpoint.ID, JSON(map[string]any{"database_id": d.ID, "database_revision": d.Revision, "endpoint_revision": endpoint.Revision, "purpose": normalized.Purpose})); err != nil {
		return "", empty, err
	}
	return reviewID, plan, tx.Commit(ctx)
}

func (s *Store) AcceptDatabasePublicEndpoint(ctx context.Context, p Principal, databaseID, reviewID, idem string, expectedDatabaseRevision, expectedEndpointRevision int64) (database.PublicEndpointOperation, error) {
	return s.acceptDatabasePublicEndpoint(ctx, p, databaseID, "", reviewID, idem, "publish", expectedDatabaseRevision, expectedEndpointRevision)
}

func (s *Store) RevokeDatabasePublicEndpoint(ctx context.Context, p Principal, databaseID, endpointID, idem string, expectedEndpointRevision int64) (database.PublicEndpointOperation, error) {
	return s.acceptDatabasePublicEndpoint(ctx, p, databaseID, endpointID, "", idem, "revoke", 0, expectedEndpointRevision)
}

func (s *Store) acceptDatabasePublicEndpoint(ctx context.Context, p Principal, databaseID, endpointID, reviewID, idem, kind string, expectedDatabaseRevision, expectedEndpointRevision int64) (database.PublicEndpointOperation, error) {
	var empty database.PublicEndpointOperation
	if len(idem) < 8 || len(idem) > 128 || expectedEndpointRevision < 0 || kind != "publish" && kind != "revoke" {
		return empty, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,66))", p.ID+":"+idem); err != nil {
		return empty, err
	}
	var oldHash []byte
	var oldID string
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM managed_database_public_endpoint_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&oldID, &oldHash)
	requestHash := sha256.Sum256(JSON(struct {
		DatabaseID, EndpointID, ReviewID, Kind string
		DatabaseRevision, EndpointRevision     int64
	}{databaseID, endpointID, reviewID, kind, expectedDatabaseRevision, expectedEndpointRevision}))
	if err == nil {
		if !bytes.Equal(oldHash, requestHash[:]) {
			return empty, ErrConflict
		}
		operation, scanErr := scanDatabasePublicEndpointOperation(tx.QueryRow(ctx, "SELECT "+databasePublicEndpointOperationCols+" FROM managed_database_public_endpoint_operations WHERE id=$1", oldID))
		if scanErr != nil {
			return empty, scanErr
		}
		if _, authErr := s.Database(ctx, p, operation.DatabaseID, true); authErr != nil {
			return empty, authErr
		}
		return operation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	d, err := scanDatabase(tx.QueryRow(ctx, "SELECT "+databaseCols+" FROM managed_databases WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", databaseID))
	if err != nil {
		return empty, err
	}
	if !p.AllowsDatabase(d.Project, d.Environment, true) {
		return empty, ErrForbidden
	}
	idle, err := databaseMaintenanceIdle(ctx, tx, d.ID)
	if err != nil {
		return empty, err
	}
	var lifecycleBusy bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_operations WHERE database_id=$1 AND status IN ('queued','running'))", d.ID).Scan(&lifecycleBusy); err != nil {
		return empty, err
	}
	if !idle || lifecycleBusy {
		return empty, fmt.Errorf("%w: database lifecycle or identity maintenance is in progress", ErrConflict)
	}
	var review *database.PublicEndpointReview
	if kind == "publish" {
		var plan database.PublicEndpointReview
		err = tx.QueryRow(ctx, "SELECT payload FROM managed_database_public_endpoint_reviews WHERE id=$1 AND database_id=$2 AND identity_id=$3 AND database_revision=$4 AND endpoint_revision=$5 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", reviewID, databaseID, p.ID, expectedDatabaseRevision, expectedEndpointRevision).Scan(&plan)
		if err != nil {
			return empty, ErrConflict
		}
		endpointID = plan.EndpointID
		current, err := scanDatabasePublicEndpoint(tx.QueryRow(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE id=$1 AND database_id=$2 AND revoked_at IS NULL FOR UPDATE", endpointID, databaseID))
		if err != nil || current.Revision != expectedEndpointRevision || d.Revision != expectedDatabaseRevision || !plan.ExpiresAt.After(time.Now()) || current.Allocation != plan.Allocation || !slices.Equal(current.MemberAllocations, plan.MemberAllocations) {
			return empty, ErrConflict
		}
		next, planErr := database.PlanPublicEndpointMembers(d, plan.Spec, current, time.Now().UTC())
		if planErr != nil || !plan.MatchesRoute(next) || len(next.BlockedReasons) > 0 || next.TopologyFingerprint != plan.TopologyFingerprint || next.TLSFingerprint != plan.TLSFingerprint {
			return empty, fmt.Errorf("%w: database health, topology or TLS identity changed; review the endpoint again", ErrConflict)
		}
		if _, err = tx.Exec(ctx, "UPDATE managed_database_public_endpoint_reviews SET consumed_at=now() WHERE id=$1", reviewID); err != nil {
			return empty, err
		}
		if _, err = tx.Exec(ctx, "UPDATE managed_database_public_endpoints SET revision=revision+1,status='pending',observation='{}',updated_at=now() WHERE id=$1", endpointID); err != nil {
			return empty, err
		}
		review = &plan
	} else {
		current, err := scanDatabasePublicEndpoint(tx.QueryRow(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE id=$1 AND database_id=$2 AND revoked_at IS NULL FOR UPDATE", endpointID, databaseID))
		if err != nil || current.Revision != expectedEndpointRevision || current.Status == "review" || current.Status == "pending" || current.Status == "revoking" {
			return empty, ErrConflict
		}
		if _, err = tx.Exec(ctx, "UPDATE managed_database_public_endpoints SET revision=revision+1,status='revoking',updated_at=now() WHERE id=$1", endpointID); err != nil {
			return empty, err
		}
	}
	operation, err := scanDatabasePublicEndpointOperation(tx.QueryRow(ctx, "INSERT INTO managed_database_public_endpoint_operations(id,endpoint_id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,review) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING "+databasePublicEndpointOperationCols, NewID(), endpointID, databaseID, expectedEndpointRevision+1, p.ID, p.KeyID, idem, requestHash[:], kind, review))
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, "database.public_endpoint."+kind, endpointID, JSON(map[string]any{"database_id": databaseID, "revision": operation.Revision, "operation_id": operation.ID})); err != nil {
		return empty, err
	}
	return operation, tx.Commit(ctx)
}

func (s *Store) DatabasePublicEndpointOperation(ctx context.Context, p Principal, id string) (database.PublicEndpointOperation, error) {
	operation, err := scanDatabasePublicEndpointOperation(s.Pool.QueryRow(ctx, "SELECT "+databasePublicEndpointOperationCols+" FROM managed_database_public_endpoint_operations WHERE id=$1", id))
	if err != nil {
		return operation, err
	}
	if _, err = s.Database(ctx, p, operation.DatabaseID, false); err != nil {
		return database.PublicEndpointOperation{}, err
	}
	return operation, nil
}

func (s *Store) ClaimDatabasePublicEndpointOperation(ctx context.Context) (database.PublicEndpointOperation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return database.PublicEndpointOperation{}, err
	}
	defer tx.Rollback(ctx)
	var operationID, databaseID string
	err = tx.QueryRow(ctx, `SELECT o.id,o.database_id FROM managed_database_public_endpoint_operations o JOIN managed_databases d ON d.id=o.database_id WHERE ((o.status='queued' AND o.next_attempt_at<=clock_timestamp()) OR (o.status='running' AND o.lease_until<clock_timestamp())) AND d.deleted_at IS NULL AND (d.maintenance_lease_until IS NULL OR d.maintenance_lease_until<clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM managed_database_operations m WHERE m.database_id=o.database_id AND m.status IN ('queued','running')) ORDER BY o.next_attempt_at,o.created_at,o.id FOR UPDATE OF o,d SKIP LOCKED LIMIT 1`).Scan(&operationID, &databaseID)
	if err != nil {
		return database.PublicEndpointOperation{}, err
	}
	lease := NewID()
	if _, err = tx.Exec(ctx, "UPDATE managed_databases SET maintenance_lease=$2,maintenance_lease_until=clock_timestamp()+interval '90 seconds' WHERE id=$1", databaseID, lease); err != nil {
		return database.PublicEndpointOperation{}, err
	}
	operation, err := scanDatabasePublicEndpointOperation(tx.QueryRow(ctx, "UPDATE managed_database_public_endpoint_operations SET status='running',lease=$2,lease_until=clock_timestamp()+interval '90 seconds',started_at=COALESCE(started_at,now()) WHERE id=$1 RETURNING "+databasePublicEndpointOperationCols, operationID, lease))
	if err != nil {
		return operation, err
	}
	return operation, tx.Commit(ctx)
}

func (s *Store) ExpireDatabasePublicEndpointReviews(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE managed_database_public_endpoint_reviews SET consumed_at=now() WHERE consumed_at IS NULL AND expires_at<=now()`)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE managed_database_public_endpoints e SET status='revoked',revoked_at=now(),updated_at=now() WHERE e.status='review' AND e.revoked_at IS NULL AND NOT EXISTS (SELECT 1 FROM managed_database_public_endpoint_reviews r WHERE r.endpoint_id=e.id AND r.consumed_at IS NULL AND r.expires_at>now())`)
	return err
}

func (s *Store) CheckDatabasePublicEndpointOperation(ctx context.Context, operation database.PublicEndpointOperation) error {
	if err := s.CheckDatabasePublicEndpointOperationCleanup(ctx, operation); err != nil {
		return err
	}
	d, err := s.DatabaseInternal(ctx, operation.DatabaseID)
	if err != nil {
		return err
	}
	return s.Reauthorize(ctx, operation.KeyID, d.Project, d.Environment, "")
}

// CheckDatabasePublicEndpointOperationCleanup fences fail-closed compensation
// to the worker that still owns the durable operation and database lease. It
// intentionally does not reauthorize the initiating key: cleanup must remain
// possible after that key or its project role is revoked.
func (s *Store) CheckDatabasePublicEndpointOperationCleanup(ctx context.Context, operation database.PublicEndpointOperation) error {
	var valid bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_public_endpoint_operations o JOIN managed_database_public_endpoints e ON e.id=o.endpoint_id JOIN managed_databases d ON d.id=o.database_id WHERE o.id=$1 AND o.lease=$2 AND o.lease_until>clock_timestamp() AND o.status='running' AND e.revision=o.revision AND d.deleted_at IS NULL AND d.maintenance_lease=o.lease AND d.maintenance_lease_until>clock_timestamp())", operation.ID, operation.Lease).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

// SaveDatabasePublicEndpointIdentityTransition writes transition proof only
// while this worker owns both the operation and database maintenance leases.
// The transition never carries certificate private key material.
func (s *Store) SaveDatabasePublicEndpointIdentityTransition(ctx context.Context, operation database.PublicEndpointOperation, transition database.PublicEndpointIdentityTransition) error {
	kindValid := transition.Kind == operation.Kind || operation.Kind == "publish" && operation.Phase == "cancelling_identity" && transition.Kind == "cancel"
	if transition.SchemaVersion != database.PublicEndpointIdentityTransitionSchemaVersion || transition.OperationID != operation.ID || transition.Engine != "oracle" || !kindValid || transition.DatabaseRevision < 1 || transition.EndpointRevision != operation.Revision || transition.RouteFingerprint == "" || len(transition.DesiredNames) > database.MaxPublicEndpoints || len(transition.PVCs) != 2 {
		return ErrInput
	}
	names, err := database.NormalizePublicEndpointNames(transition.DesiredNames)
	if err != nil || !slices.Equal(names, transition.DesiredNames) {
		return ErrInput
	}
	for _, pvc := range transition.PVCs {
		if pvc.Name == "" || pvc.UID == "" || pvc.VolumeName == "" || pvc.PersistentVolumeUID == "" || pvc.BackingVolumeFingerprint == "" {
			return ErrInput
		}
	}
	encoded, err := json.Marshal(transition)
	if err != nil || len(encoded) > 32<<10 {
		return ErrInput
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE managed_database_public_endpoint_operations o SET identity_transition=$3 WHERE o.id=$1 AND o.lease=$2 AND o.status='running' AND o.lease_until>clock_timestamp() AND EXISTS(SELECT 1 FROM managed_database_public_endpoints e JOIN managed_databases d ON d.id=o.database_id WHERE e.id=o.endpoint_id AND e.revision=o.revision AND d.deleted_at IS NULL AND d.maintenance_lease=o.lease AND d.maintenance_lease_until>clock_timestamp())`, operation.ID, operation.Lease, JSON(transition))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) RecordDatabasePublicEndpointStep(ctx context.Context, operation database.PublicEndpointOperation, observation database.PublicEndpointObservation, status, phase, message string) error {
	if status != "queued" && status != "succeeded" && status != "failed" && status != "cancelled" || len(phase) > 64 || len(message) > 512 {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE managed_database_public_endpoint_operations SET status=$3,phase=$4,message=$5,next_attempt_at=now()+interval '3 seconds',lease='',lease_until=NULL,finished_at=CASE WHEN $3='queued' THEN NULL ELSE now() END WHERE id=$1 AND lease=$2 AND status='running' AND lease_until>now()", operation.ID, operation.Lease, status, phase, message)
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	endpointStatus := "pending"
	if operation.Kind == "revoke" {
		endpointStatus = "revoking"
	}
	if status == "succeeded" {
		endpointStatus = "active"
		if operation.Kind == "revoke" {
			endpointStatus = "revoked"
		}
	}
	if status == "failed" || status == "cancelled" {
		endpointStatus = "error"
	}
	var acceptedSpec any
	if operation.Kind == "publish" && operation.Review != nil && status == "succeeded" {
		acceptedSpec = JSON(operation.Review.Spec)
	}
	tag, err = tx.Exec(ctx, "UPDATE managed_database_public_endpoints SET status=$3,observation=$4,spec=COALESCE($5,spec),updated_at=now(),revoked_at=CASE WHEN $3='revoked' THEN now() ELSE revoked_at END WHERE id=$1 AND revision=$2", operation.EndpointID, operation.Revision, endpointStatus, JSON(observation), acceptedSpec)
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_databases SET maintenance_lease='',maintenance_lease_until=NULL WHERE id=$1 AND maintenance_lease=$2", operation.DatabaseID, operation.Lease); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DatabasePublicEndpointNames(ctx context.Context, databaseID, exceptEndpointID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, "SELECT a.host FROM managed_database_public_endpoint_allocations a JOIN managed_database_public_endpoints e ON e.id=a.endpoint_id WHERE e.database_id=$1 AND e.revoked_at IS NULL AND e.id<>$2 AND e.status IN ('pending','active','revoking','error') ORDER BY a.host LIMIT $3", databaseID, exceptEndpointID, database.MaxPublicEndpointNames+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if len(names) > database.MaxPublicEndpointNames {
		return nil, fmt.Errorf("database public endpoint identity inventory exceeds its bound")
	}
	return names, rows.Err()
}

func (s *Store) DatabasePublicEndpointMembers(ctx context.Context, databaseID, exceptEndpointID string) ([]database.PublicEndpointMemberAllocation, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+databasePublicEndpointCols+" FROM managed_database_public_endpoints WHERE database_id=$1 AND id<>$2 AND revoked_at IS NULL AND status IN ('pending','active','revoking','error') ORDER BY id LIMIT $3", databaseID, exceptEndpointID, database.MaxPublicEndpoints+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []database.PublicEndpointMemberAllocation{}
	count := 0
	for rows.Next() {
		endpoint, err := scanDatabasePublicEndpoint(rows)
		if err != nil {
			return nil, err
		}
		count++
		if count > database.MaxPublicEndpoints || len(members)+len(endpoint.MemberAllocations) > database.MaxMembers {
			return nil, fmt.Errorf("database public member inventory exceeds its bound")
		}
		members = append(members, endpoint.MemberAllocations...)
	}
	slices.SortFunc(members, func(a, b database.PublicEndpointMemberAllocation) int {
		return strings.Compare(a.MemberName, b.MemberName)
	})
	for i := 1; i < len(members); i++ {
		if members[i-1].MemberName == members[i].MemberName {
			return nil, fmt.Errorf("database public member inventory contains duplicate members")
		}
	}
	return members, rows.Err()
}

func (s *Store) DatabasePublicEndpointAccessRequired(ctx context.Context, databaseID string) (bool, error) {
	var required bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_public_endpoints WHERE database_id=$1 AND revoked_at IS NULL AND status='active')", databaseID).Scan(&required)
	return required, err
}

func ensureNoDatabasePublicEndpoints(ctx context.Context, tx pgx.Tx, databaseID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_database_public_endpoints WHERE database_id=$1 AND revoked_at IS NULL)", databaseID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: revoke every public endpoint before deleting or restoring this database", ErrConflict)
	}
	return nil
}
