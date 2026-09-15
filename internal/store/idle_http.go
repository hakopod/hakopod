package store

import "context"

// WithRuntimeApplication serializes ephemeral runtime scaling with configuration
// acceptance, stop/resume and deletion. The callback must finish promptly; do
// not hold the transaction while waiting for pods to start.
func (s *Store) WithRuntimeApplication(ctx context.Context, id string, change func(Application) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Use the reconciler's application lock as well: certificate maintenance
	// must not reapply a Deployment concurrently with an idle transition.
	var locked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,3))", id).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrConflict
	}
	app, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return err
	}
	if err = change(app); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
