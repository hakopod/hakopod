package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/util/validation"
)

func lockManagedCapacityPoolID(ctx context.Context, tx pgx.Tx, pool string) error {
	if len(validation.IsDNS1123Label(pool)) != 0 {
		return fmt.Errorf("managed capacity pool is invalid")
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,73))`, pool)
	return err
}

func lockManagedCapacityPool(ctx context.Context, tx pgx.Tx, project, environment, pool string) error {
	if err := lockManagedCapacityPoolID(ctx, tx, pool); err != nil {
		return err
	}
	var existing string
	err := tx.QueryRow(ctx, `SELECT capacity_pool FROM managed_capacity_scopes WHERE project=$1 AND environment=$2 FOR UPDATE`, project, environment).Scan(&existing)
	if err == nil {
		if existing != pool {
			return fmt.Errorf("managed capacity scope pool changed")
		}
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_capacity_scopes(project,environment,capacity_pool) VALUES($1,$2,$3)`, project, environment, pool)
	return err
}

func lockManagedCapacityPolicy(ctx context.Context, tx pgx.Tx, pool string, fingerprint [32]byte) error {
	if err := lockManagedCapacityPoolID(ctx, tx, pool); err != nil {
		return err
	}
	var existing []byte
	err := tx.QueryRow(ctx, `SELECT policy_fingerprint FROM managed_capacity_policies WHERE capacity_pool=$1 FOR UPDATE`, pool).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, fingerprint[:]) {
			return fmt.Errorf("%w: managed capacity pool policy changed; use a new capacity pool", ErrConflict)
		}
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO managed_capacity_policies(capacity_pool,policy_fingerprint) VALUES($1,$2)`, pool, fingerprint[:])
	return err
}

func (s *Store) managedCapacityPoolTx(ctx context.Context, tx pgx.Tx, project, environment string) (string, error) {
	if s.ManagedCapacityPool == nil {
		var existing string
		err := tx.QueryRow(ctx, `SELECT capacity_pool FROM managed_capacity_scopes WHERE project=$1 AND environment=$2`, project, environment).Scan(&existing)
		if err == nil {
			return "", fmt.Errorf("managed capacity scope policy is unavailable")
		}
		if err != pgx.ErrNoRows {
			return "", err
		}
		return "", nil
	}
	pool, err := s.ManagedCapacityPool(ctx, tx, project, environment)
	if err != nil {
		return pool, err
	}
	if pool == "" {
		var existing string
		err = tx.QueryRow(ctx, `SELECT capacity_pool FROM managed_capacity_scopes WHERE project=$1 AND environment=$2`, project, environment).Scan(&existing)
		if err == nil {
			return "", fmt.Errorf("managed capacity scope policy is unavailable")
		}
		if err != pgx.ErrNoRows {
			return "", err
		}
		return "", nil
	}
	return pool, lockManagedCapacityPool(ctx, tx, project, environment, pool)
}

func (s *Store) managedPlatformCapacityPolicyTx(ctx context.Context, tx pgx.Tx, project, environment string) (managedplatform.CapacityPolicy, [32]byte, error) {
	if s.ManagedPlatformCapacityBudget == nil {
		return managedplatform.CapacityPolicy{}, [32]byte{}, fmt.Errorf("managed platform capacity policy is unavailable")
	}
	policy, err := s.ManagedPlatformCapacityBudget(ctx, tx, project, environment)
	if err != nil {
		return managedplatform.CapacityPolicy{}, [32]byte{}, err
	}
	fingerprint, err := policy.Fingerprint()
	if err == nil {
		err = lockManagedCapacityPolicy(ctx, tx, policy.Pool, fingerprint)
	}
	return policy, fingerprint, err
}

func loadManagedPlatformReservations(ctx context.Context, tx pgx.Tx, platformID string) ([]managedplatform.CapacityReservation, string, error) {
	rows, err := tx.Query(ctx, `SELECT capacity_pool,reservation_key,node_name,cpu_milli,memory_bytes,storage_gib FROM managed_platform_capacity_reservations WHERE platform_id=$1 ORDER BY reservation_key,node_name FOR UPDATE`, platformID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	reservations := make([]managedplatform.CapacityReservation, 0)
	pool := ""
	for rows.Next() {
		var currentPool string
		var reservation managedplatform.CapacityReservation
		if err = rows.Scan(&currentPool, &reservation.Key, &reservation.NodeName, &reservation.Capacity.CPUMilli, &reservation.Capacity.MemoryBytes, &reservation.Capacity.StorageGiB); err != nil {
			return nil, "", err
		}
		if pool != "" && pool != currentPool {
			return nil, "", fmt.Errorf("managed platform capacity reservation pool changed")
		}
		pool = currentPool
		reservations = append(reservations, reservation)
		if len(reservations) > managedplatform.MaxCapacityReservations {
			return nil, "", fmt.Errorf("%w: managed platform capacity reservation inventory exceeds its bound", ErrConflict)
		}
	}
	return reservations, pool, rows.Err()
}

func mergeManagedPlatformReservations(old, next []managedplatform.CapacityReservation) []managedplatform.CapacityReservation {
	key := func(r managedplatform.CapacityReservation) string { return r.Key + "\x00" + r.NodeName }
	merged := make(map[string]managedplatform.CapacityReservation, len(old)+len(next))
	for _, reservation := range append(append([]managedplatform.CapacityReservation(nil), old...), next...) {
		id := key(reservation)
		current := merged[id]
		if current.Key == "" {
			merged[id] = reservation
			continue
		}
		current.Capacity.CPUMilli = max(current.Capacity.CPUMilli, reservation.Capacity.CPUMilli)
		current.Capacity.MemoryBytes = max(current.Capacity.MemoryBytes, reservation.Capacity.MemoryBytes)
		current.Capacity.StorageGiB = max(current.Capacity.StorageGiB, reservation.Capacity.StorageGiB)
		merged[id] = current
	}
	result := make([]managedplatform.CapacityReservation, 0, len(merged))
	for _, reservation := range merged {
		result = append(result, reservation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Key == result[j].Key {
			return result[i].NodeName < result[j].NodeName
		}
		return result[i].Key < result[j].Key
	})
	return result
}

func managedPlatformReservationsContained(retained, next []managedplatform.CapacityReservation) bool {
	key := func(r managedplatform.CapacityReservation) string { return r.Key + "\x00" + r.NodeName }
	envelope := make(map[string]managedplatform.Capacity, len(retained))
	for _, reservation := range retained {
		envelope[key(reservation)] = reservation.Capacity
	}
	for _, reservation := range next {
		capacity, ok := envelope[key(reservation)]
		if !ok || !reservation.Capacity.Fits(capacity) {
			return false
		}
	}
	return true
}

func platformCapacityUsed(ctx context.Context, tx pgx.Tx, project, environment, except string) (managedplatform.Capacity, error) {
	var used managedplatform.Capacity
	err := tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_cpu_milli),0),COALESCE(sum(reserved_memory_bytes),0),COALESCE(sum(reserved_storage_gib),0) FROM managed_platforms WHERE project=$1 AND environment=$2 AND id<>$3 AND deleted_at IS NULL`, project, environment, except).Scan(&used.CPUMilli, &used.MemoryBytes, &used.StorageGiB)
	return used, err
}

func platformNodeCapacityUsed(ctx context.Context, tx pgx.Tx, project, environment, except string) (map[string]managedplatform.Capacity, error) {
	rows, err := tx.Query(ctx, `SELECT r.node_name,COALESCE(sum(r.cpu_milli),0),COALESCE(sum(r.memory_bytes),0),COALESCE(sum(r.storage_gib),0) FROM managed_platform_capacity_reservations r JOIN managed_platforms p ON p.id=r.platform_id WHERE p.project=$1 AND p.environment=$2 AND p.id<>$3 AND p.deleted_at IS NULL GROUP BY r.node_name ORDER BY r.node_name`, project, environment, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]managedplatform.Capacity{}
	for rows.Next() {
		var name string
		var capacity managedplatform.Capacity
		if err = rows.Scan(&name, &capacity.CPUMilli, &capacity.MemoryBytes, &capacity.StorageGiB); err != nil {
			return nil, err
		}
		result[name] = capacity
	}
	return result, rows.Err()
}

func platformPoolCapacityUsed(ctx context.Context, tx pgx.Tx, pool, except string) (managedplatform.Capacity, map[string]managedplatform.Capacity, error) {
	rows, err := tx.Query(ctx, `SELECT r.node_name,COALESCE(sum(r.cpu_milli),0),COALESCE(sum(r.memory_bytes),0),COALESCE(sum(r.storage_gib),0) FROM managed_platform_capacity_reservations r JOIN managed_platforms p ON p.id=r.platform_id WHERE r.capacity_pool=$1 AND p.id<>$2 AND p.deleted_at IS NULL GROUP BY r.node_name ORDER BY r.node_name LIMIT 49`, pool, except)
	if err != nil {
		return managedplatform.Capacity{}, nil, err
	}
	defer rows.Close()
	total := managedplatform.Capacity{}
	byNode := map[string]managedplatform.Capacity{}
	for rows.Next() {
		var node string
		var capacity managedplatform.Capacity
		if err = rows.Scan(&node, &capacity.CPUMilli, &capacity.MemoryBytes, &capacity.StorageGiB); err != nil {
			return managedplatform.Capacity{}, nil, err
		}
		byNode[node] = capacity
		total = total.Add(capacity)
		if len(byNode) > 48 {
			return managedplatform.Capacity{}, nil, fmt.Errorf("managed platform capacity pool node inventory exceeds its bound")
		}
	}
	return total, byNode, rows.Err()
}

func reservationsByNode(reservations []managedplatform.CapacityReservation) map[string]managedplatform.Capacity {
	result := map[string]managedplatform.Capacity{}
	for _, reservation := range reservations {
		result[reservation.NodeName] = result[reservation.NodeName].Add(reservation.Capacity)
	}
	return result
}

func maxNodeCapacity(nodes map[string]managedplatform.Capacity) managedplatform.Capacity {
	result := managedplatform.Capacity{}
	for _, capacity := range nodes {
		result.CPUMilli = max(result.CPUMilli, capacity.CPUMilli)
		result.MemoryBytes = max(result.MemoryBytes, capacity.MemoryBytes)
	}
	return result
}

func validateManagedCapacityPoolUsage(policy managedplatform.CapacityPolicy, applications managedplatform.Capacity, databaseCPU, databaseMemory, databaseStorage int64, platforms managedplatform.Capacity, nodes map[string]managedplatform.Capacity) error {
	allowed := make(map[string]bool, len(policy.Nodes))
	for _, node := range policy.Nodes {
		allowed[node.Name] = true
	}
	for node, used := range nodes {
		if !allowed[node] {
			return fmt.Errorf("managed platform capacity pool uses an unapproved node")
		}
		used.CPUMilli += databaseCPU + applications.CPUMilli
		used.MemoryBytes += databaseMemory + applications.MemoryBytes
		if used.CPUMilli > policy.Capacity.CPUMilli || used.MemoryBytes > policy.Capacity.MemoryBytes {
			return fmt.Errorf("%w: applications, databases and managed platforms exceed the reserved node CPU or memory allocation", ErrConflict)
		}
	}
	if len(nodes) == 0 && (databaseCPU+applications.CPUMilli > policy.Capacity.CPUMilli || databaseMemory+applications.MemoryBytes > policy.Capacity.MemoryBytes) {
		return fmt.Errorf("%w: applications and databases exceed the reserved CPU or memory allocation", ErrConflict)
	}
	if applications.StorageGiB+databaseStorage+platforms.StorageGiB > policy.Capacity.StorageGiB {
		return fmt.Errorf("%w: applications, databases and managed platforms exceed the reserved storage allocation", ErrConflict)
	}
	return nil
}

func (s *Store) reserveManagedPlatformCapacity(ctx context.Context, tx pgx.Tx, item ManagedPlatform, plan managedplatform.Plan, policy managedplatform.CapacityPolicy, kind string) error {
	if kind == "delete" {
		return nil
	}
	if err := policy.Allows(item.Spec, plan); err != nil {
		return err
	}
	pool, err := s.managedCapacityPoolTx(ctx, tx, item.Project, item.Environment)
	if err != nil {
		return err
	}
	if pool == "" || pool != policy.Pool {
		return fmt.Errorf("managed capacity scope pool changed")
	}
	if s.ValidateManagedPlatformCapacity != nil {
		if err := s.ValidateManagedPlatformCapacity(ctx, item.Project, item.Environment, policy); err != nil {
			return err
		}
	}
	next, err := managedplatform.CapacityReservations(item.Spec, plan)
	if err != nil {
		return err
	}
	old, oldPool, err := loadManagedPlatformReservations(ctx, tx, item.ID)
	if err != nil {
		return err
	}
	if len(old) == 0 && item.Revision > 1 {
		return fmt.Errorf("managed platform capacity reservation is missing for an existing platform")
	}
	if oldPool != "" && oldPool != policy.Pool {
		return fmt.Errorf("managed platform capacity reservation pool changed")
	}
	allowedNodes := map[string]bool{}
	for _, node := range policy.Nodes {
		allowedNodes[node.Name] = true
	}
	for _, reservation := range old {
		if !allowedNodes[reservation.NodeName] {
			return fmt.Errorf("existing managed platform reservation uses a node outside the current capacity policy")
		}
	}
	reserved := mergeManagedPlatformReservations(old, next)
	if len(reserved) > managedplatform.MaxCapacityReservations {
		return fmt.Errorf("%w: managed platform capacity reservation inventory exceeds its bound", ErrConflict)
	}
	databaseCPU, databaseMemory, databaseStorage, err := databasePoolCapacityUsed(ctx, tx, policy.Pool, "")
	if err != nil {
		return err
	}
	applications, err := applicationPoolCapacityUsed(ctx, tx, policy.Pool, "", nil)
	if err != nil {
		return err
	}
	otherPlatforms, otherNodes, err := platformPoolCapacityUsed(ctx, tx, policy.Pool, item.ID)
	if err != nil {
		return err
	}
	total := managedplatform.ReservationTotal(reserved)
	for node, amount := range reservationsByNode(reserved) {
		otherNodes[node] = otherNodes[node].Add(amount)
	}
	if err = validateManagedCapacityPoolUsage(policy, applications, databaseCPU, databaseMemory, databaseStorage, otherPlatforms.Add(total), otherNodes); err != nil {
		return err
	}
	if s.ComputeBudget != nil {
		limit, e := s.ComputeBudget(ctx, tx, item.Project, item.Environment)
		if e != nil {
			return e
		}
		used, e := allocationMemory(ctx, tx, item.Project, item.Environment, "", nil, "", item.ID)
		if e != nil {
			return e
		}
		if limit < 0 || limit > 0 && used+total.MemoryBytes+(256<<20) > limit {
			return fmt.Errorf("%w: applications, databases and managed platforms exceed the workspace memory allocation including recovery headroom", ErrConflict)
		}
	}
	if s.StorageBudget != nil || s.StorageBudgetTx != nil {
		limit, e := s.storageBudgetTx(ctx, tx, item.Project, item.Environment)
		if e != nil {
			return e
		}
		var applications int64
		if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(size_gib),0) FROM storage_reservations WHERE project=$1 AND environment=$2`, item.Project, item.Environment).Scan(&applications); e != nil {
			return e
		}
		_, _, scopeDatabases, e := databaseCapacityUsed(ctx, tx, item.Project, item.Environment, "")
		if e != nil {
			return e
		}
		scopePlatforms, e := platformCapacityUsed(ctx, tx, item.Project, item.Environment, item.ID)
		if e != nil {
			return e
		}
		if limit < 0 || limit > 0 && applications+scopeDatabases+scopePlatforms.StorageGiB+total.StorageGiB > limit {
			return fmt.Errorf("%w: application, database and managed platform storage exceed the workspace quota", ErrConflict)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM managed_platform_capacity_reservations WHERE platform_id=$1`, item.ID); err != nil {
		return err
	}
	for _, reservation := range reserved {
		if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_capacity_reservations(platform_id,capacity_pool,reservation_key,node_name,cpu_milli,memory_bytes,storage_gib) VALUES($1,$2,$3,$4,$5,$6,$7)`, item.ID, policy.Pool, reservation.Key, reservation.NodeName, reservation.Capacity.CPUMilli, reservation.Capacity.MemoryBytes, reservation.Capacity.StorageGiB); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE managed_platforms SET reserved_cpu_milli=$2,reserved_memory_bytes=$3,reserved_storage_gib=$4 WHERE id=$1`, item.ID, total.CPUMilli, total.MemoryBytes, total.StorageGiB)
	return err
}

func (s *Store) contractManagedPlatformCapacity(ctx context.Context, tx pgx.Tx, op ManagedPlatformOperation) error {
	if op.Kind == "delete" {
		if _, err := tx.Exec(ctx, `DELETE FROM managed_platform_capacity_reservations WHERE platform_id=$1`, op.PlatformID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE managed_platforms SET reserved_cpu_milli=0,reserved_memory_bytes=0,reserved_storage_gib=0 WHERE id=$1`, op.PlatformID)
		return err
	}
	retained, pool, err := loadManagedPlatformReservations(ctx, tx, op.PlatformID)
	if err != nil {
		return err
	}
	if pool == "" {
		return fmt.Errorf("managed platform capacity reservation pool is unavailable")
	}
	reservations, err := managedplatform.CapacityReservations(op.Spec, op.Plan)
	if err != nil {
		return err
	}
	if !managedPlatformReservationsContained(retained, reservations) {
		return fmt.Errorf("%w: completed managed platform reservation is not contained by its admitted envelope", ErrConflict)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM managed_platform_capacity_reservations WHERE platform_id=$1`, op.PlatformID); err != nil {
		return err
	}
	for _, reservation := range reservations {
		if _, err = tx.Exec(ctx, `INSERT INTO managed_platform_capacity_reservations(platform_id,capacity_pool,reservation_key,node_name,cpu_milli,memory_bytes,storage_gib) VALUES($1,$2,$3,$4,$5,$6,$7)`, op.PlatformID, pool, reservation.Key, reservation.NodeName, reservation.Capacity.CPUMilli, reservation.Capacity.MemoryBytes, reservation.Capacity.StorageGiB); err != nil {
			return err
		}
	}
	total := managedplatform.ReservationTotal(reservations)
	_, err = tx.Exec(ctx, `UPDATE managed_platforms SET reserved_cpu_milli=$2,reserved_memory_bytes=$3,reserved_storage_gib=$4 WHERE id=$1`, op.PlatformID, total.CPUMilli, total.MemoryBytes, total.StorageGiB)
	return err
}

// CheckManagedPlatformCapacityReservation fails startup when a live platform
// has no durable reservation or the current trusted grant no longer covers it.
func (s *Store) checkManagedPlatformCapacityReservationTx(ctx context.Context, tx pgx.Tx, project, environment string) error {
	policy, _, err := s.managedPlatformCapacityPolicyTx(ctx, tx, project, environment)
	if err != nil {
		return err
	}
	if err = lockManagedCapacityPool(ctx, tx, project, environment, policy.Pool); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT p.id,p.reserved_cpu_milli,p.reserved_memory_bytes,p.reserved_storage_gib,COALESCE(sum(r.cpu_milli),0),COALESCE(sum(r.memory_bytes),0),COALESCE(sum(r.storage_gib),0),count(r.platform_id),COALESCE(min(r.capacity_pool),''),COALESCE(max(r.capacity_pool),'') FROM managed_platforms p LEFT JOIN managed_platform_capacity_reservations r ON r.platform_id=p.id WHERE p.project=$1 AND p.environment=$2 AND p.deleted_at IS NULL GROUP BY p.id ORDER BY p.id LIMIT 65`, project, environment)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		count++
		var id string
		var stored, actual managedplatform.Capacity
		var reservations int
		var minPool, maxPool string
		if err = rows.Scan(&id, &stored.CPUMilli, &stored.MemoryBytes, &stored.StorageGiB, &actual.CPUMilli, &actual.MemoryBytes, &actual.StorageGiB, &reservations, &minPool, &maxPool); err != nil {
			rows.Close()
			return err
		}
		if reservations < 1 || minPool != policy.Pool || maxPool != policy.Pool || stored != actual || stored.CPUMilli < 1 || stored.MemoryBytes < 1 || stored.StorageGiB < 1 {
			rows.Close()
			return fmt.Errorf("managed platform %s has no valid durable capacity reservation", id)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if count > MaxManagedPlatforms {
		return fmt.Errorf("managed platform capacity inventory exceeds its bound")
	}
	databaseCPU, databaseMemory, databaseStorage, err := databasePoolCapacityUsed(ctx, tx, policy.Pool, "")
	if err != nil {
		return err
	}
	applications, err := applicationPoolCapacityUsed(ctx, tx, policy.Pool, "", nil)
	if err != nil {
		return err
	}
	platforms, nodes, err := platformPoolCapacityUsed(ctx, tx, policy.Pool, "")
	if err != nil {
		return err
	}
	if err = validateManagedCapacityPoolUsage(policy, applications, databaseCPU, databaseMemory, databaseStorage, platforms, nodes); err != nil {
		return err
	}
	if s.ValidateManagedPlatformCapacity != nil {
		return s.ValidateManagedPlatformCapacity(ctx, project, environment, policy)
	}
	return nil
}

func (s *Store) CheckManagedPlatformCapacityReservation(ctx context.Context, project, environment string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var scope string
	if err = tx.QueryRow(ctx, `SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE`, project, environment).Scan(&scope); err != nil {
		return err
	}
	if err = s.checkManagedPlatformCapacityReservationTx(ctx, tx, project, environment); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CheckManagedPlatformOperationCapacity(ctx context.Context, platformID string) error {
	var project, environment string
	if err := s.Pool.QueryRow(ctx, `SELECT project,environment FROM managed_platforms WHERE id=$1 AND deleted_at IS NULL`, platformID).Scan(&project, &environment); err != nil {
		return err
	}
	return s.CheckManagedPlatformCapacityReservation(ctx, project, environment)
}

func (s *Store) ReconcileManagedCapacityScopes(ctx context.Context) error {
	if s.ManagedCapacityPool == nil {
		var bound bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_capacity_scopes)`).Scan(&bound); err != nil {
			return err
		}
		if bound {
			return fmt.Errorf("managed capacity pool policy is unavailable for durable scope bindings")
		}
		return nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT project,environment FROM (
		SELECT project,environment FROM managed_capacity_scopes
		UNION SELECT project,environment FROM applications
		UNION SELECT project,environment FROM managed_databases WHERE deleted_at IS NULL
		UNION SELECT project,environment FROM managed_platforms WHERE deleted_at IS NULL
		UNION SELECT project,environment FROM storage_reservations
	) scopes ORDER BY project,environment LIMIT 1001`)
	if err != nil {
		return err
	}
	type scope struct{ project, environment string }
	var scopes []scope
	for rows.Next() {
		var current scope
		if err = rows.Scan(&current.project, &current.environment); err != nil {
			rows.Close()
			return err
		}
		scopes = append(scopes, current)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(scopes) > 1000 {
		return fmt.Errorf("managed capacity scope inventory exceeds its bound")
	}
	policies := map[string]managedplatform.CapacityPolicy{}
	fingerprints := map[string][32]byte{}
	for _, current := range scopes {
		tx, beginErr := s.Pool.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		var environment string
		if beginErr = tx.QueryRow(ctx, `SELECT name FROM environments WHERE project=$1 AND name=$2 FOR UPDATE`, current.project, current.environment).Scan(&environment); beginErr == nil {
			var pool string
			pool, beginErr = s.managedCapacityPoolTx(ctx, tx, current.project, current.environment)
			if beginErr == nil && pool != "" {
				var policy managedplatform.CapacityPolicy
				var fingerprint [32]byte
				policy, fingerprint, beginErr = s.managedPlatformCapacityPolicyTx(ctx, tx, current.project, current.environment)
				if beginErr == nil && policy.Pool != pool {
					beginErr = fmt.Errorf("managed capacity scope pool changed")
				}
				if beginErr == nil {
					if previous, ok := fingerprints[pool]; ok && previous != fingerprint {
						beginErr = fmt.Errorf("managed capacity pool has conflicting policies")
					} else {
						fingerprints[pool], policies[pool] = fingerprint, policy
					}
				}
			}
		}
		if beginErr == nil {
			beginErr = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if beginErr != nil {
			return beginErr
		}
	}
	for pool, policy := range policies {
		tx, beginErr := s.Pool.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		if beginErr = lockManagedCapacityPoolID(ctx, tx, pool); beginErr != nil {
			tx.Rollback(ctx)
			return beginErr
		}
		applications, checkErr := applicationPoolCapacityUsed(ctx, tx, pool, "", nil)
		databaseCPU, databaseMemory, databaseStorage := int64(0), int64(0), int64(0)
		if checkErr == nil {
			databaseCPU, databaseMemory, databaseStorage, checkErr = databasePoolCapacityUsed(ctx, tx, pool, "")
		}
		platforms, nodes := managedplatform.Capacity{}, map[string]managedplatform.Capacity{}
		if checkErr == nil {
			platforms, nodes, checkErr = platformPoolCapacityUsed(ctx, tx, pool, "")
		}
		if checkErr == nil {
			checkErr = validateManagedCapacityPoolUsage(policy, applications, databaseCPU, databaseMemory, databaseStorage, platforms, nodes)
		}
		if checkErr == nil {
			checkErr = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if checkErr != nil {
			return checkErr
		}
	}
	return nil
}

// ManagedPlatformNamespaceReservations returns only namespace and workload
// controller UIDs confirmed by reconciliation. Namespace labels alone never
// make a pod part of the reserved platform envelope.
func (s *Store) ManagedPlatformNamespaceReservations(ctx context.Context, project, environment string) (map[string]managedplatform.CapacityNamespaceOwnership, error) {
	limit := MaxManagedPlatforms*MaxManagedPlatformResources + 1
	rows, err := s.Pool.Query(ctx, `SELECT p.id,p.name,p.kind,r.component,r.resource_id
		FROM managed_platforms p JOIN platform_component_resources r ON r.platform_id=p.id
		WHERE p.project=$1 AND p.environment=$2 AND p.deleted_at IS NULL
		AND r.resource_kind='runtime_component' AND r.released_at IS NULL
		AND (r.component='namespace.'||'managed-platform-'||p.id OR r.component LIKE 'deployment.%' OR r.component LIKE 'statefulset.%')
		ORDER BY p.id,r.component LIMIT $3`, project, environment, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]managedplatform.CapacityNamespaceOwnership{}
	platformKinds := map[string]string{}
	count := 0
	for rows.Next() {
		count++
		if count >= limit {
			return nil, fmt.Errorf("managed platform workload ownership inventory exceeds its bound")
		}
		var id, name, kind, component, uid string
		if err = rows.Scan(&id, &name, &kind, &component, &uid); err != nil {
			return nil, err
		}
		platformKinds[id] = kind
		namespace := "managed-platform-" + id
		owned := result[namespace]
		if owned.PlatformID == "" {
			owned = managedplatform.CapacityNamespaceOwnership{PlatformID: id, PlatformName: name, Controllers: map[string]string{}, Workloads: map[string]managedplatform.CapacityWorkloadOwnership{}}
		}
		if component == "namespace."+namespace {
			if owned.UID != "" && owned.UID != uid {
				return nil, fmt.Errorf("managed platform namespace ownership changed")
			}
			owned.UID = uid
		} else {
			if previous := owned.Controllers[component]; previous != "" && previous != uid {
				return nil, fmt.Errorf("managed platform workload controller ownership changed")
			}
			owned.Controllers[component] = uid
		}
		result[namespace] = owned
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	reservationLimit := MaxManagedPlatforms*managedplatform.MaxCapacityReservations + 1
	reservations, err := s.Pool.Query(ctx, `SELECT p.id,r.reservation_key,r.node_name,r.cpu_milli,r.memory_bytes,r.storage_gib
		FROM managed_platforms p JOIN managed_platform_capacity_reservations r ON r.platform_id=p.id
		WHERE p.project=$1 AND p.environment=$2 AND p.deleted_at IS NULL AND r.reservation_key LIKE 'workload/%'
		ORDER BY p.id,r.reservation_key,r.node_name LIMIT $3`, project, environment, reservationLimit)
	if err != nil {
		return nil, err
	}
	defer reservations.Close()
	count = 0
	for reservations.Next() {
		count++
		if count >= reservationLimit {
			return nil, fmt.Errorf("managed platform workload reservation inventory exceeds its bound")
		}
		var id, key, node string
		var capacity managedplatform.Capacity
		if err = reservations.Scan(&id, &key, &node, &capacity.CPUMilli, &capacity.MemoryBytes, &capacity.StorageGiB); err != nil {
			return nil, err
		}
		controller, ok := managedPlatformCapacityController(platformKinds[id], key)
		if !ok {
			return nil, fmt.Errorf("managed platform workload reservation is invalid")
		}
		namespace := "managed-platform-" + id
		owned := result[namespace]
		uid := owned.Controllers[controller]
		if uid == "" {
			continue
		}
		workload := owned.Workloads[controller]
		if workload.UID == "" {
			workload = managedplatform.CapacityWorkloadOwnership{UID: uid, Nodes: map[string]managedplatform.Capacity{}}
		}
		if workload.UID != uid || workload.Nodes[node] != (managedplatform.Capacity{}) {
			return nil, fmt.Errorf("managed platform workload reservation identity changed")
		}
		workload.Nodes[node] = capacity
		owned.Workloads[controller] = workload
		result[namespace] = owned
	}
	if err = reservations.Err(); err != nil {
		return nil, err
	}
	for namespace, owned := range result {
		if owned.UID == "" {
			delete(result, namespace)
		}
	}
	if len(result) > MaxManagedPlatforms {
		return nil, fmt.Errorf("managed platform namespace reservation inventory exceeds its bound")
	}
	return result, nil
}

func managedPlatformCapacityController(kind, key string) (string, bool) {
	workload := strings.TrimPrefix(key, "workload/")
	if workload == key || workload == "" {
		return "", false
	}
	parts := strings.Split(workload, "/")
	switch kind {
	case "supabase":
		if len(parts) != 1 {
			return "", false
		}
		resourceKind := "deployment"
		if parts[0] == "database" {
			resourceKind = "statefulset"
		}
		return resourceKind + ".supabase-" + parts[0], true
	case "neon":
		if len(parts) != 2 {
			return "", false
		}
		name := "neon-" + parts[0]
		resourceKind := "deployment"
		if parts[0] == "pageserver" || parts[0] == "safekeeper" || parts[0] == "compute" {
			name += "-" + parts[1]
			resourceKind = "statefulset"
		} else if parts[0] == "controller-database" {
			resourceKind = "statefulset"
		} else if parts[1] != "0" {
			return "", false
		}
		return resourceKind + "." + name, true
	default:
		return "", false
	}
}

func (s *Store) ManagedPlatformCapacityScopes(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT project,environment FROM managed_platforms WHERE deleted_at IS NULL ORDER BY project,environment LIMIT $1`, maxManagedPlatformCapacityScopes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scopes []string
	for rows.Next() {
		var project, environment string
		if err = rows.Scan(&project, &environment); err != nil {
			return nil, err
		}
		scopes = append(scopes, project+"/"+environment)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(scopes) > maxManagedPlatformCapacityScopes {
		return nil, fmt.Errorf("managed platform capacity scope inventory exceeds its bound")
	}
	return scopes, nil
}

// ManagedCapacityPoolOwnership returns every active application, database and
// reconciled managed-platform namespace whose durable reservation is included
// in one shared capacity pool.
func (s *Store) ManagedCapacityPoolOwnership(ctx context.Context, pool string) (managedplatform.CapacityPoolOwnership, error) {
	var result managedplatform.CapacityPoolOwnership
	if len(validation.IsDNS1123Label(pool)) != 0 {
		return result, fmt.Errorf("managed capacity pool is invalid")
	}
	rows, err := s.Pool.Query(ctx, `SELECT kind,id,project,environment FROM (
		SELECT 'application' AS kind,a.id,a.project,a.environment FROM applications a
		JOIN managed_capacity_scopes s ON s.project=a.project AND s.environment=a.environment WHERE s.capacity_pool=$1
		UNION ALL
		SELECT 'database' AS kind,d.id,d.project,d.environment FROM managed_databases d
		JOIN managed_capacity_scopes s ON s.project=d.project AND s.environment=d.environment WHERE s.capacity_pool=$1 AND d.deleted_at IS NULL
	) workloads ORDER BY kind,id LIMIT $2`, pool, managedplatform.MaxCapacityPoolWorkloads+1)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var workload managedplatform.CapacityPoolWorkload
		if err = rows.Scan(&workload.Kind, &workload.ID, &workload.Project, &workload.Environment); err != nil {
			rows.Close()
			return result, err
		}
		result.Workloads = append(result.Workloads, workload)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Workloads) > managedplatform.MaxCapacityPoolWorkloads {
		return result, fmt.Errorf("managed capacity pool workload inventory exceeds its bound")
	}
	platformScopes, err := s.ManagedCapacityPoolPlatformScopes(ctx, pool)
	if err != nil {
		return result, err
	}
	result.PlatformNamespaces = map[string]managedplatform.CapacityNamespaceOwnership{}
	for _, scope := range platformScopes {
		project, environment, _ := strings.Cut(scope, "/")
		namespaces, ownershipErr := s.ManagedPlatformNamespaceReservations(ctx, project, environment)
		if ownershipErr != nil {
			return result, ownershipErr
		}
		for namespace, ownership := range namespaces {
			if _, exists := result.PlatformNamespaces[namespace]; exists {
				return result, fmt.Errorf("managed platform namespace belongs to more than one capacity scope")
			}
			result.PlatformNamespaces[namespace] = ownership
			if len(result.PlatformNamespaces) > managedplatform.MaxCapacityPoolWorkloads {
				return result, fmt.Errorf("managed capacity pool platform namespace inventory exceeds its bound")
			}
		}
	}
	return result, nil
}

func (s *Store) ManagedCapacityPoolPlatformScopes(ctx context.Context, pool string) ([]string, error) {
	if len(validation.IsDNS1123Label(pool)) != 0 {
		return nil, fmt.Errorf("managed capacity pool is invalid")
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT p.project,p.environment FROM managed_platforms p
		JOIN managed_capacity_scopes s ON s.project=p.project AND s.environment=p.environment
		WHERE s.capacity_pool=$1 AND p.deleted_at IS NULL ORDER BY p.project,p.environment LIMIT $2`, pool, maxManagedPlatformCapacityScopes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var project, environment string
		if err = rows.Scan(&project, &environment); err != nil {
			return nil, err
		}
		result = append(result, project+"/"+environment)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > maxManagedPlatformCapacityScopes {
		return nil, fmt.Errorf("managed platform capacity scope inventory exceeds its bound")
	}
	return result, nil
}

func (s *Store) ManagedCapacityScopePool(ctx context.Context, project, environment string) (string, error) {
	var pool string
	err := s.Pool.QueryRow(ctx, `SELECT capacity_pool FROM managed_capacity_scopes WHERE project=$1 AND environment=$2`, project, environment).Scan(&pool)
	return pool, err
}

func (s *Store) HasManagedPlatforms(ctx context.Context, project, environment string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_platforms WHERE project=$1 AND environment=$2 AND deleted_at IS NULL)`, project, environment).Scan(&exists)
	return exists, err
}

func capacityFingerprintHex(fingerprint [32]byte) string { return hex.EncodeToString(fingerprint[:]) }
