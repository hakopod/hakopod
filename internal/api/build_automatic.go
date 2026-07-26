package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strings"
	"time"
)

var errBuildQueueFull = errors.New("build inbox is full")

// Called only after the GitHub integration has verified the raw body HMAC.
// Completion is an inbox item, never permission to trust an image in a webhook.
func (s *Server) enqueueBuildWebhook(ctx context.Context, event string, body []byte, delivery string) error {
	if event != "workflow_run" {
		return nil
	}
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Run struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Event      string `json:"event"`
			HeadSHA    string `json:"head_sha"`
			HeadBranch string `json:"head_branch"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_run"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return store.ErrInput
	}
	if payload.Action != "completed" || payload.Run.Event != "push" || payload.Run.ID <= 0 || !commitPattern.MatchString(payload.Run.HeadSHA) || !strings.HasPrefix(payload.Run.Name, "Hakopod build ") {
		return nil
	}
	id := strings.TrimPrefix(payload.Run.Name, "Hakopod build ")
	if !buildIdentifier.MatchString(id) {
		return nil
	}
	c, err := s.readBuild(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Provider != "github" || !c.AutoBuild || c.InstalledRevision != c.Revision || !strings.EqualFold(payload.Repository.FullName, c.Repository) || payload.Run.HeadBranch != c.Branch {
		return nil
	}
	runID := fmt.Sprintf("%032x", payload.Run.ID)
	return s.enqueueAutomaticBuild(ctx, c, runID, payload.Run.HeadSHA, payload.Run.ID, "github-workflow-"+delivery, body)
}
func (s *Server) enqueueAutomaticBuild(ctx context.Context, c buildConfig, runID, commit string, remoteID int64, delivery string, body []byte) error {
	hash := sha256.Sum256(body)
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(724891005)"); err != nil {
		return err
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM build_runs WHERE id=$1 OR (build_id=$2 AND identity_id=(SELECT identity_id FROM api_keys WHERE id=$3) AND idempotency_key=$4))`, runID, c.ID, c.GrantID, delivery).Scan(&duplicate); err != nil || duplicate {
		return err
	}
	var pending int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM build_runs WHERE automatic AND auto_status IN ('queued','processing')").Scan(&pending); err != nil {
		return err
	}
	if pending >= 1000 {
		return errBuildQueueFull
	}
	_, err = tx.Exec(ctx, `INSERT INTO build_runs(id,build_id,identity_id,key_id,idempotency_key,request_hash,config,config_revision,commit_sha,github_run_id,automatic,auto_status) SELECT $1,$2,identity_id,id,$3,$4,$5,$6,$7,$8,true,'queued' FROM api_keys WHERE id=$9 AND kind='integration' ON CONFLICT DO NOTHING`, runID, c.ID, delivery, hash[:], store.JSON(c), c.Revision, commit, remoteID, c.GrantID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// This lightweight inbox reader does no builds. Provider-hosted runners are
// started only by installed workflows; idle work is bounded PostgreSQL queries.
func (s *Server) RunBuilds(ctx context.Context) {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = s.processBuildQueue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *Server) processBuildQueue(ctx context.Context) error {
	// A process can die during its final attempt. Reclaim exhausted leases rather
	// than leaving them permanently in the pending index and consuming capacity.
	_, err := s.Store.Pool.Exec(ctx, `UPDATE build_runs SET auto_status='blocked',message='Automatic build processing exhausted five attempts; review the build before retrying',updated_at=now() WHERE id IN (SELECT id FROM build_runs WHERE automatic AND attempts>=5 AND (auto_status='queued' OR (auto_status='processing' AND updated_at<now()-interval '2 minutes')) ORDER BY created_at LIMIT 100)`)
	if err != nil {
		return err
	}
	// Keep pending work, ambiguous dispatches, and deployment replay records.
	// Only completed, unused metadata has an age-based expiry.
	_, err = s.Store.Pool.Exec(ctx, `DELETE FROM build_runs WHERE id IN (SELECT id FROM build_runs WHERE created_at<now()-interval '30 days' AND deployment_id='' AND ((automatic AND auto_status NOT IN ('queued','processing')) OR (NOT automatic AND status IN ('completed','failed','cancelled'))) ORDER BY created_at LIMIT 100)`)
	if err != nil {
		return err
	}
	run, err := scanBuildRun(s.Store.Pool.QueryRow(ctx, `UPDATE build_runs SET auto_status='processing',attempts=attempts+1,updated_at=now() WHERE id=(SELECT id FROM build_runs WHERE automatic AND attempts<5 AND next_attempt_at<=now() AND (auto_status='queued' OR (auto_status='processing' AND updated_at<now()-interval '2 minutes')) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+buildRunColumns))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	processCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	state, message, processErr := s.processAutomaticBuild(processCtx, run)
	if processErr != nil {
		state = "queued"
		message = processErr.Error()
	}
	_, err = s.Store.Pool.Exec(ctx, "UPDATE build_runs SET auto_status=CASE WHEN $2='queued' AND attempts>=5 THEN 'blocked' ELSE $2 END,message=$3,next_attempt_at=now()+interval '30 seconds',updated_at=now() WHERE id=$1", run.ID, state, message)
	return err
}
func (s *Server) processAutomaticBuild(ctx context.Context, run buildRun) (string, string, error) {
	c, err := s.readBuild(ctx, run.BuildID)
	if err != nil {
		return "", "", err
	}
	if c.Revision != run.ConfigRevision || !c.AutoBuild {
		return "superseded", "Build settings changed after this source revision was dispatched", nil
	}
	p, err := s.Store.KeyPrincipal(ctx, c.GrantID)
	if err != nil || !p.Allows("deployments:write", c.Project, c.Environment, c.Name) {
		return "blocked", "Automatic build grant expired, was revoked, or no longer has deployment permission; save the configuration to renew it", nil
	}
	run, err = s.refreshBuildRun(ctx, run)
	if err != nil {
		return "", "", err
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		return "finished", "Source build did not produce a successful deployment candidate", nil
	}
	if run.Image == "" {
		return "", "", errors.New("waiting for the verified source build-result artifact")
	}
	if !c.AutoDeploy {
		return "ready", "Verified image ready for an explicit deployment review", nil
	}
	var latest struct {
		SHA string `json:"sha"`
	}
	if c.Provider == "gitlab" {
		latest.SHA, err = s.gitlabBuildCommit(ctx, c, c.Branch)
	} else {
		err = s.githubGET(ctx, "/repos/"+c.Repository+"/commits/"+url.PathEscape(c.Branch), &latest)
	}
	if err != nil {
		return "", "", err
	}
	if latest.SHA != run.CommitSHA {
		return "superseded", "A newer source commit exists; the older build will not deploy", nil
	}
	if run.DeploymentID != "" {
		return "deployed", "Automatic deployment already accepted", nil
	}
	var expected int64
	app, err := s.Store.FindApplication(ctx, c.Project, c.Environment, c.Name)
	if err == nil {
		expected = app.Revision
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if err = s.Store.Reauthorize(ctx, c.GrantID, c.Project, c.Environment, c.Name); err != nil {
		return "blocked", "Automatic deployment permission was revoked", nil
	}
	deployment, err := s.acceptBuiltImage(ctx, p, c, run, expected)
	if err != nil {
		return "", "", err
	}
	return "deployed", "Automatic deployment accepted: " + deployment.ID, nil
}
