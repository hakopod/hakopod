package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
)

func recordDatabaseFailures(ctx context.Context, tx pgx.Tx, id string, revision int64, observedAt time.Time, failures []database.FailureEvidence) error {
	if len(failures) > database.MaxFailureEvidencePerObservation {
		return ErrInput
	}
	for _, failure := range failures {
		if failure.Code == "" || failure.OccurredAt.IsZero() || failure.ObservedAt.IsZero() || failure.Revision != revision || !failure.ObservedAt.Equal(observedAt) || failure.Source == "" {
			return ErrInput
		}
		identity, err := json.Marshal(struct {
			Code       string    `json:"code"`
			MemberUID  string    `json:"member_uid"`
			Container  string    `json:"container"`
			OccurredAt time.Time `json:"occurred_at"`
		}{failure.Code, failure.MemberUID, failure.Container, failure.OccurredAt.UTC()})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(identity)
		fingerprint := hex.EncodeToString(digest[:])
		if _, err = tx.Exec(ctx, `INSERT INTO managed_database_failure_evidence(database_id,fingerprint,evidence,first_seen_at,last_seen_at) VALUES($1,$2,$3,$4,$4)
			ON CONFLICT(database_id,fingerprint) DO UPDATE SET evidence=EXCLUDED.evidence,last_seen_at=GREATEST(managed_database_failure_evidence.last_seen_at,EXCLUDED.last_seen_at)`, id, fingerprint, JSON(failure), failure.ObservedAt); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM managed_database_failure_evidence WHERE database_id=$1 AND fingerprint IN (
		SELECT fingerprint FROM managed_database_failure_evidence WHERE database_id=$1 ORDER BY last_seen_at DESC,fingerprint DESC OFFSET $2
	)`, id, database.MaxFailureHistory)
	return err
}

func (s *Store) DatabaseFailureHistory(ctx context.Context, p Principal, id string) (database.FailureHistory, error) {
	if _, err := s.Database(ctx, p, id, false); err != nil {
		return database.FailureHistory{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT evidence,first_seen_at,last_seen_at FROM managed_database_failure_evidence WHERE database_id=$1 ORDER BY last_seen_at DESC,fingerprint DESC LIMIT $2`, id, database.MaxFailureHistory)
	if err != nil {
		return database.FailureHistory{}, err
	}
	defer rows.Close()
	history := database.FailureHistory{Items: []database.FailureRecord{}, Limit: database.MaxFailureHistory}
	for rows.Next() {
		var item database.FailureRecord
		if err = rows.Scan(&item.FailureEvidence, &item.FirstSeenAt, &item.LastSeenAt); err != nil {
			return database.FailureHistory{}, err
		}
		history.Items = append(history.Items, item)
	}
	return history, rows.Err()
}
