package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// KubernetesPatchSettings is the installation switch for raw Kubernetes
// patches. It is off unless an administrator turns it on; a missing row means off.
type KubernetesPatchSettings struct {
	Enabled bool `json:"enabled"`
}

var ErrKubernetesPatchesDisabled = errors.New("raw Kubernetes patches are turned off for this installation; an administrator can enable them in Settings")

type settingsQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func kubernetesPatchSettings(ctx context.Context, q settingsQuerier) (KubernetesPatchSettings, int64, error) {
	var v KubernetesPatchSettings
	var raw []byte
	var rev int64
	err := q.QueryRow(ctx, "SELECT value,revision FROM installation_settings WHERE name='kubernetes_patches'").Scan(&raw, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, 0, nil
	}
	if err != nil {
		return v, 0, err
	}
	return v, rev, json.Unmarshal(raw, &v)
}

func (s *Store) KubernetesPatchSettings(ctx context.Context) (KubernetesPatchSettings, int64, error) {
	return kubernetesPatchSettings(ctx, s.Pool)
}

func (s *Store) PutKubernetesPatchSettings(ctx context.Context, v KubernetesPatchSettings, expectedRevision int64) (int64, error) {
	var rev int64
	var err error
	if expectedRevision == 0 {
		err = s.Pool.QueryRow(ctx, "INSERT INTO installation_settings(name,value) VALUES('kubernetes_patches',$1) ON CONFLICT(name) DO NOTHING RETURNING revision", JSON(v)).Scan(&rev)
	} else {
		err = s.Pool.QueryRow(ctx, "UPDATE installation_settings SET value=$1,revision=revision+1,updated_at=now() WHERE name='kubernetes_patches' AND revision=$2 RETURNING revision", JSON(v), expectedRevision).Scan(&rev)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: Kubernetes patch settings changed; reload them and try again", ErrConflict)
	}
	return rev, err
}

// kubernetesPatchGate admits a revision that adds or changes Kubernetes
// patches only while the installation allows them and the caller holds
// kubernetes:patch. Unchanged or removed patches pass, so turning the
// setting off never blocks redeploying what already runs.
func kubernetesPatchGate(p Principal, project, environment string, previous, next spec.Application, enabled bool) error {
	if !spec.KubernetesPatchesChanged(previous, next) {
		return nil
	}
	if !enabled {
		return ErrKubernetesPatchesDisabled
	}
	// Like pods:exec, an administrator wildcard on a CLI or machine key is not
	// enough; the key must name the grant.
	explicit := p.CredentialType != "machine" && p.CredentialType != "cli" || contains(p.Permissions, "kubernetes:patch")
	if !explicit || !p.Allows("kubernetes:patch", project, environment, next.Name) {
		return fmt.Errorf("%w: adding or changing Kubernetes patches requires kubernetes:patch", ErrForbidden)
	}
	return nil
}
