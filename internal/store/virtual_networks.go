package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type VirtualNetworkMetadata struct {
	ID   string              `json:"id"`
	Spec spec.VirtualNetwork `json:"spec"`
}

func (p Principal) CanManageVirtualNetworks(project, environment string) bool {
	return p.CanManageProject(project) && p.Allows("deployments:write", project, environment, "")
}

type networkReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ResolveVirtualNetworks returns only authorized runtime identities. A deleted
// and recreated network receives a new identity, so old pods cannot rejoin it.
func (s *Store) ResolveVirtualNetworks(ctx context.Context, project, environment string, app spec.Application) (map[string]string, error) {
	return resolveVirtualNetworks(ctx, s.Pool, project, environment, app)
}

func resolveVirtualNetworks(ctx context.Context, db networkReader, project, environment string, app spec.Application) (map[string]string, error) {
	result := map[string]string{}
	loaded := map[string]VirtualNetworkMetadata{}
	for local, network := range app.Networks {
		if network.VirtualNetwork == "" {
			continue
		}
		value, exists := loaded[network.VirtualNetwork]
		if !exists {
			var raw json.RawMessage
			err := db.QueryRow(ctx, "SELECT metadata FROM runtime_resources WHERE kind='virtual-network' AND project=$1 AND environment=$2 AND name=$3", project, environment, network.VirtualNetwork).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("virtual network %s is not configured in this project/environment", network.VirtualNetwork)
			}
			if err != nil {
				return nil, err
			}
			if json.Unmarshal(raw, &value) != nil || value.ID == "" {
				return nil, errors.New("virtual network configuration is unavailable")
			}
			loaded[network.VirtualNetwork] = value
		}
		if !value.Spec.Allows(network.Segment, app.Name) {
			return nil, fmt.Errorf("virtual network %s/%s does not allow this application; ask a project administrator to add it", network.VirtualNetwork, network.Segment)
		}
		result[local] = value.ID + "/" + network.Segment
	}
	return result, nil
}

func validateVirtualNetworkMetadata(metadata any, name string) (VirtualNetworkMetadata, error) {
	value, ok := metadata.(VirtualNetworkMetadata)
	if !ok || len(value.ID) != 32 {
		return value, errors.New("invalid virtual network identity")
	}
	var err error
	value.Spec, err = spec.NormalizeVirtualNetwork(value.Spec)
	if err != nil {
		return value, err
	}
	if value.Spec.Name != name {
		return value, errors.New("virtual network names must agree")
	}
	return value, nil
}

func (s *Store) CheckVirtualNetworkChange(ctx context.Context, project, environment string, next spec.VirtualNetwork) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	return validateVirtualNetworkChange(ctx, tx, project, environment, next.Name, &next)
}

func (s *Store) DeleteVirtualNetwork(ctx context.Context, p Principal, project, environment, name, expectedID string, expectedRevision int64) error {
	return s.deleteRuntimeResource(ctx, p, "virtual-network", project, environment, name, expectedRevision, expectedID)
}

// Running or failed deployments can have partially applied resources. Keep any
// grant from the last successful release onward. That release retired older
// pods and policies, even if a subsequent unrelated deployment fails.
func validateVirtualNetworkChange(ctx context.Context, tx pgx.Tx, project, environment, name string, next *spec.VirtualNetwork) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.name, n.value->>'segment'
 FROM applications a CROSS JOIN LATERAL (
   SELECT value FROM jsonb_each(COALESCE(a.spec->'networks','{}'::jsonb))
   UNION ALL
   SELECT n.value FROM deployments d CROSS JOIN LATERAL jsonb_each(COALESCE(d.spec->'networks','{}'::jsonb)) n
   WHERE d.application_id=a.id AND d.revision >= COALESCE((
     SELECT healthy.revision FROM deployments healthy
     WHERE healthy.application_id=a.id AND healthy.status='succeeded'
     ORDER BY healthy.revision DESC LIMIT 1
   ), 0)
 ) n WHERE a.project=$1 AND a.environment=$2 AND n.value->>'virtual_network'=$3
 LIMIT 3201`, project, environment, name)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var application, segment string
		if err := rows.Scan(&application, &segment); err != nil {
			return err
		}
		count++
		if count > 3200 {
			return errors.New("virtual network usage exceeds inspection bounds")
		}
		if next == nil || !next.Allows(segment, application) {
			return fmt.Errorf("%w: %s still uses segment %s; successfully deploy it without that connection before removing access", ErrConflict, application, segment)
		}
	}
	return rows.Err()
}
