package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/license"
	"github.com/jackc/pgx/v5"
)

var ErrLicenseRequired = errors.New("a valid paid license is required")

type LicenseStatus struct {
	InstallationID   string            `json:"installation_id"`
	Revision         int64             `json:"revision"`
	Plan             string            `json:"plan"`
	State            string            `json:"state"`
	Valid            bool              `json:"valid"`
	IssuerConfigured bool              `json:"issuer_configured"`
	LicensedTo       string            `json:"licensed_to"`
	LicenseID        string            `json:"license_id"`
	ExpiresAt        *time.Time        `json:"expires_at"`
	Sequence         int64             `json:"sequence"`
	Features         []string          `json:"features"`
	Catalog          []license.Feature `json:"catalog"`
}
type licenseRecord struct {
	InstallationID, Token string
	Revision, Sequence    int64
	Digest                []byte
}
type licenseQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) licenseVerifier() *license.Verifier {
	if s.LicenseVerifier != nil {
		return s.LicenseVerifier
	}
	return license.ReleaseVerifier()
}
func readLicense(ctx context.Context, q licenseQuerier, lock string) (licenseRecord, error) {
	var record licenseRecord
	err := q.QueryRow(ctx, "SELECT installation_id,token,revision,highest_sequence,token_digest FROM installation_license WHERE singleton"+lock).Scan(&record.InstallationID, &record.Token, &record.Revision, &record.Sequence, &record.Digest)
	return record, err
}
func (s *Store) licenseStatus(record licenseRecord) LicenseStatus {
	v := s.licenseVerifier()
	status := LicenseStatus{InstallationID: record.InstallationID, Revision: record.Revision, Plan: "free", State: "free", IssuerConfigured: v.Configured(), Sequence: record.Sequence, Features: []string{}}
	if record.Token != "" {
		digest := sha256.Sum256([]byte(record.Token))
		claims, err := v.Verify(record.Token, record.InstallationID, time.Now().UTC())
		if err != nil {
			status.State = license.State(err)
		} else if claims.Sequence != record.Sequence || !bytes.Equal(record.Digest, digest[:]) {
			status.State = "rollback_rejected"
		} else {
			status.State = "active"
			status.Valid = true
			status.Plan = claims.Plan
			status.Features = append(status.Features, claims.Features...)
		}
		if claims.LicenseID != "" {
			status.LicenseID = claims.LicenseID
			status.LicensedTo = claims.Customer
			expiry := time.Unix(claims.ExpiresAt, 0).UTC()
			status.ExpiresAt = &expiry
		}
	} else if record.Sequence > 0 {
		status.State = "inactive"
	}
	status.Catalog = license.Catalog(status.Features)
	for _, feature := range status.Catalog {
		if feature.Plan == "free" {
			status.Features = append(status.Features, feature.ID)
		}
	}
	return status
}
func (s *Store) LicenseStatus(ctx context.Context) (LicenseStatus, error) {
	record, err := readLicense(ctx, s.Pool, "")
	if err != nil {
		return LicenseStatus{}, err
	}
	return s.licenseStatus(record), nil
}
func licenseAllows(status LicenseStatus, features ...string) bool {
	for _, feature := range features {
		if !contains(status.Features, feature) {
			return false
		}
	}
	return true
}
func (s *Store) RequireFeatures(ctx context.Context, features ...string) error {
	status, err := s.LicenseStatus(ctx)
	if err != nil {
		return err
	}
	if !licenseAllows(status, features...) {
		return fmt.Errorf("%w for %v", ErrLicenseRequired, features)
	}
	return nil
}
func (s *Store) requireFeaturesTx(ctx context.Context, tx pgx.Tx, features ...string) error {
	// Shared locks keep activation/removal atomic with paid-feature mutations.
	record, err := readLicense(ctx, tx, " FOR SHARE")
	if err != nil {
		return err
	}
	if !licenseAllows(s.licenseStatus(record), features...) {
		return fmt.Errorf("%w for %v", ErrLicenseRequired, features)
	}
	return nil
}
func (s *Store) InstallLicense(ctx context.Context, p Principal, token string, expected int64) (LicenseStatus, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return LicenseStatus{}, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return LicenseStatus{}, err
	}
	defer tx.Rollback(ctx)
	record, err := readLicense(ctx, tx, " FOR UPDATE")
	if err != nil {
		return LicenseStatus{}, err
	}
	if expected != record.Revision {
		return LicenseStatus{}, ErrConflict
	}
	claims, err := s.licenseVerifier().Verify(token, record.InstallationID, time.Now().UTC())
	if err != nil {
		return LicenseStatus{}, fmt.Errorf("%w: %s", ErrInput, err)
	}
	digest := sha256.Sum256([]byte(token))
	if claims.Sequence < record.Sequence || (claims.Sequence == record.Sequence && (token != record.Token || !bytes.Equal(record.Digest, digest[:]))) {
		return LicenseStatus{}, fmt.Errorf("%w: a newer signed license sequence is required", ErrConflict)
	}
	if token == record.Token {
		return s.licenseStatus(record), nil
	}
	_, err = tx.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=$3,revision=revision+1,updated_at=now() WHERE singleton", token, digest[:], claims.Sequence)
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'license.activate',$3,$4)", p.ID, p.KeyID, claims.LicenseID, JSON(map[string]any{"plan": claims.Plan, "sequence": claims.Sequence, "key_id": claims.KeyID}))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return LicenseStatus{}, err
	}
	return s.LicenseStatus(ctx)
}
func (s *Store) RemoveLicense(ctx context.Context, p Principal, expected int64) (LicenseStatus, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return LicenseStatus{}, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return LicenseStatus{}, err
	}
	defer tx.Rollback(ctx)
	record, err := readLicense(ctx, tx, " FOR UPDATE")
	if err != nil {
		return LicenseStatus{}, err
	}
	if expected != record.Revision {
		return LicenseStatus{}, ErrConflict
	}
	_, err = tx.Exec(ctx, "UPDATE installation_license SET token='',revision=revision+1,updated_at=now() WHERE singleton")
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'license.remove',$3)", p.ID, p.KeyID, record.InstallationID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return LicenseStatus{}, err
	}
	return s.LicenseStatus(ctx)
}
