package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func enableManagedPlatformCapacity(s *Store, nodes ...string) managedplatform.CapacityPolicy {
	policy := managedplatform.CapacityPolicy{Enabled: true, Pool: "fixture", Capacity: managedplatform.Capacity{CPUMilli: 12000, MemoryBytes: 48 << 30, StorageGiB: 512}, StorageClass: "encrypted-block"}
	for _, name := range nodes {
		policy.Nodes = append(policy.Nodes, managedplatform.CapacityNode{Name: name, UID: "uid-" + name})
	}
	s.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return policy, nil
	}
	s.ManagedCapacityPool = func(context.Context, pgx.Tx, string, string) (string, error) {
		return policy.Pool, nil
	}
	return policy
}

func TestManagedPlatformCapacityControllerMapping(t *testing.T) {
	cases := map[string]struct {
		kind string
		key  string
		want string
	}{
		"supabase deployment": {kind: "supabase", key: "workload/auth", want: "deployment.supabase-auth"},
		"supabase database":   {kind: "supabase", key: "workload/database", want: "statefulset.supabase-database"},
		"neon deployment":     {kind: "neon", key: "workload/proxy/0", want: "deployment.neon-proxy"},
		"neon stateful":       {kind: "neon", key: "workload/pageserver/2", want: "statefulset.neon-pageserver-2"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := managedPlatformCapacityController(tc.kind, tc.key)
			if !ok || got != tc.want {
				t.Fatalf("unexpected controller mapping: %q %t", got, ok)
			}
		})
	}
	if _, ok := managedPlatformCapacityController("neon", "storage/pageserver/0"); ok {
		t.Fatal("storage reservation mapped to a workload controller")
	}
}

func TestManagedPlatformCapacityScopesAreNotLimitedByPerEnvironmentPlatformLimit(t *testing.T) {
	s, _, _, _ := managedPlatformFixture(t)
	ctx := context.Background()
	for i := 0; i < MaxManagedPlatforms+1; i++ {
		project := fmt.Sprintf("capacity-scope-%d", i)
		id := fmt.Sprintf("%032d", i+1)
		if _, err := s.Pool.Exec(ctx, `WITH inserted AS (INSERT INTO projects(name) VALUES($1) RETURNING name) INSERT INTO environments(project,name) SELECT name,'development' FROM inserted`, project); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO managed_platforms(id,project,environment,name,kind,revision,desired_spec) VALUES($1,$2,'development','platform','supabase',1,'{"name":"platform","kind":"supabase"}')`, id, project); err != nil {
			t.Fatal(err)
		}
	}
	scopes, err := s.ManagedPlatformCapacityScopes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) < MaxManagedPlatforms+1 {
		t.Fatalf("capacity discovery applied the per-environment platform limit globally: %d", len(scopes))
	}
}

func TestManagedCapacityScopesBackfillFromTrustedPolicyAndRejectPoolChange(t *testing.T) {
	s, p, item, _ := managedPlatformFixture(t)
	ctx := context.Background()
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	app := emptyTestSpec()
	app.Name = "capacity-scope-backfill"
	if _, err := s.Accept(ctx, p, item.Project, item.Environment, app, 0, "capacity-scope-backfill"); err != nil {
		t.Fatal(err)
	}
	policy := enableManagedPlatformCapacity(s, "node-a")
	if err := s.ReconcileManagedCapacityScopes(ctx); err != nil {
		t.Fatal(err)
	}
	var pool string
	if err := s.Pool.QueryRow(ctx, `SELECT capacity_pool FROM managed_capacity_scopes WHERE project=$1 AND environment=$2`, item.Project, item.Environment).Scan(&pool); err != nil || pool != policy.Pool {
		t.Fatal("trusted startup reconciliation did not bind the legacy scope", pool, err)
	}
	s.ManagedCapacityPool = func(context.Context, pgx.Tx, string, string) (string, error) { return "changed", nil }
	if err := s.ReconcileManagedCapacityScopes(ctx); err == nil {
		t.Fatal("durable capacity scope changed pools")
	}
	s.ManagedCapacityPool = nil
	if err := s.ReconcileManagedCapacityScopes(ctx); err == nil {
		t.Fatal("durable capacity scope silently became unmanaged")
	}
}

func TestManagedCapacityScopesRejectConflictingPoliciesForOnePool(t *testing.T) {
	s, p, item, _ := managedPlatformFixture(t)
	ctx := context.Background()
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	if _, err := s.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'other')`, item.Project); err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{item.Environment, "other"} {
		app := emptyTestSpec()
		app.Name = "capacity-policy-" + environment
		if _, err := s.Accept(ctx, p, item.Project, environment, app, 0, "capacity-policy-"+environment); err != nil {
			t.Fatal(err)
		}
	}
	policy := enableManagedPlatformCapacity(s, "node-a")
	s.ManagedPlatformCapacityBudget = func(_ context.Context, _ pgx.Tx, _, environment string) (managedplatform.CapacityPolicy, error) {
		current := policy
		if environment == "other" {
			current.Capacity.CPUMilli++
		}
		return current, nil
	}
	if err := s.ReconcileManagedCapacityScopes(ctx); err == nil {
		t.Fatal("one durable pool accepted conflicting trusted policies")
	}
}

func TestManagedCapacityPoolRejectsPolicyDriftDuringAdmission(t *testing.T) {
	s, p, item, _ := managedPlatformFixture(t)
	ctx := context.Background()
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	if _, err := s.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'other')`, item.Project); err != nil {
		t.Fatal(err)
	}
	policy := enableManagedPlatformCapacity(s, "node-a")
	first := emptyTestSpec()
	first.Name = "capacity-policy-first"
	if _, err := s.Accept(ctx, p, item.Project, item.Environment, first, 0, "capacity-policy-first"); err != nil {
		t.Fatal(err)
	}
	s.ManagedPlatformCapacityBudget = func(_ context.Context, _ pgx.Tx, _, environment string) (managedplatform.CapacityPolicy, error) {
		current := policy
		if environment == "other" {
			current.Capacity.CPUMilli++
		}
		return current, nil
	}
	second := emptyTestSpec()
	second.Name = "capacity-policy-second"
	if _, err := s.Accept(ctx, p, item.Project, "other", second, 0, "capacity-policy-second"); !errors.Is(err, ErrConflict) {
		t.Fatalf("admission accepted a changed policy for an existing physical pool: %v", err)
	}
}

func TestManagedPlatformCapacitySerializesConcurrentEnvironmentsInOnePool(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	policy := enableManagedPlatformCapacity(s, "node-a", "node-b", "node-c")
	plan.StorageClass = "encrypted-block"
	reservations, err := managedplatform.CapacityReservations(item.Spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	peak := maxNodeCapacity(reservationsByNode(reservations))
	policy.Capacity = managedplatform.Capacity{CPUMilli: peak.CPUMilli, MemoryBytes: peak.MemoryBytes, StorageGiB: managedplatform.ReservationTotal(reservations).StorageGiB}
	s.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return policy, nil
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'other')`, item.Project); err != nil {
		t.Fatal(err)
	}
	other := item
	other.ID = NewID()
	other.Environment = "other"
	other.Spec.Name = "other-platform"
	otherPlan := plan
	otherPlan.Namespace = "managed-platform-other-platform"
	reviews := []ManagedPlatformReview{
		managedPlatformReview(t, s, p, item, plan, 0, "create"),
		managedPlatformReview(t, s, p, other, otherPlan, 0, "create"),
	}

	type result struct {
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var group sync.WaitGroup
	for i, candidate := range []ManagedPlatform{item, other} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			candidatePlan := plan
			if candidate.Environment == "other" {
				candidatePlan = otherPlan
			}
			_, err := s.AcceptManagedPlatform(ctx, p, candidate, candidatePlan, []byte("sealed-cross-environment"), reviews[i], 0, "capacity-cross-environment-"+candidate.Environment, "create")
			results <- result{err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	accepted, rejected := 0, 0
	for result := range results {
		if result.err == nil {
			accepted++
		} else if errors.Is(result.err, ErrConflict) {
			rejected++
		} else {
			t.Fatal(result.err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("cross-environment pool admitted %d and rejected %d operations", accepted, rejected)
	}
	var owners int
	if err = s.Pool.QueryRow(ctx, `SELECT count(DISTINCT platform_id) FROM managed_platform_capacity_reservations WHERE capacity_pool=$1`, policy.Pool).Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("cross-environment pool retained %d platform reservations: %v", owners, err)
	}
}

func TestManagedPlatformCapacityOverlapUsesReplacementEnvelope(t *testing.T) {
	old := []managedplatform.CapacityReservation{{Key: "workload/api/0", NodeName: "node-a", Capacity: managedplatform.Capacity{CPUMilli: 500, MemoryBytes: 1 << 30}}}
	next := []managedplatform.CapacityReservation{{Key: "workload/api/0", NodeName: "node-a", Capacity: managedplatform.Capacity{CPUMilli: 750, MemoryBytes: 2 << 30}}}
	merged := mergeManagedPlatformReservations(old, next)
	if len(merged) != 1 || merged[0].Capacity.CPUMilli != 750 || merged[0].Capacity.MemoryBytes != 2<<30 {
		t.Fatalf("recreate rollout did not retain the larger old/new envelope: %#v", merged)
	}
	next[0].NodeName = "node-b"
	merged = mergeManagedPlatformReservations(old, next)
	if len(merged) != 2 {
		t.Fatalf("placement change did not reserve old and new nodes: %#v", merged)
	}
}

func TestManagedPlatformCapacityRetainsOverlapAndContractsAfterSuccess(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	enableManagedPlatformCapacity(s, "node-a", "node-b", "node-c", "node-d", "node-e", "node-f")
	plan.StorageClass = "encrypted-block"
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-create"), review, 0, "capacity-create", "create"); err != nil {
		t.Fatal(err)
	}
	created, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, created, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.Spec.Placement.NodeNames = []string{"node-d", "node-e", "node-f"}
	updatedPlan := plan
	updatedPlan.Namespace = plan.Namespace
	// Placement is held in the spec; the plan's component inventory is unchanged.
	updateReview := managedPlatformReview(t, s, p, next, updatedPlan, 1, "update")
	if _, err = s.AcceptManagedPlatform(ctx, p, next, updatedPlan, []byte("sealed-update"), updateReview, 1, "capacity-update", "update"); err != nil {
		t.Fatal(err)
	}
	var oldNodes, newNodes int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE node_name=ANY($2)),count(*) FILTER (WHERE node_name=ANY($3)) FROM managed_platform_capacity_reservations WHERE platform_id=$1`, item.ID, []string{"node-a", "node-b", "node-c"}, []string{"node-d", "node-e", "node-f"}).Scan(&oldNodes, &newNodes); err != nil {
		t.Fatal(err)
	}
	if oldNodes == 0 || newNodes == 0 {
		t.Fatalf("placement overlap was not retained: old=%d new=%d", oldNodes, newNodes)
	}
	failed, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, failed, "failed", "failed", "fixture", nil); err != nil {
		t.Fatal(err)
	}
	var afterFailure int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_platform_capacity_reservations WHERE platform_id=$1`, item.ID).Scan(&afterFailure); err != nil || afterFailure < oldNodes+newNodes {
		t.Fatalf("failed update released retained capacity: %d %v", afterFailure, err)
	}

	current, err = s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	retryReview := managedPlatformReview(t, s, p, current, updatedPlan, 2, "update")
	if _, err = s.AcceptManagedPlatform(ctx, p, current, updatedPlan, []byte("sealed-retry"), retryReview, 2, "capacity-retry", "update"); err != nil {
		t.Fatal(err)
	}
	succeeded, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, succeeded, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_platform_capacity_reservations WHERE platform_id=$1 AND node_name=ANY($2)`, item.ID, []string{"node-a", "node-b", "node-c"}).Scan(&oldNodes); err != nil || oldNodes != 0 {
		t.Fatalf("successful reconciliation did not contract old placement: %d %v", oldNodes, err)
	}
}

func TestManagedPlatformDeleteRetainsCapacityUntilConfirmed(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	enableManagedPlatformCapacity(s, "node-a", "node-b", "node-c")
	plan.StorageClass = "encrypted-block"
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-create"), review, 0, "delete-capacity-create", "create"); err != nil {
		t.Fatal(err)
	}
	create, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, create, "succeeded", "ready", "", nil); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagedPlatform(ctx, p, item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	deleteReview := managedPlatformReview(t, s, p, current, plan, 1, "delete")
	if _, err = s.AcceptManagedPlatform(ctx, p, current, plan, []byte("sealed-delete"), deleteReview, 1, "delete-capacity-final", "delete"); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_platform_capacity_reservations WHERE platform_id=$1`, item.ID).Scan(&retained); err != nil || retained == 0 {
		t.Fatalf("delete acceptance released capacity: %d %v", retained, err)
	}
	deleting, err := s.ClaimManagedPlatformOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordManagedPlatformStep(ctx, deleting, "succeeded", "deleted", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_platform_capacity_reservations WHERE platform_id=$1`, item.ID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("confirmed deletion retained capacity: %d %v", retained, err)
	}
}

func TestManagedPlatformCapacityFingerprintRejectsStaleReview(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	policy := enableManagedPlatformCapacity(s, "node-a", "node-b", "node-c")
	plan.StorageClass = "encrypted-block"
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")
	policy.Capacity.CPUMilli++
	s.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return policy, nil
	}
	if _, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed"), review, 0, "stale-capacity-review", "create"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale capacity review accepted: %v", err)
	}
}

func TestManagedPlatformAndDatabaseCapacitySerialize(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	plan.StorageClass = "encrypted-block"
	d := database.Resource{
		ID:                   NewID(),
		Project:              item.Project,
		Environment:          item.Environment,
		Spec:                 database.Spec{SchemaVersion: 1, Name: "capacity-race-database", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}.WithSecureDefaults(),
		EncryptedCredentials: []byte("sealed-capacity-race"),
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'other')`, item.Project); err != nil {
		t.Fatal(err)
	}
	d.Environment = "other"
	platformReservations, err := managedplatform.CapacityReservations(item.Spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	platformPeak := maxNodeCapacity(reservationsByNode(platformReservations))
	platformTotal := managedplatform.ReservationTotal(platformReservations)
	databaseCPU, err := d.Spec.CPUReservationMilli()
	if err != nil {
		t.Fatal(err)
	}
	policy := managedplatform.CapacityPolicy{
		Enabled:      true,
		Pool:         "fixture",
		Capacity:     managedplatform.Capacity{CPUMilli: max(platformPeak.CPUMilli, databaseCPU), MemoryBytes: max(platformPeak.MemoryBytes, DatabaseMemoryReservation(d.Spec)), StorageGiB: max(platformTotal.StorageGiB, DatabaseStorageReservation(d.Spec))},
		StorageClass: "encrypted-block",
		Nodes: []managedplatform.CapacityNode{
			{Name: "node-a", UID: "uid-node-a"},
			{Name: "node-b", UID: "uid-node-b"},
			{Name: "node-c", UID: "uid-node-c"},
		},
	}
	s.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return policy, nil
	}
	s.ManagedCapacityPool = func(context.Context, pgx.Tx, string, string) (string, error) { return policy.Pool, nil }
	s.DatabaseCapacityBudget = func(context.Context, pgx.Tx, string, string) (database.Capacity, error) {
		return database.Capacity{CPUMilli: policy.Capacity.CPUMilli, MemoryBytes: policy.Capacity.MemoryBytes, StorageGiB: policy.Capacity.StorageGiB}, nil
	}
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-capacity-race"), review, 0, "capacity-race-platform", "create")
		results <- err
	}()
	go func() {
		defer group.Done()
		<-start
		_, err := s.AcceptDatabase(ctx, p, d, 0, "capacity-race-database", "create")
		results <- err
	}()
	close(start)
	group.Wait()
	close(results)

	var accepted atomic.Int32
	for err := range results {
		if err == nil {
			accepted.Add(1)
			continue
		}
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("unexpected capacity race error: %v", err)
		}
	}
	if accepted.Load() != 1 {
		t.Fatalf("concurrent database and platform admission accepted %d allocations with capacity for one: platform_peak=%+v platform_total=%+v database_cpu=%d database_memory=%d database_storage=%d policy=%+v", accepted.Load(), platformPeak, platformTotal, databaseCPU, DatabaseMemoryReservation(d.Spec), DatabaseStorageReservation(d.Spec), policy.Capacity)
	}
}

func TestManagedPlatformAndApplicationCapacitySerialize(t *testing.T) {
	s, p, item, plan := managedPlatformFixture(t)
	ctx := context.Background()
	plan.StorageClass = "encrypted-block"
	policy := enableManagedPlatformCapacity(s, "node-a", "node-b", "node-c")
	platformReservations, err := managedplatform.CapacityReservations(item.Spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	platformMemory := managedplatform.ReservationTotal(platformReservations).MemoryBytes
	app := emptyTestSpec()
	app.Name = "capacity-race-application"
	appMemory, err := applicationMemoryReservation(app)
	if err != nil {
		t.Fatal(err)
	}
	s.ComputeBudget = func(context.Context, pgx.Tx, string, string) (int64, error) {
		return max(platformMemory, appMemory) + (256 << 20), nil
	}
	appCapacity, err := applicationCapacityReservation(app)
	if err != nil {
		t.Fatal(err)
	}
	platformPeak := maxNodeCapacity(reservationsByNode(platformReservations))
	platformTotal := managedplatform.ReservationTotal(platformReservations)
	policy.Capacity = managedplatform.Capacity{CPUMilli: max(platformPeak.CPUMilli, appCapacity.CPUMilli), MemoryBytes: max(platformPeak.MemoryBytes, appCapacity.MemoryBytes), StorageGiB: platformTotal.StorageGiB}
	s.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return policy, nil
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'other')`, item.Project); err != nil {
		t.Fatal(err)
	}
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	review := managedPlatformReview(t, s, p, item, plan, 0, "create")

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, err := s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-capacity-app-race"), review, 0, "capacity-race-platform-app", "create")
		results <- err
	}()
	go func() {
		defer group.Done()
		<-start
		_, err := s.Accept(ctx, p, item.Project, "other", app, 0, "capacity-race-application")
		results <- err
	}()
	close(start)
	group.Wait()
	close(results)

	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
			continue
		}
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("unexpected capacity race error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent application and platform admission accepted %d allocations with capacity for one: platform_memory=%d application_memory=%d limit=%d", accepted, platformMemory, appMemory, max(platformMemory, appMemory)+(256<<20))
	}
}

func TestManagedPlatformCapacityIncludesCommittedCrossEnvironmentUsage(t *testing.T) {
	for _, kind := range []string{"database", "application"} {
		t.Run(kind, func(t *testing.T) {
			s, p, item, plan := managedPlatformFixture(t)
			ctx := context.Background()
			plan.StorageClass = "encrypted-block"
			if _, err := s.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'other')`, item.Project); err != nil {
				t.Fatal(err)
			}
			platformReservations, err := managedplatform.CapacityReservations(item.Spec, plan)
			if err != nil {
				t.Fatal(err)
			}
			platformPeak := maxNodeCapacity(reservationsByNode(platformReservations))
			platformTotal := managedplatform.ReservationTotal(platformReservations)
			policy := managedplatform.CapacityPolicy{Enabled: true, Pool: "fixture", Capacity: platformPeak, StorageClass: "encrypted-block", Nodes: []managedplatform.CapacityNode{{Name: "node-a", UID: "uid-node-a"}, {Name: "node-b", UID: "uid-node-b"}, {Name: "node-c", UID: "uid-node-c"}}}
			policy.Capacity.StorageGiB = platformTotal.StorageGiB
			s.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
				return policy, nil
			}
			s.ManagedCapacityPool = func(context.Context, pgx.Tx, string, string) (string, error) { return policy.Pool, nil }
			s.DatabaseCapacityBudget = func(context.Context, pgx.Tx, string, string) (database.Capacity, error) {
				return database.Capacity(policy.Capacity), nil
			}
			s.ComputeBudget = func(context.Context, pgx.Tx, string, string) (int64, error) { return 64 << 30, nil }
			s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
			if kind == "database" {
				d := database.Resource{ID: NewID(), Project: item.Project, Environment: "other", Spec: database.Spec{SchemaVersion: 1, Name: "committed-capacity-database", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}.WithSecureDefaults(), EncryptedCredentials: []byte("sealed-committed-capacity")}
				if _, err = s.AcceptDatabase(ctx, p, d, 0, "committed-capacity-database", "create"); err != nil {
					t.Fatal(err)
				}
			} else {
				app := emptyTestSpec()
				app.Name = "committed-capacity-application"
				if _, err = s.Accept(ctx, p, item.Project, "other", app, 0, "committed-capacity-application"); err != nil {
					t.Fatal(err)
				}
			}
			review := managedPlatformReview(t, s, p, item, plan, 0, "create")
			if _, err = s.AcceptManagedPlatform(ctx, p, item, plan, []byte("sealed-committed-capacity"), review, 0, "committed-capacity-platform-"+kind, "create"); !errors.Is(err, ErrConflict) {
				t.Fatalf("platform ignored committed cross-environment %s capacity: %v", kind, err)
			}
		})
	}
}
