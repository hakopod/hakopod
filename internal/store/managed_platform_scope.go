package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Store) ValidateManagedPlatformScope(ctx context.Context, principal Principal, project, environment string) error {
	if !principal.AllowsManagedPlatform(project, environment, false) {
		return ErrForbidden
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", project, environment).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	return nil
}
