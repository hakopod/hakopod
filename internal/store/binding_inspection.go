package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hakopod/hakopod/internal/bindingprobe"
	"github.com/jackc/pgx/v5"
)

// RecordBindingTest retains one bounded, redacted result per declared binding.
// Serializing on the application also orders writes against new revisions.
func (s *Store) RecordBindingTest(ctx context.Context, result bindingprobe.TestResult) error {
	if result.SchemaVersion != 1 || result.Revision < 1 || len(result.Stages) > 8 || len(JSON(result)) > 16384 || len(JSON(result.Evidence)) > 2048 {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM applications WHERE id=$1 FOR UPDATE", result.ApplicationID).Scan(&revision); err != nil {
		return err
	}
	if revision != result.Revision {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO binding_test_evidence(application_id,service,variable,revision,observed_at,result,runtime_evidence)
VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(application_id,service,variable) DO UPDATE
SET revision=EXCLUDED.revision,observed_at=EXCLUDED.observed_at,result=EXCLUDED.result,runtime_evidence=EXCLUDED.runtime_evidence
WHERE binding_test_evidence.observed_at <= EXCLUDED.observed_at`, result.ApplicationID, result.Service, result.Variable, result.Revision, result.ObservedAt, JSON(result), JSON(result.Evidence))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM binding_test_evidence WHERE application_id=$1 AND (service,variable) IN
(SELECT service,variable FROM binding_test_evidence WHERE application_id=$1 ORDER BY observed_at DESC,service,variable OFFSET 256)`, result.ApplicationID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LatestBindingTest(ctx context.Context, p Principal, appID, service, variable string) (*bindingprobe.TestResult, error) {
	a, err := s.Application(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !p.Allows("deployments:read", a.Project, a.Environment, a.Name) {
		return nil, ErrForbidden
	}
	if _, ok := a.Spec.Services[service].Bindings[variable]; !ok {
		return nil, pgx.ErrNoRows
	}
	var result, evidence []byte
	err = s.Pool.QueryRow(ctx, "SELECT result,runtime_evidence FROM binding_test_evidence WHERE application_id=$1 AND service=$2 AND variable=$3", appID, service, variable).Scan(&result, &evidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value bindingprobe.TestResult
	if err = json.Unmarshal(result, &value); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(evidence, &value.Evidence); err != nil {
		return nil, err
	}
	return &value, nil
}
