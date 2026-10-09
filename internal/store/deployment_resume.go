package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type resumeObservation struct {
	Services []struct{ Name, Status string } `json:"services"`
	Attempt  *struct {
		Failed []string `json:"failed"`
	} `json:"attempt"`
}

// ResumeDeployment queues only failed, incomplete and transitively dependent
// services from the latest accepted revision. The caller cannot choose a subset.
func (s *Store) ResumeDeployment(ctx context.Context, p Principal, id string, expected int64, idem string) (Deployment, error) {
	if len(idem) < 8 || len(idem) > 128 || expected < 1 {
		return Deployment{}, ErrInput
	}
	hash := sha256.Sum256(JSON(struct {
		Deployment string
		Revision   int64
	}{id, expected}))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,1))", p.ID+":"+idem); err != nil {
		return Deployment{}, err
	}
	var replayID string
	var replayHash []byte
	err = tx.QueryRow(ctx, "SELECT deployment_id,request_hash FROM deployment_resume_attempts WHERE identity_id=$1 AND idempotency_key=$2", p.ID, idem).Scan(&replayID, &replayHash)
	if err == nil {
		if replayID != id || !bytes.Equal(replayHash, hash[:]) {
			return Deployment{}, fmt.Errorf("%w: idempotency key reused with different input", ErrConflict)
		}
		replayed, err := scanDep(tx.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE id=$1", id))
		if err != nil {
			return Deployment{}, err
		}
		application, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1", replayed.ApplicationID))
		if err != nil {
			return Deployment{}, err
		}
		if !p.Allows("deployments:write", application.Project, application.Environment, application.Name) {
			return Deployment{}, ErrForbidden
		}
		return replayed, nil
	}
	if err != pgx.ErrNoRows {
		return Deployment{}, err
	}
	d, err := scanDep(tx.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return d, err
	}
	a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", d.ApplicationID))
	if err != nil {
		return d, err
	}
	if !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return d, ErrForbidden
	}
	if d.Status != "failed" || d.Revision != expected || a.Revision != expected || d.ResolvedSpec == nil || d.RecoveryState != "" {
		return d, fmt.Errorf("%w: only the latest partially failed unrecovered revision can resume", ErrConflict)
	}
	if s.AdmitDeployment != nil {
		if err = s.AdmitDeployment(ctx, tx, p, a.Project, a.Environment, idem); err != nil {
			return d, err
		}
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,2))", a.Project+":"+a.Environment+":"+a.Name); err != nil {
		return d, err
	}
	if err = s.requireActionsTx(ctx, tx, a.Project, a.Environment, *d.ResolvedSpec); err != nil {
		return d, err
	}
	var newer bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM deployments WHERE application_id=$1 AND revision>$2)", a.ID, d.Revision).Scan(&newer); err != nil {
		return d, err
	}
	if newer {
		return d, ErrConflict
	}
	if s.ValidateDeployment != nil {
		if err = s.ValidateDeployment(ctx, a, *d.ResolvedSpec); err != nil {
			return d, err
		}
	}
	if err = fenceSessionsForDeployment(ctx, tx, a.ID); err != nil {
		return d, err
	}
	if err = fenceInvocationsForDeployment(ctx, tx, a.ID); err != nil {
		return d, err
	}
	var maintenance bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM deployment_volume_cleanup c JOIN deployments x ON x.id=c.deployment_id WHERE x.application_id=$1 AND NOT c.completed) OR EXISTS(SELECT 1 FROM volume_resizes WHERE application_id=$1 AND "+resizeBlocking+")", a.ID).Scan(&maintenance); err != nil {
		return d, err
	}
	if maintenance {
		return d, fmt.Errorf("%w: finish volume maintenance before resuming this deployment", ErrConflict)
	}
	var observed resumeObservation
	if err = json.Unmarshal(d.Result, &observed); err != nil {
		return d, ErrConflict
	}
	if observed.Attempt == nil || len(observed.Attempt.Failed) == 0 {
		return d, fmt.Errorf("%w: this failure has no recorded service attempt that can safely resume", ErrConflict)
	}
	state := map[string]string{}
	for _, item := range observed.Services {
		state[item.Name] = item.Status
	}
	selected := map[string]bool{}
	for name, svc := range d.ResolvedSpec.Services {
		status := state[name]
		if status != "ready" && status != "completed" && status != "configured" && status != "scheduled" && status != "stopped" && status != "sleeping" {
			if svc.Job != nil && status == "failed" {
				return d, fmt.Errorf("%w: failed deployment job %s requires a new reviewed revision", ErrConflict, name)
			}
			selected[name] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for name, svc := range d.ResolvedSpec.Services {
			if selected[name] {
				continue
			}
			for _, dependency := range svc.DependsOn {
				if selected[dependency] {
					selected[name] = true
					changed = true
					break
				}
			}
		}
	}
	services := make([]string, 0, len(selected))
	for name := range selected {
		services = append(services, name)
	}
	sort.Strings(services)
	if len(services) == 0 {
		return d, fmt.Errorf("%w: no failed or incomplete service can resume", ErrConflict)
	}
	if d.ResumeGeneration == 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO deployment_resume_attempts(deployment_id,generation,identity_id,key_id,idempotency_key,request_hash,services,status,error,result,created_at,started_at,finished_at) VALUES($1,0,$2,$3,$4,$5,'{}',$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, id, d.IdentityID, d.KeyID, "original:"+id, []byte{}, d.Status, d.Error, d.Result, d.CreatedAt, d.StartedAt, d.FinishedAt); err != nil {
			return d, err
		}
	}
	generation := d.ResumeGeneration + 1
	if _, err = tx.Exec(ctx, `INSERT INTO deployment_resume_attempts(deployment_id,generation,identity_id,key_id,idempotency_key,request_hash,services,status) VALUES($1,$2,$3,$4,$5,$6,$7,'queued')`, id, generation, p.ID, p.KeyID, idem, hash[:], services); err != nil {
		return d, err
	}
	if _, err = tx.Exec(ctx, `UPDATE deployments SET status='queued',error='',cancel_requested=false,started_at=NULL,finished_at=NULL,recovery_state='',recovery_revision=0,recovery_spec=NULL,recovery_error='',resume_generation=$2,resume_services=$3,resume_identity_id=$4,resume_key_id=$5 WHERE id=$1`, id, generation, services, p.ID, p.KeyID); err != nil {
		return d, err
	}
	if _, err = tx.Exec(ctx, "UPDATE applications SET status='queued',updated_at=now() WHERE id=$1 AND revision=$2", a.ID, d.Revision); err != nil {
		return d, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message) VALUES($1,'resume_queued','Failed and dependent services queued for same-revision resume')", id); err != nil {
		return d, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'deployment.resume',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]any{"revision": d.Revision, "services": services})); err != nil {
		return d, err
	}
	if err = tx.Commit(ctx); err != nil {
		return d, err
	}
	return s.Deployment(ctx, id)
}
