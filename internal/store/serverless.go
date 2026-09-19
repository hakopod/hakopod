package store

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// Admission bounds the complete gateway table. Otherwise an accepted 513th
// service would make a bounded route refresh fail for every existing tenant.
func validateServerlessCapacity(ctx context.Context, tx pgx.Tx, applicationID string, next spec.Application) error {
	count := 0
	for _, svc := range next.Services {
		if svc.Serverless != nil {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('hakopod.serverless.capacity',0))"); err != nil {
		return err
	}
	var existing int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM applications a CROSS JOIN LATERAL jsonb_each(a.spec->'services') s WHERE a.id<>$1 AND s.value->'serverless' IS NOT NULL AND s.value->'serverless'<>'null'::jsonb`, applicationID).Scan(&existing)
	if err != nil {
		return err
	}
	if existing+count > 512 {
		return fmt.Errorf("the installation supports at most 512 serverless HTTP services; remove an unused function or use an always-running service")
	}
	return nil
}
