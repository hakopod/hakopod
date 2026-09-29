package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
)

func nativeHistoryFixture(t *testing.T, db *Store) (ActionsPool, ActionsSlot, ActionsNativeHistory, actions.ProviderJob) {
	t.Helper()
	ctx := context.Background()
	app := Application{ID: NewID(), Project: "native-history-fixture", Environment: "development"}
	app.Name = "native-" + app.ID[:12]
	for _, statement := range []string{`INSERT INTO projects(name) VALUES('native-history-fixture') ON CONFLICT DO NOTHING`, `INSERT INTO environments(project,name) VALUES('native-history-fixture','development') ON CONFLICT DO NOTHING`} {
		if _, err := db.Pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO applications(id,project,environment,name,spec) VALUES($1,$2,$3,$4,'{}')`, app.ID, app.Project, app.Environment, app.Name); err != nil {
		t.Fatal(err)
	}
	config := spec.Service{Image: "example.invalid/runner@sha256:" + strings.Repeat("a", 64), Actions: &spec.Actions{Provider: actions.ProviderGitLab, GitLab: &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 42}, Credential: "original-management", JobsCredential: "original-jobs"}}
	if err := db.SyncActions(ctx, app, spec.Application{Name: app.Name, Services: map[string]spec.Service{"runner": config}}, 1); err != nil {
		t.Fatal(err)
	}
	pool, err := db.ActionsPool(ctx, app.ID, "runner")
	if err != nil || pool == nil {
		t.Fatal("fixture pool missing", err)
	}
	slot, err := db.NewActionsSlot(ctx, *pool)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic ciphertext exercises store ownership checks, not crypto or
	// provider acceptance. IDs deliberately exceed JavaScript's integer range.
	if err = db.SaveActionsProviderRegistration(ctx, slot, "90071992547409931234", bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	slots, err := db.ActionsSlots(ctx, app.ID, "runner")
	if err != nil || len(slots) != 1 {
		t.Fatal(err)
	}
	slot, err = db.MarkActionsProviderLaunch(ctx, slots[0])
	if err != nil {
		t.Fatal(err)
	}
	h, err := db.EnsureActionsNativeHistory(ctx, slot)
	if err != nil {
		t.Fatal(err)
	}
	job := actions.ProviderJob{Identity: actions.ProviderJobIdentity{RunnerID: slot.ProviderRunnerID, RunnerName: "hakopod-" + slot.ID, Repository: "42", RunID: "90071992547409931235", JobID: "90071992547409931236"}, Name: "development-store-fixture", Status: "in_progress", Steps: []actions.Step{}}
	return *pool, slot, h, job
}

func readNativeHistory(t *testing.T, db *Store, slot ActionsSlot) ActionsNativeHistory {
	t.Helper()
	h, err := db.ActionsNativeHistory(context.Background(), slot.ApplicationID, slot.Service, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestNativeJobHistoryKeepsOpaqueIDsAndImmutableBinding(t *testing.T) {
	db := isolatedDatabase(t)
	pool, slot, initial, job := nativeHistoryFixture(t, db)
	ctx := context.Background()
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, initial, job); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, initial, job); !errors.Is(err, ErrConflict) {
		t.Fatal("stale history reader overwrote metadata", err)
	}
	h := readNativeHistory(t, db, slot)
	if h.Job.NativeJob == nil || h.Job.NativeJob.Identity != job.Identity || h.Job.ProviderRunnerID != job.Identity.RunnerID {
		t.Fatal("native identifiers were coerced or lost")
	}
	for _, changed := range []actions.ProviderJobIdentity{
		{RunnerID: job.Identity.RunnerID, RunnerName: job.Identity.RunnerName, Repository: "43", RunID: job.Identity.RunID, JobID: job.Identity.JobID},
		{RunnerID: job.Identity.RunnerID, RunnerName: job.Identity.RunnerName, Repository: "42", RunID: job.Identity.RunID, JobID: "replacement"},
		{RunnerID: job.Identity.RunnerID, RunnerName: "hakopod-foreign", Repository: "42", RunID: job.Identity.RunID, JobID: job.Identity.JobID},
	} {
		forged := job
		forged.Identity = changed
		if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, h, forged); err == nil {
			t.Fatal("immutable job identity changed")
		}
	}
	if err := db.RecordActionsNativeJob(ctx, NewID(), pool.Service, h, job); !errors.Is(err, ErrConflict) {
		t.Fatal("job update crossed its application", err)
	}
	changedConfig := *h.Job.ProviderConfig
	changedConfig.JobsCredential = "replacement"
	forged := h
	forged.Job.ProviderConfig = &changedConfig
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, forged, job); !errors.Is(err, ErrConflict) {
		t.Fatal("job update changed credential binding", err)
	}
	job.Status, job.Conclusion = "completed", "success"
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, h, job); err != nil {
		t.Fatal(err)
	}
	h = readNativeHistory(t, db, slot)
	job.Status = "in_progress"
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, h, job); !errors.Is(err, ErrConflict) {
		t.Fatal("completed job regressed", err)
	}
	public, err := json.Marshal(h.Job)
	if err != nil || bytes.Contains(public, []byte("original-management")) || bytes.Contains(public, []byte("original-jobs")) || bytes.Contains(public, []byte("provider_config")) || !bytes.Contains(public, []byte(`"runner_id":"90071992547409931234"`)) {
		t.Fatal("public history leaked configuration or lost string IDs", err)
	}
}

func TestNativeDiscoveryAttemptsDeadlineAndConcurrentRefresh(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "attempts", true: "deadline"}[deadline], func(t *testing.T) {
			_, slot, h, _ := nativeHistoryFixture(t, db)
			if err := db.FinishActionsNativeDiscovery(ctx, slot, h, nil, nil); !errors.Is(err, ErrConflict) {
				t.Fatal("discovery completed before acquisition was paused", err)
			}
			if err := db.MarkActionsNativePaused(ctx, slot, h); err != nil {
				t.Fatal(err)
			}
			if err := db.MarkActionsNativePaused(ctx, slot, h); !errors.Is(err, ErrConflict) {
				t.Fatal("stale pause reset the deadline", err)
			}
			if deadline {
				if _, err := db.Pool.Exec(ctx, `UPDATE actions_jobs SET discovery_started_at=now()-interval '61 seconds' WHERE slot_id=$1`, slot.ID); err != nil {
					t.Fatal(err)
				}
			}
			for attempts := 1; attempts <= 3; attempts++ {
				h = readNativeHistory(t, db, slot)
				if err := db.FinishActionsNativeDiscovery(ctx, slot, h, nil, nil); err != nil {
					t.Fatal(err)
				}
				if err := db.FinishActionsNativeDiscovery(ctx, slot, h, nil, nil); !errors.Is(err, ErrConflict) {
					t.Fatal("stale discovery changed attempt accounting", err)
				}
				h = readNativeHistory(t, db, slot)
				if h.Attempts != attempts || h.Finished != (deadline || attempts == 3) {
					t.Fatal("discovery exceeded its bounded window")
				}
				if h.Finished {
					if h.Job.DiscoveryState != "unavailable" || h.Job.NativeJob != nil {
						t.Fatal("empty discovery claimed no job ran")
					}
					break
				}
			}
		})
	}
	pool, slot, initial, job := nativeHistoryFixture(t, db)
	if err := db.MarkActionsNativePaused(ctx, slot, initial); err != nil {
		t.Fatal(err)
	}
	cleanup := readNativeHistory(t, db, slot)
	job.Status, job.Conclusion = "completed", "success"
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, cleanup, job); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishActionsNativeDiscovery(ctx, slot, cleanup, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("cleanup overwrote a concurrent observation", err)
	}
	cleanup = readNativeHistory(t, db, slot)
	job.Status, job.Conclusion = "in_progress", ""
	if err := db.FinishActionsNativeDiscovery(ctx, slot, cleanup, &job, nil); err != nil {
		t.Fatal(err)
	}
	cleanup = readNativeHistory(t, db, slot)
	if !cleanup.Finished || cleanup.Job.NativeJob.Status != "completed" || cleanup.Job.NativeJob.Conclusion != "success" {
		t.Fatal("late cleanup observation regressed terminal metadata")
	}
}

func TestNativeReuseHoldSurvivesRetirementAndHistoryExpiry(t *testing.T) {
	db := isolatedDatabase(t)
	pool, slot, h, job := nativeHistoryFixture(t, db)
	ctx := context.Background()
	pending, err := db.NewActionsSlot(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveActionsProviderRegistration(ctx, pending, "another-runner", bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	slots, err := db.ActionsSlots(ctx, pool.ApplicationID, pool.Service)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range slots {
		if candidate.ID == pending.ID {
			pending = candidate
		}
	}
	other := job
	other.Identity.JobID += "2"
	if err := db.HoldActionsProviderReuse(ctx, pool.ApplicationID, pool.Service, h, []actions.ProviderJob{job, other}); err != nil {
		t.Fatal(err)
	}
	h = readNativeHistory(t, db, slot)
	if h.Job.DiscoveryState != "reuse_detected" {
		t.Fatal("reuse evidence state was not persisted")
	}
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, h, job); !errors.Is(err, ErrConflict) {
		t.Fatal("normal refresh erased a reuse violation", err)
	}
	if _, err = db.NewActionsSlot(ctx, pool); err == nil {
		t.Fatal("held pool created a new slot")
	}
	if _, err = db.MarkActionsProviderLaunch(ctx, pending); !errors.Is(err, ErrConflict) {
		t.Fatal("hold raced past the durable launch checkpoint", err)
	}
	if _, err = db.MarkActionsProviderCleanup(ctx, pending); err != nil {
		t.Fatal("hold blocked retirement of an unlaunched runner", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM actions_slots WHERE application_id=$1`, pool.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM actions_jobs WHERE slot_id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE actions_pools SET removed=true WHERE application_id=$1 AND service=$2`, pool.ApplicationID, pool.Service); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRetiredActionsPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	held, err := db.ActionsProviderPoolHeld(ctx, pool.ApplicationID, pool.Service)
	if err != nil || !held {
		t.Fatal("retirement or history expiry erased the pool safety hold", err)
	}
}

func TestNativeHistoryRetainsOnlyOriginalRequiredCredentials(t *testing.T) {
	db := isolatedDatabase(t)
	pool, slot, h, job := nativeHistoryFixture(t, db)
	ctx := context.Background()
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, h, job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM actions_slots WHERE id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE actions_pools SET removed=true WHERE application_id=$1`, pool.ApplicationID); err != nil {
		t.Fatal(err)
	}
	check := func(reference string, want bool) {
		t.Helper()
		got, err := db.ActionsCredentialRequired(ctx, pool.Project, pool.Environment, pool.ApplicationName, reference)
		if err != nil || got != want {
			t.Fatalf("credential %s retained=%v, want %v: %v", reference, got, want, err)
		}
	}
	check("original-management", true)
	check("original-jobs", true)
	check("unrelated", false)
	h = readNativeHistory(t, db, slot)
	job.Status = "completed"
	if err := db.RecordActionsNativeJob(ctx, pool.ApplicationID, pool.Service, h, job); err != nil {
		t.Fatal(err)
	}
	check("original-management", false)
	check("original-jobs", true)
	if _, err := db.Pool.Exec(ctx, `UPDATE actions_jobs SET created_at=$2 WHERE slot_id=$1`, slot.ID, time.Now().Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check("original-jobs", false)
}
