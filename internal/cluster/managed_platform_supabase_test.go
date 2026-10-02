package cluster

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func TestSupabaseDatabaseMigrationScriptIsOneBoundedTransaction(t *testing.T) {
	script, err := supabaseDatabaseMigrationScript([]byte("BEGIN;\nSELECT 1;\nCOMMIT;\n"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(script, []byte("BEGIN;")) != 1 || bytes.Count(script, []byte("COMMIT;")) != 1 || !bytes.Contains(script, []byte(`ALTER DATABASE :"database_name" SET app.settings.jwt_exp TO :'jwt_expiry';`)) {
		t.Fatalf("database migration transaction is malformed: %q", script)
	}
	for _, invalid := range [][]byte{nil, []byte("SELECT 1;"), bytes.Repeat([]byte("x"), (64<<10)+1)} {
		if _, err = supabaseDatabaseMigrationScript(invalid); err == nil {
			t.Fatal("invalid database migration input was accepted")
		}
	}
}

type fakeSupabaseOperationStore struct {
	claims       map[int64][]store.PlatformResourceClaim
	intents      map[int64][]store.PlatformResourceIntent
	heartbeats   int
	advances     int
	verifies     int
	releases     int
	cancels      int
	records      []string
	heartbeatErr error
}

func (s *fakeSupabaseOperationStore) CheckManagedPlatformOperation(context.Context, store.ManagedPlatformOperation) error {
	return nil
}
func (s *fakeSupabaseOperationStore) HeartbeatManagedPlatformOperation(context.Context, store.ManagedPlatformOperation) error {
	s.heartbeats++
	return s.heartbeatErr
}
func (s *fakeSupabaseOperationStore) ClaimPlatformResource(_ context.Context, op store.ManagedPlatformOperation, claim store.PlatformResourceClaim) error {
	s.claims[op.Revision] = append(s.claims[op.Revision], claim)
	return nil
}
func (s *fakeSupabaseOperationStore) ReservePlatformResourceIntent(_ context.Context, op store.ManagedPlatformOperation, intent store.PlatformResourceIntent) (store.PlatformResourceIntent, error) {
	intent.ID = "intent-" + intent.Component
	s.intents[op.Revision] = append(s.intents[op.Revision], intent)
	return intent, nil
}
func (s *fakeSupabaseOperationStore) PlatformResourceIntents(_ context.Context, _ store.ManagedPlatformOperation, revision int64) ([]store.PlatformResourceIntent, error) {
	return append([]store.PlatformResourceIntent(nil), s.intents[revision]...), nil
}
func (s *fakeSupabaseOperationStore) ConfirmPlatformResourceIntent(_ context.Context, op store.ManagedPlatformOperation, intent store.PlatformResourceIntent, claim store.PlatformResourceClaim) error {
	now := metav1.Now().Time
	for i := range s.intents[op.Revision] {
		if s.intents[op.Revision][i].ID == intent.ID {
			s.intents[op.Revision][i].ConfirmedAt = &now
		}
	}
	s.claims[op.Revision] = append(s.claims[op.Revision], claim)
	return nil
}
func (s *fakeSupabaseOperationStore) CancelPlatformResourceIntent(context.Context, store.ManagedPlatformOperation, store.PlatformResourceIntent) error {
	s.cancels++
	return nil
}
func (s *fakeSupabaseOperationStore) AdvancePlatformResourceClaim(_ context.Context, op store.ManagedPlatformOperation, claim store.PlatformResourceClaim) error {
	s.advances++
	old := s.claims[claim.PlatformRevision]
	kept := old[:0]
	for _, item := range old {
		if item.Component != claim.Component {
			kept = append(kept, item)
		}
	}
	s.claims[claim.PlatformRevision] = kept
	claim.PlatformRevision = op.Revision
	claim.OwnerOperationID = op.ID
	s.claims[op.Revision] = append(s.claims[op.Revision], claim)
	return nil
}
func (s *fakeSupabaseOperationStore) VerifyPlatformResourceClaim(context.Context, store.ManagedPlatformOperation, store.PlatformResourceClaim) error {
	s.verifies++
	return nil
}
func (s *fakeSupabaseOperationStore) ReleasePlatformResourceClaim(context.Context, store.ManagedPlatformOperation, store.PlatformResourceClaim) error {
	s.releases++
	return nil
}
func (s *fakeSupabaseOperationStore) PlatformResourceClaims(_ context.Context, _ store.ManagedPlatformOperation, revision int64) ([]store.PlatformResourceClaim, error) {
	return append([]store.PlatformResourceClaim(nil), s.claims[revision]...), nil
}
func (s *fakeSupabaseOperationStore) RecordManagedPlatformStep(_ context.Context, _ store.ManagedPlatformOperation, status, phase, _ string, _ map[string]any) error {
	s.records = append(s.records, status+":"+phase)
	return nil
}

func supabaseTestOperation(revision int64) store.ManagedPlatformOperation {
	return store.ManagedPlatformOperation{ID: "operation", PlatformID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Revision: revision, Kind: "update", Lease: "lease"}
}
func supabaseTestNamespace(op store.ManagedPlatformOperation) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "managed-platform-" + op.PlatformID, UID: types.UID("namespace-uid"), Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": op.PlatformID}}}
}
func supabaseTestMeta(op store.ManagedPlatformOperation, ns *corev1.Namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns.Name, UID: types.UID(name + "-uid"), ResourceVersion: "1", Labels: supabaseLabels(op), OwnerReferences: []metav1.OwnerReference{supabaseNamespaceOwner(ns)}}
}
func newFakeSupabaseStore() *fakeSupabaseOperationStore {
	return &fakeSupabaseOperationStore{claims: map[int64][]store.PlatformResourceClaim{}, intents: map[int64][]store.PlatformResourceIntent{}}
}

func TestSupabaseMutationStopsWhenHeartbeatFails(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	kube := fake.NewSimpleClientset(ns)
	client := &Client{kube: kube}
	state := newFakeSupabaseStore()
	state.heartbeatErr = errors.New("stale lease")
	desired := &corev1.ConfigMap{ObjectMeta: supabaseTestMeta(op, ns, "supabase-config-r1"), Immutable: boolPointer(true), Data: map[string]string{"config": "value"}}
	desired.UID = ""
	desired.ResourceVersion = ""
	err := client.applySupabaseConfigMap(ctx, state, op, ns, desired, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) })
	if !errors.Is(err, state.heartbeatErr) {
		t.Fatalf("expected stale lease error, got %v", err)
	}
	if state.heartbeats != 1 {
		t.Fatalf("expected one heartbeat, got %d", state.heartbeats)
	}
	if _, getErr := kube.CoreV1().ConfigMaps(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{}); !apierrors.IsNotFound(getErr) {
		t.Fatalf("ConfigMap was mutated after stale lease: %v", getErr)
	}
}

func TestSupabaseForeignObjectIsRefused(t *testing.T) {
	op := supabaseTestOperation(2)
	ns := supabaseTestNamespace(op)
	object := &corev1.Secret{ObjectMeta: supabaseTestMeta(op, ns, "snapshot-r2")}
	object.Labels["hakopod.io/managed-platform-id"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := verifySupabaseOwned(object, op.PlatformID, ns.UID); err == nil {
		t.Fatal("foreign object was accepted")
	}
}

func TestSupabaseExistingUnclaimedNamespaceIsRefused(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	client := &Client{kube: fake.NewSimpleClientset(ns)}
	state := newFakeSupabaseStore()
	if _, err := client.ensureSupabaseNamespace(ctx, state, op, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil }); err == nil {
		t.Fatal("existing namespace without a durable UID claim was adopted")
	}
}

func TestSupabaseCreateCrashConfirmsExactPendingIntent(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	key := supabaseClaimKey("namespace", ns.Name)
	intent := store.PlatformResourceIntent{ID: "intent-namespace", PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: key, Kind: "runtime_component", ExternalKey: supabaseExternalKey("namespace", "", ns.Name), OwnerOperationID: op.ID}
	ns.Labels["hakopod.io/owner-operation-id"] = op.ID
	ns.Labels["hakopod.io/resource-intent-id"] = intent.ID
	state := newFakeSupabaseStore()
	state.intents[op.Revision] = []store.PlatformResourceIntent{intent}
	client := &Client{kube: fake.NewSimpleClientset(ns)}
	current := map[string]store.PlatformResourceClaim{}
	if _, err := client.ensureSupabaseNamespace(ctx, state, op, map[string]store.PlatformResourceClaim{}, current, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if current[key].ResourceID != string(ns.UID) {
		t.Fatalf("pending intent was not confirmed: %#v", current[key])
	}
}

func TestSupabasePriorClaimUIDMismatchIsRefused(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	state := newFakeSupabaseStore()
	object := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "supabase-api", UID: types.UID("actual")}}
	prior := map[string]store.PlatformResourceClaim{supabaseClaimKey("service", object.Name): {PlatformID: op.PlatformID, PlatformRevision: 1, Component: supabaseClaimKey("service", object.Name), Kind: "runtime_component", ResourceID: "different", ImmutableGeneration: 1, OwnerOperationID: "prior"}}
	if err := claimOrAdvanceSupabaseObject(ctx, state, op, "service", object, prior, map[string]store.PlatformResourceClaim{}, false); err == nil {
		t.Fatal("mismatched prior UID was accepted")
	}
	if state.advances != 0 {
		t.Fatal("mismatched claim was advanced")
	}
}

func TestSupabaseUnchangedPriorClaimIsAdvancedAndVerified(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	state := newFakeSupabaseStore()
	object := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "supabase-api", UID: types.UID("stable")}}
	key := supabaseClaimKey("service", object.Name)
	prior := map[string]store.PlatformResourceClaim{key: {PlatformID: op.PlatformID, PlatformRevision: 1, Component: key, Kind: "runtime_component", ResourceID: "stable", ImmutableGeneration: 1, OwnerOperationID: "prior"}}
	current := map[string]store.PlatformResourceClaim{}
	if err := claimOrAdvanceSupabaseObject(ctx, state, op, "service", object, prior, current, false); err != nil {
		t.Fatal(err)
	}
	if state.advances != 1 || state.verifies != 1 || len(prior) != 0 || current[key].OwnerOperationID != op.ID {
		t.Fatalf("prior claim was not advanced and verified: advances=%d verifies=%d", state.advances, state.verifies)
	}
}

func TestSupabaseImmutableSecretMismatchIsRefused(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	secret := &corev1.Secret{ObjectMeta: supabaseTestMeta(op, ns, "snapshot-r1"), Immutable: boolPointer(true), Data: map[string][]byte{"value": []byte("stored")}}
	client := &Client{kube: fake.NewSimpleClientset(ns, secret)}
	state := newFakeSupabaseStore()
	err := client.applySupabaseSecret(ctx, state, op, ns, secret.Name, map[string][]byte{"value": []byte("requested")}, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil })
	if err == nil {
		t.Fatal("immutable Secret mismatch was accepted")
	}
	if state.heartbeats != 0 {
		t.Fatal("immutable Secret mismatch attempted a mutation")
	}
}

func TestSupabaseServicePreservesAllocatedAddresses(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	ns := supabaseTestNamespace(op)
	existing := &corev1.Service{ObjectMeta: supabaseTestMeta(op, ns, "supabase-api"), Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.4", ClusterIPs: []string{"10.96.0.4"}, IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol}, Ports: []corev1.ServicePort{{Name: "http", Port: 8000, TargetPort: intstr.FromInt32(8000)}}}}
	kube := fake.NewSimpleClientset(ns, existing)
	client := &Client{kube: kube}
	state := newFakeSupabaseStore()
	key := supabaseClaimKey("service", existing.Name)
	claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: key, Kind: "runtime_component", ResourceID: string(existing.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID}
	current := map[string]store.PlatformResourceClaim{key: claim}
	desired := existing.DeepCopy()
	desired.ResourceVersion = ""
	desired.Spec.ClusterIP = ""
	desired.Spec.ClusterIPs = nil
	desired.Spec.Ports[0].Port = 8443
	if err := client.applySupabaseService(ctx, state, op, ns, desired, map[string]store.PlatformResourceClaim{}, current, func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }); err != nil {
		t.Fatal(err)
	}
	updated, err := kube.CoreV1().Services(ns.Name).Get(ctx, existing.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Spec.ClusterIP != "10.96.0.4" || len(updated.Spec.ClusterIPs) != 1 || updated.Spec.ClusterIPs[0] != "10.96.0.4" {
		t.Fatal("Service allocation was not preserved")
	}
	if state.verifies != 1 || state.heartbeats != 1 {
		t.Fatalf("expected claim verification and heartbeat, got verifies=%d heartbeats=%d", state.verifies, state.heartbeats)
	}
}

func TestSupabaseStatefulSetPreservesDefaultedPodManagementPolicy(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	one := int32(1)
	existing := &appsv1.StatefulSet{
		ObjectMeta: supabaseTestMeta(op, ns, "supabase-database"),
		Spec: appsv1.StatefulSetSpec{
			Replicas:            &one,
			ServiceName:         "db",
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: map[string]string{"app": "database"}},
			Template:            corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "database"}}},
		},
	}
	kube := fake.NewSimpleClientset(ns, existing)
	kube.PrependReactor("update", "statefulsets", func(action kubetesting.Action) (bool, runtime.Object, error) {
		updated := action.(kubetesting.UpdateAction).GetObject().(*appsv1.StatefulSet)
		if updated.Spec.PodManagementPolicy != appsv1.OrderedReadyPodManagement {
			return true, nil, errors.New("immutable podManagementPolicy changed")
		}
		return false, nil, nil
	})
	state := newFakeSupabaseStore()
	key := supabaseClaimKey("statefulset", existing.Name)
	current := map[string]store.PlatformResourceClaim{key: {
		PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: key,
		Kind: "runtime_component", ResourceID: string(existing.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID,
	}}
	desired := existing.DeepCopy()
	desired.ResourceVersion = ""
	desired.Spec.PodManagementPolicy = ""
	if err := (&Client{kube: kube}).applySupabaseStatefulSet(ctx, state, op, ns, desired, map[string]store.PlatformResourceClaim{}, current, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := kube.AppsV1().StatefulSets(ns.Name).Get(ctx, existing.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Spec.PodManagementPolicy != appsv1.OrderedReadyPodManagement {
		t.Fatalf("defaulted pod management policy was not preserved: %q", updated.Spec.PodManagementPolicy)
	}
}

func TestSupabaseCurrentCreateDatabaseClaimRequiresExactDurableOwnership(t *testing.T) {
	op := supabaseTestOperation(1)
	op.Kind = "create"
	ns := supabaseTestNamespace(op)
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: supabaseTestMeta(op, ns, "supabase-database")}
	key := supabaseClaimKey("pvc", claim.Name)
	durable := store.PlatformResourceClaim{
		PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: key,
		Kind: "runtime_component", ResourceID: string(claim.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID,
	}
	current := map[string]store.PlatformResourceClaim{key: durable}
	state := newFakeSupabaseStore()
	for attempt := 1; attempt <= 2; attempt++ {
		if err := recoverSupabaseCurrentCreateDatabaseClaim(context.Background(), state, op, ns, claim, map[string]store.PlatformResourceClaim{}, current, func() error { return nil }); err != nil {
			t.Fatalf("same create retry %d was rejected: %v", attempt, err)
		}
	}

	if err := verifySupabaseCurrentCreateDatabaseClaim(op, ns, claim, map[string]store.PlatformResourceClaim{}); err == nil {
		t.Fatal("unclaimed database PVC was accepted")
	}
	mismatched := durable
	mismatched.ResourceID = "different-uid"
	if err := verifySupabaseCurrentCreateDatabaseClaim(op, ns, claim, map[string]store.PlatformResourceClaim{key: mismatched}); err == nil {
		t.Fatal("mismatched database PVC UID was accepted")
	}
	foreign := claim.DeepCopy()
	foreign.Labels["hakopod.io/managed-platform-id"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := verifySupabaseCurrentCreateDatabaseClaim(op, ns, foreign, current); err == nil {
		t.Fatal("foreign database PVC was accepted")
	}
	update := op
	update.Kind = "update"
	if err := verifySupabaseCurrentCreateDatabaseClaim(update, ns, claim, current); err == nil {
		t.Fatal("update operation claimed initial-create ownership")
	}

	pending := claim.DeepCopy()
	pending.UID = types.UID("pending-uid")
	intent := store.PlatformResourceIntent{
		ID: "intent-database-pvc", PlatformID: op.PlatformID, PlatformRevision: op.Revision,
		Component: key, Kind: "runtime_component", ExternalKey: supabaseExternalKey("pvc", ns.Name, pending.Name), OwnerOperationID: op.ID,
	}
	pending.Labels["hakopod.io/resource-intent-id"] = intent.ID
	pending.Labels["hakopod.io/owner-operation-id"] = op.ID
	state = newFakeSupabaseStore()
	state.intents[op.Revision] = []store.PlatformResourceIntent{intent}
	recovered := map[string]store.PlatformResourceClaim{}
	if err := recoverSupabaseCurrentCreateDatabaseClaim(context.Background(), state, op, ns, pending, map[string]store.PlatformResourceClaim{}, recovered, func() error { return nil }); err != nil {
		t.Fatalf("same-operation pending intent was not recovered: %v", err)
	}
	foreignPending := pending.DeepCopy()
	foreignPending.UID = types.UID("foreign-pending-uid")
	foreignPending.Labels["hakopod.io/managed-platform-id"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	foreignState := newFakeSupabaseStore()
	foreignIntent := intent
	foreignIntent.ID = "intent-foreign-database-pvc"
	foreignPending.Labels["hakopod.io/resource-intent-id"] = foreignIntent.ID
	foreignState.intents[op.Revision] = []store.PlatformResourceIntent{foreignIntent}
	if err := recoverSupabaseCurrentCreateDatabaseClaim(context.Background(), foreignState, op, ns, foreignPending, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil }); err == nil {
		t.Fatal("foreign pending-intent PVC was adopted")
	}
	if len(foreignState.claims[op.Revision]) != 0 || foreignState.intents[op.Revision][0].ConfirmedAt != nil {
		t.Fatal("foreign pending-intent PVC mutated durable ownership")
	}
	wrongScope := op
	wrongScope.Kind = "update"
	wrongScopeState := newFakeSupabaseStore()
	wrongScopeState.intents[op.Revision] = []store.PlatformResourceIntent{intent}
	if err := recoverSupabaseCurrentCreateDatabaseClaim(context.Background(), wrongScopeState, wrongScope, ns, pending, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil }); err == nil {
		t.Fatal("non-create pending-intent PVC was accepted")
	}
	if len(wrongScopeState.claims[op.Revision]) != 0 || wrongScopeState.intents[op.Revision][0].ConfirmedAt != nil {
		t.Fatal("non-create pending-intent PVC mutated durable ownership")
	}
}

func TestSupabaseDatabaseClaimPreparationClearsStaleCreateBinding(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	op.Kind = "create"
	ns := supabaseTestNamespace(op)
	render := managedplatform.SupabaseRenderInput{DatabaseClaim: managedplatform.ObservedClaimState{Observed: true, UID: "stale"}, DatabaseClaimFromCurrentCreate: true}
	client := &Client{kube: fake.NewSimpleClientset(ns)}
	if err := client.prepareSupabaseDatabaseClaim(ctx, newFakeSupabaseStore(), op, ns, &render, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !render.DatabaseClaim.Observed || render.DatabaseClaim.UID != "" || render.DatabaseClaimFromCurrentCreate {
		t.Fatalf("stale database claim binding survived observation: %#v", render.DatabaseClaim)
	}
}

func TestObserveSupabaseReportsPendingWorkloadsAndClaims(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	deployment := &appsv1.Deployment{ObjectMeta: supabaseTestMeta(op, ns, "supabase-auth"), Spec: appsv1.DeploymentSpec{Replicas: func() *int32 { value := int32(1); return &value }()}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: supabaseTestMeta(op, ns, "supabase-database")}
	client := &Client{kube: fake.NewSimpleClientset(ns, deployment, pvc)}
	manifests := managedplatform.SupabaseManifests{Namespace: *ns, ExpectedUID: ns.UID, Objects: []runtime.Object{deployment, pvc}}
	observation, err := client.ObserveSupabase(ctx, op, manifests, map[string]store.PlatformResourceClaim{supabaseClaimKey("namespace", ns.Name): {ResourceID: string(ns.UID), ImmutableGeneration: 1}, supabaseClaimKey("deployment", deployment.Name): {ResourceID: string(deployment.UID), ImmutableGeneration: 1}, supabaseClaimKey("pvc", pvc.Name): {ResourceID: string(pvc.UID), ImmutableGeneration: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != "pending" || observation.ExpectedComponents != 2 || observation.ReadyComponents != 0 || len(observation.Pending) != 2 {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func TestObserveSupabaseRefusesReplacementUID(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	ns := supabaseTestNamespace(op)
	deployment := &appsv1.Deployment{ObjectMeta: supabaseTestMeta(op, ns, "supabase-auth"), Spec: appsv1.DeploymentSpec{Replicas: func() *int32 { value := int32(1); return &value }()}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 1}}
	deployment.Generation = 1
	client := &Client{kube: fake.NewSimpleClientset(ns, deployment)}
	manifests := managedplatform.SupabaseManifests{Namespace: *ns, ExpectedUID: ns.UID, Objects: []runtime.Object{deployment}}
	claims := map[string]store.PlatformResourceClaim{supabaseClaimKey("namespace", ns.Name): {ResourceID: string(ns.UID), ImmutableGeneration: 1}, supabaseClaimKey("deployment", deployment.Name): {ResourceID: "replaced-uid", ImmutableGeneration: 1}}
	if _, err := client.ObserveSupabase(ctx, op, manifests, claims); err == nil {
		t.Fatal("replacement Deployment reported ready")
	}
}

func TestObserveSupabaseWaitsForOldDeploymentPod(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	ns := supabaseTestNamespace(op)
	one := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: supabaseTestMeta(op, ns, "supabase-auth"), Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, UpdatedReplicas: 1, AvailableReplicas: 1}}
	deployment.Generation = 1
	client := &Client{kube: fake.NewSimpleClientset(ns, deployment)}
	manifests := managedplatform.SupabaseManifests{Namespace: *ns, ExpectedUID: ns.UID, Objects: []runtime.Object{deployment}}
	claims := map[string]store.PlatformResourceClaim{supabaseClaimKey("namespace", ns.Name): {ResourceID: string(ns.UID), ImmutableGeneration: 1}, supabaseClaimKey("deployment", deployment.Name): {ResourceID: string(deployment.UID), ImmutableGeneration: 1}}
	observation, err := client.ObserveSupabase(ctx, op, manifests, claims)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != "pending" || len(observation.Pending) != 1 {
		t.Fatalf("old deployment pod allowed capacity contraction: %#v", observation)
	}
}

func TestSupabaseDeleteRequeuesThenReleasesAfterNamespaceIsAbsent(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	op.Kind = "delete"
	ns := supabaseTestNamespace(op)
	secret := &corev1.Secret{ObjectMeta: supabaseTestMeta(op, ns, "snapshot-r1")}
	key := supabaseClaimKey("secret", secret.Name)
	claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: key, Kind: "runtime_component", ResourceID: string(secret.UID), ImmutableGeneration: 1, OwnerOperationID: "prior"}
	state := newFakeSupabaseStore()
	state.claims[1] = []store.PlatformResourceClaim{claim, store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: supabaseClaimKey("namespace", ns.Name), Kind: "runtime_component", ResourceID: string(ns.UID), ImmutableGeneration: 1, OwnerOperationID: "prior"}}
	kube := fake.NewSimpleClientset(ns, secret)
	client := &Client{kube: kube}
	if err := client.deleteSupabaseOperation(ctx, state, op, func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }); err != nil {
		t.Fatal(err)
	}
	if len(state.records) != 1 || state.records[0] != "queued:waiting-delete" || state.releases != 0 {
		t.Fatalf("delete did not requeue safely: %#v releases=%d", state.records, state.releases)
	}
	if err := client.deleteSupabaseOperation(ctx, state, op, func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }); err != nil {
		t.Fatal(err)
	}
	if state.releases != 2 || state.records[len(state.records)-1] != "succeeded:deleted" {
		t.Fatalf("resumed delete did not release and finish: %#v releases=%d", state.records, state.releases)
	}
}

func TestSupabaseDeleteToleratesMissingChildWhileNamespaceTerminates(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	op.Kind = "delete"
	ns := supabaseTestNamespace(op)
	now := metav1.Now()
	ns.DeletionTimestamp = &now
	ns.Finalizers = []string{"fixture.hakopod.dev/hold"}
	namespaceKey := supabaseClaimKey("namespace", ns.Name)
	missingKey := supabaseClaimKey("secret", "snapshot-r1")
	state := newFakeSupabaseStore()
	state.claims[2] = []store.PlatformResourceClaim{{PlatformID: op.PlatformID, PlatformRevision: 2, Component: namespaceKey, Kind: "runtime_component", ResourceID: string(ns.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID}, {PlatformID: op.PlatformID, PlatformRevision: 2, Component: missingKey, Kind: "runtime_component", ResourceID: "deleted-child-uid", ImmutableGeneration: 1, OwnerOperationID: op.ID}}
	state.intents[2] = []store.PlatformResourceIntent{{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PlatformID: op.PlatformID, PlatformRevision: 2, Component: supabaseClaimKey("configmap", "never-created"), Kind: "runtime_component", ExternalKey: supabaseExternalKey("configmap", ns.Name, "never-created"), OwnerOperationID: op.ID}}
	client := &Client{kube: fake.NewSimpleClientset(ns)}
	if err := client.deleteSupabaseOperation(ctx, state, op, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if state.releases != 0 {
		t.Fatalf("released %d claims before namespace absence", state.releases)
	}
	if state.cancels != 1 {
		t.Fatalf("pending absent intent was not cancelled: %d", state.cancels)
	}
	if len(state.records) != 1 || state.records[0] != "queued:waiting-delete" {
		t.Fatalf("unexpected records: %#v", state.records)
	}
}

func TestSupabasePruneListBoundIsEnforced(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	ns := supabaseTestNamespace(op)
	objects := []runtime.Object{ns}
	for i := 0; i <= maxSupabaseRuntimeObjects; i++ {
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: supabaseTestMeta(op, ns, "old-"+string(rune('a'+i%26))+string(rune('a'+i/26)))})
	}
	clientset := fake.NewSimpleClientset(objects...)
	clientset.PrependReactor("list", "configmaps", func(kubetesting.Action) (bool, runtime.Object, error) {
		list := &corev1.ConfigMapList{}
		for _, object := range objects[1:] {
			list.Items = append(list.Items, *object.(*corev1.ConfigMap))
		}
		return true, list, nil
	})
	client := &Client{kube: clientset}
	state := newFakeSupabaseStore()
	err := client.pruneSupabaseSnapshots(ctx, state, op, ns, managedplatform.SupabaseManifests{}, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}, func() error { return nil })
	if err == nil {
		t.Fatal("over-bound ConfigMap list was accepted")
	}
}
