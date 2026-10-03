package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/jackc/pgx/v5"
)

const (
	MaxManagedPlatforms                = 64
	maxManagedPlatformCapacityScopes   = 1000
	MaxManagedPlatformReviews          = 4
	MaxManagedPlatformObservationBytes = 64 << 10
	MaxManagedPlatformResources        = managedplatform.MaxComponents * 5
	maxManagedPlatformPayloadBytes     = 64 << 10
)

var managedPlatformID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func lockManagedPlatformMutations(ctx context.Context, tx pgx.Tx, ids ...string) error {
	sort.Strings(ids)
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,71))", id); err != nil {
			return err
		}
	}
	return nil
}

type ManagedPlatform struct {
	ID                  string               `json:"id"`
	Project             string               `json:"project"`
	Environment         string               `json:"environment"`
	Revision            int64                `json:"revision"`
	Spec                managedplatform.Spec `json:"spec"`
	Status              string               `json:"status"`
	Observation         map[string]any       `json:"observation"`
	ReservedCPUMilli    int64                `json:"reserved_cpu_milli"`
	ReservedMemoryBytes int64                `json:"reserved_memory_bytes"`
	ReservedStorageGiB  int64                `json:"reserved_storage_gib"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	DeletedAt           *time.Time           `json:"deleted_at,omitempty"`
}

type ManagedPlatformOperation struct {
	ID                   string                `json:"id"`
	PlatformID           string                `json:"platform_id"`
	Revision             int64                 `json:"revision"`
	Kind                 string                `json:"kind"`
	Status               string                `json:"status"`
	Phase                string                `json:"phase"`
	Message              string                `json:"message"`
	Spec                 managedplatform.Spec  `json:"spec"`
	Plan                 managedplatform.Plan  `json:"plan"`
	EncryptedSnapshot    []byte                `json:"-"`
	Review               ManagedPlatformReview `json:"review"`
	ReviewID             string                `json:"review_id"`
	AuthorityFingerprint []byte                `json:"-"`
	CreatedAt            time.Time             `json:"created_at"`
	StartedAt            *time.Time            `json:"started_at,omitempty"`
	FinishedAt           *time.Time            `json:"finished_at,omitempty"`
	IdentityID           string                `json:"-"`
	KeyID                string                `json:"-"`
	Lease                string                `json:"-"`
	LeaseUntil           *time.Time            `json:"-"`
	Attempt              int                   `json:"attempt"`
	Maintenance          bool                  `json:"-"`
	MaintenanceID        string                `json:"-"`
}

type ManagedPlatformReview struct {
	ID                   string    `json:"id"`
	ExpectedRevision     int64     `json:"expected_revision"`
	Kind                 string    `json:"kind"`
	RequestHash          string    `json:"request_hash"`
	AuthorityFingerprint string    `json:"authority_fingerprint"`
	CapacityFingerprint  string    `json:"capacity_fingerprint,omitempty"`
	ExpiresAt            time.Time `json:"expires_at"`
	BlockedReasons       []string  `json:"blocked_reasons"`
}

type PlatformResourceClaim struct {
	PlatformID          string
	PlatformRevision    int64
	Component           string
	Kind                string
	ResourceID          string
	ImmutableGeneration int64
	OwnerOperationID    string
	ReleasedAt          *time.Time
}

// PlatformResourceIntent is a durable reservation to create one external
// identity. A pending intent is not ownership; only confirmation creates a
// PlatformResourceClaim from provider-returned identity.
type PlatformResourceIntent struct {
	ID               string
	PlatformID       string
	PlatformRevision int64
	Component        string
	Kind             string
	ExternalKey      string
	OwnerOperationID string
	CreatedAt        time.Time
	ConfirmedAt      *time.Time
	ReleasedAt       *time.Time
}

const managedPlatformColumns = `id,project,environment,revision,desired_spec,status,observation,reserved_cpu_milli,reserved_memory_bytes,reserved_storage_gib,created_at,updated_at,deleted_at`
const managedPlatformOperationColumns = `id,platform_id,revision,kind,status,phase,message,desired_spec,resolved_plan,encrypted_snapshot,review,review_id,authority_fingerprint,created_at,started_at,finished_at,identity_id,key_id,lease,lease_until,attempt`

func scanManagedPlatform(row scanner) (ManagedPlatform, error) {
	var item ManagedPlatform
	err := row.Scan(&item.ID, &item.Project, &item.Environment, &item.Revision, &item.Spec, &item.Status, &item.Observation, &item.ReservedCPUMilli, &item.ReservedMemoryBytes, &item.ReservedStorageGiB, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt)
	return item, err
}

func scanManagedPlatformOperation(row scanner) (ManagedPlatformOperation, error) {
	var item ManagedPlatformOperation
	err := row.Scan(&item.ID, &item.PlatformID, &item.Revision, &item.Kind, &item.Status, &item.Phase, &item.Message, &item.Spec, &item.Plan, &item.EncryptedSnapshot, &item.Review, &item.ReviewID, &item.AuthorityFingerprint, &item.CreatedAt, &item.StartedAt, &item.FinishedAt, &item.IdentityID, &item.KeyID, &item.Lease, &item.LeaseUntil, &item.Attempt)
	return item, err
}

func redactManagedPlatformOperation(op ManagedPlatformOperation) ManagedPlatformOperation {
	op.EncryptedSnapshot = nil
	op.AuthorityFingerprint = nil
	op.Review.AuthorityFingerprint = ""
	return op
}

func (p Principal) AllowsManagedPlatform(project, environment string, write bool) bool {
	permission := "deployments:read"
	if write {
		permission = "deployments:write"
	}
	return p.Application == "" && p.Allows(permission, project, environment, "")
}

func managedPlatformRequestHash(item ManagedPlatform, plan managedplatform.Plan, expected int64, kind string) [32]byte {
	return sha256.Sum256(JSON(struct {
		Project, Environment, ID, Kind string
		Expected                       int64
		Spec                           managedplatform.Spec
		Plan                           managedplatform.Plan
	}{item.Project, item.Environment, item.ID, kind, expected, item.Spec, plan}))
}

func sameManagedPlatformRuntimePlan(a, b managedplatform.Plan) bool {
	a.Capability = managedplatform.Capability{}
	b.Capability = managedplatform.Capability{}
	return bytes.Equal(JSON(a), JSON(b))
}

func validateManagedPlatformIntentShape(item ManagedPlatform, plan managedplatform.Plan, expected int64, kind string) error {
	if err := item.Spec.Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrInput, err)
	}
	if !managedPlatformID.MatchString(item.ID) || expected < 0 {
		return fmt.Errorf("%w: managed platform id or expected revision is invalid", ErrInput)
	}
	if kind != "create" && kind != "update" && kind != "delete" {
		return fmt.Errorf("%w: managed platform operation kind is invalid", ErrInput)
	}
	if len(plan.Components) < 1 || len(plan.Components) > managedplatform.MaxComponents || plan.Namespace == "" {
		return fmt.Errorf("%w: managed platform resolved plan shape is invalid", ErrInput)
	}
	if len(JSON(item.Spec)) > maxManagedPlatformPayloadBytes || len(JSON(plan)) > maxManagedPlatformPayloadBytes {
		return fmt.Errorf("%w: managed platform desired state exceeds the payload bound", ErrInput)
	}
	return nil
}

func validateManagedPlatformIntent(item ManagedPlatform, plan managedplatform.Plan, expected int64, kind string) error {
	if err := validateManagedPlatformIntentShape(item, plan, expected, kind); err != nil {
		return err
	}
	if kind != "delete" && (!plan.Capability.Available || !plan.Capability.ClusterQualified) {
		return fmt.Errorf("%w: managed platform runtime is not qualified", ErrForbidden)
	}
	return nil
}

func (s *Store) ManagedPlatforms(ctx context.Context, p Principal, project, environment string) ([]ManagedPlatform, error) {
	if (project == "") != (environment == "") {
		return nil, ErrInput
	}
	if p.MFARequired || p.Application != "" || (!contains(p.Permissions, "deployments:read") && !contains(p.Permissions, "admin")) {
		return nil, ErrForbidden
	}
	if project != "" && !p.AllowsManagedPlatform(project, environment, false) {
		return nil, ErrForbidden
	}
	args := []any{}
	where := []string{"deleted_at IS NULL"}
	bind := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	eq := func(column, value string) {
		if value != "" {
			where = append(where, column+"="+bind(value))
		}
	}
	// Apply the same identity and bearer-key constraints as Allows before the
	// result limit, so unreadable rows cannot crowd out accessible platforms.
	if !p.Admin {
		if p.Email != "" {
			projects := []string{}
			for _, role := range p.ProjectRoles {
				if contains(role.permissions(), "deployments:read") {
					projects = append(projects, role.Project)
				}
			}
			where = append(where, "project=ANY("+bind(projects)+"::text[])")
		} else if !contains(p.IdentityPermissions, "deployments:read") {
			return nil, ErrForbidden
		}
	}
	eq("project", p.Project)
	eq("environment", p.Environment)
	eq("project", p.IdentityProject)
	eq("environment", p.IdentityEnvironment)
	eq("project", project)
	eq("environment", environment)
	rows, err := s.Pool.Query(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE "+strings.Join(where, " AND ")+" ORDER BY project,environment,name,id LIMIT "+bind(MaxManagedPlatforms+1), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedPlatform, 0)
	for rows.Next() {
		item, scanErr := scanManagedPlatform(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if len(result) > MaxManagedPlatforms {
		return nil, ErrConflict
	}
	return result, rows.Err()
}

func (s *Store) ManagedPlatform(ctx context.Context, p Principal, id string, write bool) (ManagedPlatform, error) {
	item, err := scanManagedPlatform(s.Pool.QueryRow(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE id=$1 AND deleted_at IS NULL", id))
	if err != nil {
		return item, err
	}
	if !p.AllowsManagedPlatform(item.Project, item.Environment, write) {
		return ManagedPlatform{}, pgx.ErrNoRows
	}
	return item, nil
}

// SaveManagedPlatformReview persists the exact server-resolved intent and the
// current authority that produced it. A caller cannot manufacture or alter a
// review and later consume it through AcceptManagedPlatform.
func (s *Store) SaveManagedPlatformReview(ctx context.Context, p Principal, item ManagedPlatform, plan managedplatform.Plan, expected int64, kind string) (ManagedPlatformReview, error) {
	var review ManagedPlatformReview
	if err := validateManagedPlatformIntent(item, plan, expected, kind); err != nil {
		return review, err
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return review, err
	}
	defer tx.Rollback(ctx)
	authority, fingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, p.KeyID)
	if err != nil {
		return review, err
	}
	if authority.Principal.ID != p.ID || !authority.Principal.AllowsManagedPlatform(item.Project, item.Environment, true) {
		return review, ErrForbidden
	}
	var environment string
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR SHARE", item.Project, item.Environment).Scan(&environment); err != nil {
		return review, err
	}
	current, currentErr := scanManagedPlatform(tx.QueryRow(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE project=$1 AND environment=$2 AND name=$3 AND deleted_at IS NULL FOR SHARE", item.Project, item.Environment, item.Spec.Name))
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return review, currentErr
	}
	if current.Revision != expected {
		return review, ErrConflict
	}
	if expected == 0 {
		if kind != "create" || current.ID != "" {
			return review, ErrConflict
		}
	} else {
		if current.ID != item.ID || kind == "create" || current.Status == "pending" || current.Status == "deleting" {
			return review, ErrConflict
		}
		if current.Spec.Kind != item.Spec.Kind {
			return review, ErrInput
		}
		if kind == "delete" && !bytes.Equal(JSON(current.Spec), JSON(item.Spec)) {
			return review, fmt.Errorf("%w: delete must preserve the current desired specification", ErrConflict)
		}
		if kind == "delete" {
			var currentPlan managedplatform.Plan
			if err = tx.QueryRow(ctx, "SELECT resolved_plan FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2", current.ID, current.Revision).Scan(&currentPlan); err != nil {
				return review, err
			}
			if !sameManagedPlatformRuntimePlan(currentPlan, plan) {
				return review, fmt.Errorf("%w: delete must preserve the current resolved plan", ErrConflict)
			}
		}
	}
	capacityFingerprint := ""
	if kind != "delete" && (s.RequireManagedPlatformAdmission || s.ManagedPlatformCapacityBudget != nil) {
		policy, fingerprint, capacityErr := s.managedPlatformCapacityPolicyTx(ctx, tx, item.Project, item.Environment)
		if capacityErr != nil {
			return review, capacityErr
		}
		if capacityErr = policy.Allows(item.Spec, plan); capacityErr != nil {
			return review, capacityErr
		}
		capacityFingerprint = capacityFingerprintHex(fingerprint)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM managed_platform_reviews WHERE identity_id=$1 AND platform_id=$2 AND consumed_at IS NULL AND expires_at<=clock_timestamp()", p.ID, item.ID); err != nil {
		return review, err
	}
	var pending int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_platform_reviews WHERE identity_id=$1 AND platform_id=$2 AND consumed_at IS NULL AND expires_at>clock_timestamp()", p.ID, item.ID).Scan(&pending); err != nil {
		return review, err
	}
	if pending >= MaxManagedPlatformReviews {
		return review, fmt.Errorf("%w: wait for an existing managed platform review to expire", ErrConflict)
	}
	hash := managedPlatformRequestHash(item, plan, expected, kind)
	review = ManagedPlatformReview{ID: NewID(), ExpectedRevision: expected, Kind: kind, RequestHash: hex.EncodeToString(hash[:]), AuthorityFingerprint: hex.EncodeToString(fingerprint), CapacityFingerprint: capacityFingerprint, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), BlockedReasons: []string{}}
	if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_reviews(id,identity_id,key_id,project,environment,platform_id,platform_name,kind,expected_revision,request_hash,authority_fingerprint,desired_spec,resolved_plan,payload,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, review.ID, p.ID, p.KeyID, item.Project, item.Environment, item.ID, item.Spec.Name, kind, expected, hash[:], fingerprint, JSON(item.Spec), JSON(plan), JSON(review), review.ExpiresAt); err != nil {
		return ManagedPlatformReview{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'managed_platform.review',$3,$4)", p.ID, p.KeyID, item.ID, JSON(map[string]any{"kind": kind, "expected_revision": expected, "review_id": review.ID})); err != nil {
		return ManagedPlatformReview{}, err
	}
	return review, tx.Commit(ctx)
}

// ValidateManagedPlatformReview verifies that a persisted review still
// describes the exact requested operation and was issued under the caller's
// current authority. It does not consume the review or mutate the platform.
// AcceptManagedPlatform repeats these checks in its mutation transaction.
func (s *Store) ValidateManagedPlatformReview(ctx context.Context, p Principal, item ManagedPlatform, plan managedplatform.Plan, review ManagedPlatformReview, expected int64, kind string) error {
	if err := validateManagedPlatformIntent(item, plan, expected, kind); err != nil {
		return err
	}
	if !managedPlatformID.MatchString(review.ID) {
		return fmt.Errorf("%w: managed platform review id is invalid", ErrInput)
	}
	hash := managedPlatformRequestHash(item, plan, expected, kind)
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	authority, currentFingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, p.KeyID)
	if err != nil {
		return err
	}
	if authority.Principal.ID != p.ID || !authority.Principal.AllowsManagedPlatform(item.Project, item.Environment, true) {
		return ErrForbidden
	}

	var stored ManagedPlatformReview
	var storedHash, storedFingerprint []byte
	var storedSpec managedplatform.Spec
	var storedPlan managedplatform.Plan
	err = tx.QueryRow(ctx, `SELECT payload,request_hash,authority_fingerprint,desired_spec,resolved_plan
		FROM managed_platform_reviews
		WHERE id=$1 AND identity_id=$2 AND key_id=$3 AND project=$4 AND environment=$5 AND platform_id=$6 AND platform_name=$7 AND kind=$8 AND expected_revision=$9 AND consumed_at IS NULL AND expires_at>clock_timestamp()
		FOR SHARE`, review.ID, p.ID, p.KeyID, item.Project, item.Environment, item.ID, item.Spec.Name, kind, expected).Scan(&stored, &storedHash, &storedFingerprint, &storedSpec, &storedPlan)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(storedHash, hash[:]) || !bytes.Equal(storedFingerprint, currentFingerprint) || !bytes.Equal(JSON(stored), JSON(review)) || !bytes.Equal(JSON(storedSpec), JSON(item.Spec)) || !bytes.Equal(JSON(storedPlan), JSON(plan)) || len(stored.BlockedReasons) != 0 {
		return ErrConflict
	}

	var environment string
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR SHARE", item.Project, item.Environment).Scan(&environment); err != nil {
		return err
	}
	current, currentErr := scanManagedPlatform(tx.QueryRow(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE project=$1 AND environment=$2 AND name=$3 AND deleted_at IS NULL FOR SHARE", item.Project, item.Environment, item.Spec.Name))
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return currentErr
	}
	if current.Revision != expected {
		return ErrConflict
	}
	if kind != "delete" && (s.RequireManagedPlatformAdmission || s.ManagedPlatformCapacityBudget != nil) {
		policy, fingerprint, capacityErr := s.managedPlatformCapacityPolicyTx(ctx, tx, item.Project, item.Environment)
		if capacityErr != nil {
			return capacityErr
		}
		if capacityErr = policy.Allows(item.Spec, plan); capacityErr != nil || review.CapacityFingerprint != capacityFingerprintHex(fingerprint) {
			return ErrConflict
		}
	}
	if expected == 0 {
		if kind != "create" || current.ID != "" {
			return ErrConflict
		}
	} else {
		if current.ID != item.ID || kind == "create" || current.Status == "pending" || current.Status == "deleting" || current.Spec.Kind != item.Spec.Kind {
			return ErrConflict
		}
		if kind == "delete" {
			if !bytes.Equal(JSON(current.Spec), JSON(item.Spec)) {
				return ErrConflict
			}
			var currentPlan managedplatform.Plan
			if err = tx.QueryRow(ctx, "SELECT resolved_plan FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2", current.ID, current.Revision).Scan(&currentPlan); err != nil {
				return err
			}
			if !sameManagedPlatformRuntimePlan(currentPlan, plan) {
				return ErrConflict
			}
		}
	}
	return tx.Commit(ctx)
}

// ManagedPlatformOperationReplay returns a previously accepted operation for
// the same principal, key, idempotency key, and immutable semantic request. It
// deliberately does not require a still-live review or current resource row.
func (s *Store) ManagedPlatformOperationReplay(ctx context.Context, p Principal, item ManagedPlatform, plan managedplatform.Plan, expected int64, idempotencyKey, kind string) (ManagedPlatformOperation, error) {
	var empty ManagedPlatformOperation
	if err := validateManagedPlatformIntentShape(item, plan, expected, kind); err != nil {
		return empty, err
	}
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return empty, ErrInput
	}
	hash := managedPlatformRequestHash(item, plan, expected, kind)
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	authority, _, err := s.managedPlatformAuthorityTx(ctx, tx, p.KeyID)
	if err != nil {
		return empty, err
	}
	if authority.Principal.ID != p.ID || !authority.Principal.AllowsManagedPlatform(item.Project, item.Environment, true) {
		return empty, ErrForbidden
	}
	var oldID, oldKeyID string
	var oldHash []byte
	err = tx.QueryRow(ctx, "SELECT id,key_id,request_hash FROM managed_platform_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idempotencyKey).Scan(&oldID, &oldKeyID, &oldHash)
	if err != nil {
		return empty, err
	}
	if oldKeyID != p.KeyID || !bytes.Equal(oldHash, hash[:]) {
		return empty, ErrConflict
	}
	op, err := scanManagedPlatformOperation(tx.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE id=$1", oldID))
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}
	return redactManagedPlatformOperation(op), nil
}

func (s *Store) AcceptManagedPlatform(ctx context.Context, p Principal, item ManagedPlatform, plan managedplatform.Plan, encryptedSnapshot []byte, review ManagedPlatformReview, expected int64, idempotencyKey, kind string) (ManagedPlatformOperation, error) {
	var empty ManagedPlatformOperation
	if err := validateManagedPlatformIntentShape(item, plan, expected, kind); err != nil {
		return empty, err
	}
	if !managedPlatformID.MatchString(review.ID) || len(encryptedSnapshot) < 1 || len(encryptedSnapshot) > managedplatform.MaxManagedPlatformEncryptedSnapshotBytes || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return empty, ErrInput
	}
	hash := managedPlatformRequestHash(item, plan, expected, kind)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044269)"); err != nil {
		return empty, err
	}
	if err = lockManagedPlatformMutations(ctx, tx, item.ID); err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,69))", p.ID+":"+idempotencyKey); err != nil {
		return empty, err
	}
	authority, currentFingerprint, err := s.managedPlatformAuthorityTx(ctx, tx, p.KeyID)
	if err != nil {
		return empty, err
	}
	if authority.Principal.ID != p.ID || !authority.Principal.AllowsManagedPlatform(item.Project, item.Environment, true) {
		return empty, ErrForbidden
	}
	var oldID, oldKeyID string
	var oldHash []byte
	err = tx.QueryRow(ctx, "SELECT id,key_id,request_hash FROM managed_platform_operations WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idempotencyKey).Scan(&oldID, &oldKeyID, &oldHash)
	if err == nil {
		if oldKeyID != p.KeyID || !bytes.Equal(oldHash, hash[:]) {
			return empty, ErrConflict
		}
		op, scanErr := scanManagedPlatformOperation(tx.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE id=$1", oldID))
		if scanErr != nil {
			return empty, scanErr
		}
		return redactManagedPlatformOperation(op), tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if err = rejectPlatformRuntimeOverlapTx(ctx, tx, []string{item.ID}); err != nil {
		return empty, err
	}
	if err = validateManagedPlatformIntent(item, plan, expected, kind); err != nil {
		return empty, err
	}
	if kind != "delete" {
		if s.RequireManagedPlatformAdmission && s.AdmitManagedPlatform == nil {
			return empty, fmt.Errorf("%w: managed platform admission must be configured", ErrForbidden)
		}
		if s.AdmitManagedPlatform != nil {
			if err = s.AdmitManagedPlatform(ctx, tx, p, item.Project, item.Environment, idempotencyKey); err != nil {
				return empty, err
			}
		}
	}

	var stored ManagedPlatformReview
	var storedHash, storedFingerprint []byte
	var storedSpec managedplatform.Spec
	var storedPlan managedplatform.Plan
	err = tx.QueryRow(ctx, `SELECT payload,request_hash,authority_fingerprint,desired_spec,resolved_plan
		FROM managed_platform_reviews
		WHERE id=$1 AND identity_id=$2 AND key_id=$3 AND project=$4 AND environment=$5 AND platform_id=$6 AND platform_name=$7 AND kind=$8 AND expected_revision=$9 AND consumed_at IS NULL AND expires_at>clock_timestamp()
		FOR UPDATE`, review.ID, p.ID, p.KeyID, item.Project, item.Environment, item.ID, item.Spec.Name, kind, expected).Scan(&stored, &storedHash, &storedFingerprint, &storedSpec, &storedPlan)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, ErrConflict
	}
	if err != nil {
		return empty, err
	}
	if !bytes.Equal(storedHash, hash[:]) || !bytes.Equal(storedFingerprint, currentFingerprint) || !bytes.Equal(JSON(stored), JSON(review)) || !bytes.Equal(JSON(storedSpec), JSON(item.Spec)) || !bytes.Equal(JSON(storedPlan), JSON(plan)) || len(stored.BlockedReasons) != 0 {
		return empty, ErrConflict
	}

	var environment string
	if err = tx.QueryRow(ctx, "SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE", item.Project, item.Environment).Scan(&environment); err != nil {
		return empty, err
	}
	current, currentErr := scanManagedPlatform(tx.QueryRow(ctx, "SELECT "+managedPlatformColumns+" FROM managed_platforms WHERE project=$1 AND environment=$2 AND name=$3 AND deleted_at IS NULL FOR UPDATE", item.Project, item.Environment, item.Spec.Name))
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return empty, currentErr
	}
	if current.Revision != expected {
		return empty, ErrConflict
	}
	var activeRecovery bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_platform_recovery_operations WHERE (source_platform_id=$1 OR target_platform_id=$1) AND status IN ('queued','running'))", item.ID).Scan(&activeRecovery); err != nil {
		return empty, err
	}
	if activeRecovery {
		return empty, ErrConflict
	}
	if expected == 0 {
		if kind != "create" || current.ID != "" {
			return empty, ErrConflict
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM managed_platforms WHERE project=$1 AND environment=$2 AND deleted_at IS NULL", item.Project, item.Environment).Scan(&count); err != nil {
			return empty, err
		}
		if count >= MaxManagedPlatforms {
			return empty, ErrInput
		}
		item.Revision = 1
		_, err = tx.Exec(ctx, "INSERT INTO managed_platforms(id,project,environment,name,kind,revision,desired_spec) VALUES($1,$2,$3,$4,$5,1,$6)", item.ID, item.Project, item.Environment, item.Spec.Name, item.Spec.Kind, JSON(item.Spec))
	} else {
		if current.ID != item.ID || kind == "create" || current.Status == "pending" || current.Status == "deleting" || current.Spec.Kind != item.Spec.Kind {
			return empty, ErrConflict
		}
		if kind == "delete" {
			if !bytes.Equal(JSON(current.Spec), JSON(item.Spec)) {
				return empty, ErrConflict
			}
			var currentPlan managedplatform.Plan
			if err = tx.QueryRow(ctx, "SELECT resolved_plan FROM managed_platform_operations WHERE platform_id=$1 AND revision=$2", current.ID, current.Revision).Scan(&currentPlan); err != nil {
				return empty, err
			}
			if !sameManagedPlatformRuntimePlan(currentPlan, plan) {
				return empty, ErrConflict
			}
			item.Spec = current.Spec
		}
		status := "pending"
		if kind == "delete" {
			status = "deleting"
		}
		item.Revision = expected + 1
		tag, updateErr := tx.Exec(ctx, "UPDATE managed_platforms SET revision=$2,desired_spec=$3,status=$4,updated_at=now() WHERE id=$1 AND revision=$5 AND deleted_at IS NULL", item.ID, item.Revision, JSON(item.Spec), status, expected)
		err = updateErr
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrConflict
		}
	}
	if err != nil {
		return empty, err
	}
	if kind != "delete" && (s.RequireManagedPlatformAdmission || s.ManagedPlatformCapacityBudget != nil) {
		policy, capacityFingerprint, capacityErr := s.managedPlatformCapacityPolicyTx(ctx, tx, item.Project, item.Environment)
		if capacityErr != nil {
			return empty, capacityErr
		}
		if review.CapacityFingerprint != capacityFingerprintHex(capacityFingerprint) {
			return empty, ErrConflict
		}
		if capacityErr = s.reserveManagedPlatformCapacity(ctx, tx, item, plan, policy, kind); capacityErr != nil {
			return empty, capacityErr
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE managed_platform_reviews SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL", review.ID); err != nil {
		return empty, err
	}
	op, err := scanManagedPlatformOperation(tx.QueryRow(ctx, `INSERT INTO managed_platform_operations(id,platform_id,revision,identity_id,key_id,idempotency_key,request_hash,authority_fingerprint,review_id,kind,desired_spec,resolved_plan,encrypted_snapshot,review)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+managedPlatformOperationColumns, NewID(), item.ID, item.Revision, p.ID, p.KeyID, idempotencyKey, hash[:], currentFingerprint, review.ID, kind, JSON(item.Spec), JSON(plan), encryptedSnapshot, JSON(review)))
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, "managed_platform."+kind, item.ID, JSON(map[string]any{"revision": item.Revision, "operation_id": op.ID, "review_id": review.ID})); err != nil {
		return empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}
	return redactManagedPlatformOperation(op), nil
}

func (s *Store) ManagedPlatformOperations(ctx context.Context, p Principal, platformID string) ([]ManagedPlatformOperation, error) {
	var project, environment string
	if err := s.Pool.QueryRow(ctx, "SELECT project,environment FROM managed_platforms WHERE id=$1", platformID).Scan(&project, &environment); err != nil {
		return nil, err
	}
	if !p.AllowsManagedPlatform(project, environment, false) {
		return nil, pgx.ErrNoRows
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE platform_id=$1 ORDER BY created_at DESC,id LIMIT $2", platformID, MaxManagedPlatforms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedPlatformOperation, 0)
	for rows.Next() {
		op, scanErr := scanManagedPlatformOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, redactManagedPlatformOperation(op))
	}
	return result, rows.Err()
}

func (s *Store) ManagedPlatformOperation(ctx context.Context, p Principal, id string) (ManagedPlatformOperation, error) {
	op, err := scanManagedPlatformOperation(s.Pool.QueryRow(ctx, "SELECT "+managedPlatformOperationColumns+" FROM managed_platform_operations WHERE id=$1", id))
	if err != nil {
		return op, err
	}
	var project, environment string
	if err = s.Pool.QueryRow(ctx, "SELECT project,environment FROM managed_platforms WHERE id=$1", op.PlatformID).Scan(&project, &environment); err != nil {
		return ManagedPlatformOperation{}, err
	}
	if !p.AllowsManagedPlatform(project, environment, false) {
		return ManagedPlatformOperation{}, pgx.ErrNoRows
	}
	return redactManagedPlatformOperation(op), nil
}
