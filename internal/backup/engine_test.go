package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// engineFake stands in for a database engine that backs itself up. It records
// what it was asked to do and answers with whatever status the test queued.
type engineFake struct {
	restoreSource string
	ref           string
	startCalls    int
	pollCalls     int
	startErr      error
	statuses      []EngineStatus
	lastPrefix    string
	restoreStart  int
}

func (e *engineFake) StartBackup(_ context.Context, _ Target, _ Destination, _ Credentials, prefix string) (string, error) {
	e.startCalls++
	e.lastPrefix = prefix
	return e.ref, e.startErr
}
func (e *engineFake) PollBackup(context.Context, Target, string) (EngineStatus, error) {
	e.pollCalls++
	if len(e.statuses) == 0 {
		return EngineStatus{}, nil
	}
	next := e.statuses[0]
	if len(e.statuses) > 1 {
		e.statuses = e.statuses[1:]
	}
	return next, nil
}
func (e *engineFake) StartRestore(_ context.Context, _ Target, _ Destination, _ Credentials, prefix string, sourceDatabase string) (string, error) {
	e.restoreSource = sourceDatabase
	e.restoreStart++
	e.lastPrefix = prefix
	return e.ref, e.startErr
}
func (e *engineFake) PollRestore(ctx context.Context, t Target, ref string) (EngineStatus, error) {
	return e.PollBackup(ctx, t, ref)
}

// engineObjects is an object store that only answers the prefix operations the
// engine path uses. testObjects cannot serve them: its stubs are empty on purpose.
type engineObjects struct {
	keys           []string
	listErr        error
	listCalls      int
	deletedPrefix  string
	deleteRemains  bool
	deletePrefixes int
}

func (o *engineObjects) Put(context.Context, string, io.Reader, int64) (int64, string, error) {
	return 0, "", errors.New("an engine-managed backup never streams through this server")
}
func (o *engineObjects) Get(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("an engine-managed backup never streams through this server")
}
func (o *engineObjects) Delete(context.Context, string) error {
	return errors.New("an engine-managed backup is deleted by prefix")
}
func (o *engineObjects) ListPrefix(_ context.Context, prefix, token string) ([]string, string, error) {
	o.listCalls++
	if o.listErr != nil {
		return nil, "", o.listErr
	}
	if token != "" || !strings.HasSuffix(prefix, "/") {
		return nil, "", errors.New("unexpected listing request")
	}
	return o.keys, "", nil
}
func (o *engineObjects) DeletePrefix(_ context.Context, prefix string) (bool, error) {
	o.deletePrefixes++
	o.deletedPrefix = prefix
	if !o.deleteRemains {
		o.keys = nil
	}
	return o.deleteRemains, nil
}
func (o *engineObjects) Close() {}

func engineFixture(t *testing.T) (*Service, *testRepository, *engineFake, *engineObjects, Destination) {
	t.Helper()
	d, _, key := testDestination(t)
	repo := &testRepository{destination: d}
	engine := &engineFake{ref: "hakopod_backup_1"}
	objects := &engineObjects{}
	service := &Service{Repo: repo, Runtime: &testRuntime{}, Engine: engine, CredentialKey: key, ObjectStore: func(Destination, Credentials) ObjectStore { return objects }}
	return service, repo, engine, objects, d
}

func engineSource() Source {
	return Source{Kind: "database", Engine: "clickhouse", ApplicationID: strings.Repeat("c", 32), Service: "ch", Database: "app"}
}

func TestEngineBackupParksPollsThenRecordsArtifact(t *testing.T) {
	service, repo, engine, objects, d := engineFixture(t)
	id := strings.Repeat("d", 32)
	prefix := EnginePrefix(d, id)
	engine.statuses = []EngineStatus{{}, {Done: true, Bytes: 4096, Files: 3, Message: "done"}}
	objects.keys = []string{prefix + "meta.backup", prefix + "data/one", prefix + "data/two"}
	repo.job = &Job{ID: id, Kind: "backup", DestinationID: d.ID, Source: engineSource()}

	// First claim: the engine is asked to start and the job parks with its ref.
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.startCalls != 1 || engine.lastPrefix != prefix || repo.engineRef != engine.ref {
		t.Fatalf("start not recorded: %d %q %q", engine.startCalls, engine.lastPrefix, repo.engineRef)
	}
	if repo.parked != 1 || len(repo.finished) != 0 {
		t.Fatalf("first claim did not park: parked=%d finished=%d", repo.parked, len(repo.finished))
	}
	// Second claim: the engine is still working, so the job parks again.
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.pollCalls != 1 || repo.parked != 2 || len(repo.finished) != 0 {
		t.Fatalf("second claim did not poll and park: polls=%d parked=%d finished=%d", engine.pollCalls, repo.parked, len(repo.finished))
	}
	// Third claim: the engine reports success and the listing confirms it.
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.finished) != 1 || repo.finished[0].status != "succeeded" {
		t.Fatalf("engine backup did not succeed: %+v", repo.finished)
	}
	a := repo.finished[0].artifact
	if a == nil || a.Format != "engine:clickhouse" || a.SHA256 != "" || a.Bytes != 4096 || a.ObjectKey != prefix {
		t.Fatalf("unexpected engine artifact: %+v", a)
	}
	if objects.listCalls != 1 {
		t.Fatalf("success was recorded without confirming the prefix: %d listings", objects.listCalls)
	}
	if engine.startCalls != 1 {
		t.Fatal("a re-claimed job started the engine a second time")
	}
}

func TestEngineBackupElapsedBoundFailsAndKeepsTheReference(t *testing.T) {
	service, repo, engine, _, d := engineFixture(t)
	if MaxEngineElapsed <= 90*time.Minute {
		t.Fatalf("elapsed bound %s is shorter than a measured retryable object-store failure", MaxEngineElapsed)
	}
	started := time.Now().Add(-MaxEngineElapsed - time.Minute)
	repo.engineRef = engine.ref
	repo.job = &Job{ID: strings.Repeat("d", 32), Kind: "backup", DestinationID: d.ID, Source: engineSource(), StartedAt: &started}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.finished) != 1 || repo.finished[0].status != "failed" || repo.finished[0].artifact != nil {
		t.Fatalf("expired engine backup was not failed without an artifact: %+v", repo.finished)
	}
	if !strings.Contains(repo.finished[0].message, "stopped waiting") || !strings.Contains(repo.finished[0].message, engine.ref) {
		t.Fatalf("failure did not preserve the engine reference: %q", repo.finished[0].message)
	}
	if repo.engineRef != engine.ref {
		t.Fatal("the engine reference was cleared, so nothing can clean up after it")
	}
}

func TestEngineBackupVerificationGateRejectsEmptyListingAndCountMismatch(t *testing.T) {
	for _, c := range []struct {
		name  string
		keys  []string
		files int64
		want  string
	}{
		{"empty listing", nil, 3, "holds nothing"},
		{"count mismatch", []string{"a", "b"}, 5, "fewer than the 5 files"},
	} {
		t.Run(c.name, func(t *testing.T) {
			service, repo, engine, objects, d := engineFixture(t)
			objects.keys = c.keys
			engine.statuses = []EngineStatus{{Done: true, Bytes: 4096, Files: c.files}}
			repo.engineRef = engine.ref
			repo.job = &Job{ID: strings.Repeat("d", 32), Kind: "backup", DestinationID: d.ID, Source: engineSource()}
			if err := service.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(repo.finished) != 1 || repo.finished[0].status != "failed" {
				t.Fatalf("the engine's own word was accepted: %+v", repo.finished)
			}
			if repo.finished[0].artifact != nil {
				t.Fatal("an artifact was recorded for a backup that was never confirmed")
			}
			if !strings.Contains(repo.finished[0].message, c.want) {
				t.Fatalf("unexpected failure copy: %q", repo.finished[0].message)
			}
		})
	}
}

func TestEngineArtifactDeletionStaysPendingWhileWorkRemains(t *testing.T) {
	service, repo, _, objects, d := engineFixture(t)
	id := strings.Repeat("d", 32)
	a := Artifact{ID: id, DestinationID: d.ID, ObjectKey: EnginePrefix(d, id), Format: "engine:clickhouse", Bytes: 4096}
	objects.deleteRemains = true
	if err := service.DeleteArtifact(context.Background(), a); err == nil {
		t.Fatal("an incomplete prefix deletion reported success")
	}
	if repo.marked != 0 {
		t.Fatal("the artifact was marked deleted while objects remained")
	}
	if objects.deletedPrefix != a.ObjectKey {
		t.Fatalf("deleted the wrong prefix: %q", objects.deletedPrefix)
	}
	objects.deleteRemains = false
	if err := service.DeleteArtifact(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if repo.marked != 1 {
		t.Fatal("a completed prefix deletion did not mark the artifact deleted")
	}
	// A dump keeps its exact single-object deletion.
	single := &testObjects{}
	service.ObjectStore = func(Destination, Credentials) ObjectStore { return single }
	dump := Artifact{ID: id, DestinationID: d.ID, ObjectKey: ObjectKey(d, id), SHA256: strings.Repeat("a", 64), Format: "age-v1+postgresql-custom", Bytes: 10}
	if err := service.DeleteArtifact(context.Background(), dump); err != nil {
		t.Fatal(err)
	}
	if single.deleted != 2 || objects.deletePrefixes != 2 {
		t.Fatalf("dump deletion changed shape: %d object deletes, %d prefix deletes", single.deleted, objects.deletePrefixes)
	}
}

func TestEngineBackupCancellationOfAParkedJob(t *testing.T) {
	service, repo, engine, objects, d := engineFixture(t)
	id := strings.Repeat("d", 32)
	prefix := EnginePrefix(d, id)
	objects.keys = []string{prefix + "data/one"}
	repo.engineRef = engine.ref
	repo.cancelled = true
	repo.job = &Job{ID: id, Kind: "backup", DestinationID: d.ID, Source: engineSource()}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.finished) != 1 || repo.finished[0].status != "cancelled" || repo.finished[0].artifact != nil {
		t.Fatalf("a cancelled parked job was not recorded as cancelled: %+v", repo.finished)
	}
	if engine.pollCalls != 0 {
		t.Fatal("a cancelled job kept polling the engine")
	}
	if objects.deletedPrefix != prefix {
		t.Fatalf("cancellation left the engine's objects behind: %q", objects.deletedPrefix)
	}
	if repo.parked != 0 {
		t.Fatal("a cancelled job parked again instead of finishing")
	}
}

func TestEngineBackupFailureNeverEchoesEngineText(t *testing.T) {
	service, repo, engine, _, d := engineFixture(t)
	engine.ref = "BACKUP TO S3('http://s3/bucket','AKIAEXAMPLE','secret-key')"
	engine.statuses = []EngineStatus{{Failed: true, Message: "Code 499: while executing BACKUP TO S3('http://s3/bucket','AKIAEXAMPLE','secret-key')"}}
	repo.engineRef = engine.ref
	repo.job = &Job{ID: strings.Repeat("d", 32), Kind: "backup", DestinationID: d.ID, Source: engineSource()}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	message := repo.finished[0].message
	if repo.finished[0].status != "failed" {
		t.Fatalf("engine failure was not recorded as a failure: %+v", repo.finished)
	}
	for _, secret := range []string{"secret-key", "AKIAEXAMPLE", "Code 499", "S3("} {
		if strings.Contains(message, secret) {
			t.Fatalf("job error echoed engine text %q: %q", secret, message)
		}
	}
}

func TestEngineRestoreParksAndPollsWithoutStagingOrDigest(t *testing.T) {
	service, repo, engine, _, d := engineFixture(t)
	artifactID := strings.Repeat("f", 32)
	prefix := EnginePrefix(d, artifactID)
	repo.artifact = Artifact{ID: artifactID, DestinationID: d.ID, ObjectKey: prefix, Format: "engine:clickhouse", Bytes: 1 << 40, Source: engineSource()}
	engine.statuses = []EngineStatus{{}, {Done: true, Bytes: 1 << 40, Files: 3}}
	target := &Target{Source: Source{Kind: "database", Engine: "clickhouse", ApplicationID: strings.Repeat("c", 32), Service: "ch", Database: "hp_restore_12345678901234567890"}}
	repo.job = &Job{ID: strings.Repeat("d", 32), Kind: "restore", DestinationID: d.ID, ArtifactID: artifactID, Source: engineSource(), Target: target}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.restoreStart != 1 || engine.lastPrefix != prefix || repo.parked != 1 || len(repo.finished) != 0 {
		t.Fatalf("restore did not start and park: starts=%d prefix=%q parked=%d finished=%d", engine.restoreStart, engine.lastPrefix, repo.parked, len(repo.finished))
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.parked != 2 || len(repo.finished) != 0 {
		t.Fatalf("an unfinished restore did not park: parked=%d finished=%d", repo.parked, len(repo.finished))
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.finished) != 1 || repo.finished[0].status != "succeeded" {
		t.Fatalf("engine restore did not succeed: %+v", repo.finished)
	}
	// The fresh-database requirement is untouched.
	stale := *target
	stale.Database = "app"
	repo.job = &Job{ID: strings.Repeat("e", 32), Kind: "restore", DestinationID: d.ID, ArtifactID: artifactID, Source: engineSource(), Target: &stale}
	repo.finished = nil
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.finished) != 1 || repo.finished[0].status != "failed" {
		t.Fatalf("a restore into an existing database was accepted: %+v", repo.finished)
	}
}

func TestEngineManagedJobWithoutAnEngineFailsInsteadOfPanicking(t *testing.T) {
	service, repo, _, _, d := engineFixture(t)
	service.Engine = nil
	repo.job = &Job{ID: strings.Repeat("d", 32), Kind: "backup", DestinationID: d.ID, Source: engineSource()}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.finished) != 1 || repo.finished[0].status != "failed" {
		t.Fatalf("a missing engine did not fail the job: %+v", repo.finished)
	}
}
