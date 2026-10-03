package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
)

func neonUnprovisionedDeleteFixture(t *testing.T, seed func(*Store, ManagedPlatformOperation)) (*Store, Principal, ManagedPlatformOperation) {
	t.Helper()
	s, principal, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	review := managedPlatformReview(t, s, principal, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, principal, item, plan, []byte("sealed"), review, 0, "empty-state-create", "create"); err != nil {
		t.Fatal(err)
	}
	create, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(s, create)
	}
	if err = s.RecordManagedPlatformStep(ctx, create, "failed", "provider-unavailable", "", nil); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagedPlatform(ctx, principal, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	review = managedPlatformReview(t, s, principal, current, plan, current.Revision, "delete")
	if _, err = s.AcceptManagedPlatform(ctx, principal, current, plan, []byte("sealed-delete"), review, current.Revision, "empty-state-delete", "delete"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, principal, op
}

func TestNeonProviderStateEmptyPreservesKubernetesCleanup(t *testing.T) {
	ctx := context.Background()
	s, _, op := neonUnprovisionedDeleteFixture(t, func(s *Store, create ManagedPlatformOperation) {
		claim := PlatformResourceClaim{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: "namespace.managed-platform-" + create.PlatformID, Kind: "runtime_component", ResourceID: "namespace-uid", ImmutableGeneration: 1, OwnerOperationID: create.ID}
		if err := s.ClaimPlatformResource(ctx, create, claim); err != nil {
			t.Fatal(err)
		}
		intent := PlatformResourceIntent{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: "service.neon-proxy", Kind: "runtime_component", ExternalKey: "service/managed-platform-" + create.PlatformID + "/neon-proxy", OwnerOperationID: create.ID}
		if _, err := s.ReservePlatformResourceIntent(ctx, create, intent); err != nil {
			t.Fatal(err)
		}
	})
	empty, err := s.NeonProviderStateEmpty(ctx, op)
	if err != nil || !empty {
		t.Fatalf("Kubernetes-only state required an unavailable provider: empty=%v error=%v", empty, err)
	}
	claims, err := s.PlatformResourceClaims(ctx, op, op.Revision-1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("absence proof changed Kubernetes ownership: %v %v", claims, err)
	}
	intents, err := s.PlatformResourceIntents(ctx, op, op.Revision-1)
	if err != nil || len(intents) != 1 || intents[0].ConfirmedAt != nil {
		t.Fatalf("absence proof changed Kubernetes creation intent: %v %v", intents, err)
	}
}

func TestNeonProviderStateEmptyIncludesOlderNativeClaimsAndIntents(t *testing.T) {
	for _, kind := range []struct {
		component string
		kind      string
	}{
		{component: "tenant", kind: "neon_tenant"},
		{component: "timeline", kind: "neon_timeline"},
		{component: "compute-compute-0", kind: "runtime_component"},
		{component: "pageserver-registration-0", kind: "runtime_component"},
		{component: "safekeeper-registration-0", kind: "runtime_component"},
	} {
		for _, confirmed := range []bool{false, true} {
			name := kind.component + "/pending"
			if confirmed {
				name = kind.component + "/claimed"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				s, principal, op := neonUnprovisionedDeleteFixture(t, func(s *Store, create ManagedPlatformOperation) {
					intent, err := s.ReservePlatformResourceIntent(ctx, create, PlatformResourceIntent{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: kind.component, Kind: kind.kind, ExternalKey: "provider-resource", OwnerOperationID: create.ID})
					if err != nil {
						t.Fatal(err)
					}
					if confirmed {
						claim := PlatformResourceClaim{PlatformID: create.PlatformID, PlatformRevision: create.Revision, Component: kind.component, Kind: kind.kind, ResourceID: "provider-resource", ImmutableGeneration: 1, OwnerOperationID: create.ID}
						if err = s.ConfirmPlatformResourceIntent(ctx, create, intent, claim); err != nil {
							t.Fatal(err)
						}
					}
				})
				if err := s.RecordManagedPlatformStep(ctx, op, "failed", "provider-unavailable", "", nil); err != nil {
					t.Fatal(err)
				}
				current, err := s.ManagedPlatform(ctx, principal, op.PlatformID, true)
				if err != nil {
					t.Fatal(err)
				}
				review := managedPlatformReview(t, s, principal, current, op.Plan, current.Revision, "delete")
				if _, err = s.AcceptManagedPlatform(ctx, principal, current, op.Plan, []byte("sealed-retry"), review, current.Revision, "empty-state-delete-retry", "delete"); err != nil {
					t.Fatal(err)
				}
				op, err = s.ClaimManagedPlatformOperation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if op.Revision != 3 {
					t.Fatalf("fixture did not retain older state: revision=%d", op.Revision)
				}
				intents, err := s.PlatformResourceIntents(ctx, op, op.Revision-1)
				if err != nil || len(intents) != 0 {
					t.Fatalf("fixture unexpectedly exposed old intents through the prior revision: %v %v", intents, err)
				}
				if empty, err := s.NeonProviderStateEmpty(ctx, op); err != nil || empty {
					t.Fatalf("older native state was ignored: empty=%v error=%v", empty, err)
				}
			})
		}
	}
}

func TestNeonProviderStateEmptyRequiresCurrentDeleteAuthority(t *testing.T) {
	s, principal, op := neonUnprovisionedDeleteFixture(t, nil)
	ctx := context.Background()
	if empty, err := s.NeonProviderStateEmpty(ctx, op); err != nil || !empty {
		t.Fatalf("empty fixture did not produce a fenced proof: %v %v", empty, err)
	}
	stale := op
	stale.Lease = NewID()
	if empty, err := s.NeonProviderStateEmpty(ctx, stale); empty || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete lease produced absence proof: %v %v", empty, err)
	}
	create := op
	create.Kind = "create"
	if empty, err := s.NeonProviderStateEmpty(ctx, create); empty || !errors.Is(err, ErrInput) {
		t.Fatalf("non-delete operation produced absence proof: %v %v", empty, err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE api_keys SET permissions=ARRAY['deployments:read'] WHERE id=$1", principal.KeyID); err != nil {
		t.Fatal(err)
	}
	if empty, err := s.NeonProviderStateEmpty(ctx, op); empty || !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked authority produced absence proof: %v %v", empty, err)
	}
}

func TestNeonProviderStateEmptyRefusesUnknownKubernetesKeys(t *testing.T) {
	s, _, op := neonUnprovisionedDeleteFixture(t, nil)
	ctx := context.Background()
	for _, component := range []string{"namespace", "service", "namespace.", "service..", "service.UPPER", "other.resource", "service.-bad", "service.bad-"} {
		t.Run(component, func(t *testing.T) {
			// These malformed development fixtures bypass the writer so the
			// absence check must independently refuse ambiguous stored keys.
			if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_component_resources(platform_id,platform_revision,component,resource_kind,resource_id,immutable_generation,owner_operation_id) VALUES($1,$2,$3,'runtime_component',$3,1,$4)`, op.PlatformID, op.Revision, component, op.ID); err != nil {
				t.Fatal(err)
			}
			if empty, err := s.NeonProviderStateEmpty(ctx, op); err != nil || empty {
				t.Fatalf("malformed claim was treated as Kubernetes-only: %v %v", empty, err)
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE platform_component_resources SET released_at=now() WHERE platform_id=$1 AND component=$2`, op.PlatformID, component); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_resource_intents(id,platform_id,platform_revision,component,resource_kind,external_key,owner_operation_id) VALUES($1,$2,$3,$4,'runtime_component',$4,$5)`, NewID(), op.PlatformID, op.Revision, component, op.ID); err != nil {
				t.Fatal(err)
			}
			if empty, err := s.NeonProviderStateEmpty(ctx, op); err != nil || empty {
				t.Fatalf("malformed intent was treated as Kubernetes-only: %v %v", empty, err)
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE platform_resource_intents SET released_at=now() WHERE platform_id=$1 AND component=$2`, op.PlatformID, component); err != nil {
				t.Fatal(err)
			}
			if empty, err := s.NeonProviderStateEmpty(ctx, op); err != nil || !empty {
				t.Fatalf("released fixture still blocked absence proof: %v %v", empty, err)
			}
		})
	}
}

func TestNeonProviderStateEmptyRefusesAmbiguousRecoveryReplacements(t *testing.T) {
	s, principal, op := neonUnprovisionedDeleteFixture(t, nil)
	ctx := context.Background()
	item, err := s.ManagedPlatform(ctx, principal, op.PlatformID, true)
	if err != nil {
		t.Fatal(err)
	}
	principal.Project, principal.Environment = item.Project, item.Environment
	destination, err := s.PutBackupDestination(ctx, principal, backup.Destination{ID: NewID(), Name: "absence-proof-fixture", Endpoint: "https://storage.example.test", Region: "test", Bucket: "recovery", Prefix: "neon", EncryptionRecipient: "age1fixture", EncryptedCredentials: []byte("sealed")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	artifact, review, recovery := NewID(), NewID(), NewID()
	digest := strings.Repeat("a", 64)
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_artifacts(id,source_platform_id,source_revision,destination_id,object_key,encrypted_bytes,encrypted_sha256,manifest,manifest_sha256) VALUES($1,$2,$3,$4,'neon/fixture',1,$5,'{}',$5)`, artifact, op.PlatformID, op.Revision, destination.ID, digest); err != nil {
		t.Fatal(err)
	}
	// Development fixtures model inconsistent recovery completion records. They
	// deliberately bypass the recovery writer while retaining real foreign keys.
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_reviews(id,identity_id,key_id,project,environment,kind,source_platform_id,destination_id,destination_revision,expected_source_revision,request_hash,authority_fingerprint,reviewed_intent,expires_at)
		SELECT $1,o.identity_id,o.key_id,p.project,p.environment,'backup',o.platform_id,$2,1,o.revision,o.request_hash,o.authority_fingerprint,'{}',now()+interval '5 minutes'
		FROM managed_platform_operations o JOIN managed_platforms p ON p.id=o.platform_id WHERE o.id=$3`, review, destination.ID, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO managed_platform_recovery_operations(id,kind,identity_id,key_id,project,environment,review_id,idempotency_key,request_hash,authority_fingerprint,source_platform_id,destination_id,destination_revision,expected_source_revision,status)
		SELECT $1,kind,identity_id,key_id,project,environment,id,'absence-proof-recovery',request_hash,authority_fingerprint,source_platform_id,destination_id,destination_revision,expected_source_revision,'failed'
		FROM managed_platform_recovery_reviews WHERE id=$2`, recovery, review); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		phase       string
		replacement bool
		released    bool
		wantEmpty   bool
	}{
		{phase: "reserved"},
		{phase: "confirmed", replacement: true},
		{phase: "complete", replacement: true},
		{phase: "adopted", replacement: true},
		{phase: "complete", replacement: true, released: true, wantEmpty: true},
	} {
		name := test.phase
		if test.released {
			name += "/released"
		}
		t.Run(name, func(t *testing.T) {
			var replacement any
			var generation any
			if test.replacement {
				replacement, generation = "replacement-resource", int64(2)
			}
			if _, err := s.Pool.Exec(ctx, `INSERT INTO platform_component_recovery_overrides(platform_id,platform_revision,component,resource_kind,recovery_operation_id,artifact_id,manifest_sha256,prior_resource_id,prior_generation,prior_owner_operation_id,replacement_external_key,transition_token,phase,replacement_resource_id,replacement_generation,replacement_released_at)
				VALUES($1,$2,'compute-compute-0','runtime_component',$3,$4,$5,'prior-resource',1,$6,'replacement-key',$5,$7,$8,$9,CASE WHEN $10 THEN now() ELSE NULL END)`, op.PlatformID, op.Revision, recovery, artifact, digest, op.ID, test.phase, replacement, generation, test.released); err != nil {
				t.Fatal(err)
			}
			if empty, err := s.NeonProviderStateEmpty(ctx, op); err != nil || empty != test.wantEmpty {
				t.Fatalf("recovery replacement absence differs: empty=%v error=%v", empty, err)
			}
			if _, err := s.Pool.Exec(ctx, `DELETE FROM platform_component_recovery_overrides WHERE platform_id=$1`, op.PlatformID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
