package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"filippo.io/age"
)

type Service struct {
	Repo    Repository
	Runtime Runtime
	// Engine drives the engines that back themselves up. It is optional: a
	// server without one simply cannot run an engine-managed job, and says so.
	Engine        EngineBackup
	CredentialKey []byte
	StateDir      string
	MaxBytes      int64
	// BlockedEndpointCIDRs is trusted operator configuration, never destination input.
	BlockedEndpointCIDRs []netip.Prefix
	// Test/embedding seam; production lazily creates one official S3 client.
	ObjectStore func(Destination, Credentials) ObjectStore
	testMu      sync.Mutex
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (s *Service) limit() int64 {
	if s.MaxBytes > 0 && s.MaxBytes <= 64<<30 {
		return s.MaxBytes
	}
	return DefaultMaxBytes
}
func (s *Service) storage(d Destination, c Credentials) ObjectStore {
	if s.ObjectStore != nil {
		return s.ObjectStore(d, c)
	}
	return NewS3(d, c, s.BlockedEndpointCIDRs...)
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	nextSchedule := time.Time{}
	nextRetention := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(nextSchedule) {
			tick, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.Repo.QueueDueBackups(tick)
			cancel()
			if err != nil {
				slog.Warn("backup scheduling temporarily unavailable")
			}
			nextSchedule = time.Now().Add(30 * time.Second)
		}
		err := s.RunOnce(ctx)
		if err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			slog.Warn("backup worker operation could not be finalized")
		}
		if err == nil || time.Now().After(nextRetention) {
			s.retention(ctx)
			nextRetention = time.Now().Add(time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) RunOnce(parent context.Context) error {
	claim, cancel := context.WithTimeout(parent, 5*time.Second)
	j, err := s.Repo.ClaimBackupJob(claim, randomID())
	cancel()
	if err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(parent, 30*time.Minute)
	defer stop()
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	revoked, err := s.Repo.HeartbeatBackupJob(check, j.ID, j.Lease)
	cancel()
	// A job carrying an engine reference together with a cancellation request is
	// claimed on purpose, so this worker can stop waiting on the engine and record
	// what happened to what it wrote. Its heartbeat is refused because the cancel
	// is recorded, which is not the same thing as a lost lease. Such a job cannot
	// renew its lease at all, so it also gets no heartbeat goroutine: that
	// goroutine could only cancel the work it has left to do.
	stopping := j.EngineRef != "" && j.CancelRequested
	if err != nil || (revoked && !stopping) {
		finish, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		return s.Repo.FinishBackupJob(finish, j, "cancelled", "Operation authority is no longer valid or its lease could not be renewed.", nil)
	}
	var wg sync.WaitGroup
	if !stopping {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					check, c := context.WithTimeout(ctx, 2*time.Second)
					cancelled, e := s.Repo.HeartbeatBackupJob(check, j.ID, j.Lease)
					c()
					if e != nil || cancelled {
						stop()
						return
					}
				}
			}
		}()
	}
	var artifact *Artifact
	if j.Kind == "backup" {
		artifact, err = s.create(ctx, j)
	} else if j.Kind == "restore" {
		err = s.restore(ctx, j)
	} else {
		err = fmt.Errorf("unknown backup operation")
	}
	wasCancelled := ctx.Err() != nil
	stop()
	wg.Wait()
	// A parked job is waiting on the engine, not finished. It holds no lease now,
	// so nothing may be recorded against it: the next claim polls it again.
	if errors.Is(err, errEngineParked) {
		return nil
	}
	status, message := "succeeded", ""
	if err != nil {
		status = "failed"
		message = err.Error()
		if wasCancelled {
			status = "cancelled"
			message = "Operation cancelled, timed out, or lost its authority. A newly created restore database may be partial; inspect it before retrying."
		}
		// The engine path writes its own cancellation copy, because only it knows
		// what happened to the objects the engine had already written.
		if errors.Is(err, errEngineCancelled) {
			status = "cancelled"
			message = err.Error()
		}
	}
	finish, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	return s.Repo.FinishBackupJob(finish, j, status, message, artifact)
}
func (s *Service) create(ctx context.Context, j Job) (*Artifact, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d, err := s.Repo.BackupDestination(ctx, j.DestinationID)
	if err != nil {
		return nil, err
	}
	credentials, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return nil, err
	}
	target, err := s.Runtime.Resolve(ctx, j.Source)
	if err != nil {
		return nil, err
	}
	if !target.Available {
		return nil, fmt.Errorf("selected database backup target is unavailable")
	}
	if EngineManaged(j.Source) {
		return s.engineCreate(ctx, j, d, credentials, target)
	}
	recipient, err := age.ParseX25519Recipient(d.EncryptionRecipient)
	if err != nil {
		return nil, fmt.Errorf("backup encryption recipient is invalid")
	}
	store := s.storage(d, credentials)
	defer store.Close()
	reader, writer := io.Pipe()
	produced := make(chan error, 1)
	go func() {
		encrypted, e := age.Encrypt(writer, recipient)
		if e == nil {
			prefix := "PGDMP"
			if target.Engine == "mysql" {
				prefix = "-- MySQL dump"
			}
			verified := &dumpFormatWriter{target: encrypted, prefix: []byte(prefix)}
			e = s.Runtime.Dump(ctx, target, verified)
			if e == nil && !verified.valid {
				e = fmt.Errorf("database tool returned no valid logical dump header")
			}
			if e == nil {
				e = encrypted.Close()
			}
		}
		_ = writer.CloseWithError(e)
		produced <- e
	}()
	key := ObjectKey(d, j.ID)
	bytes, digest, uploadErr := store.Put(ctx, key, reader, s.limit())
	_ = reader.CloseWithError(uploadErr)
	if uploadErr != nil {
		cancel()
	}
	dumpErr := <-produced
	if uploadErr != nil {
		return nil, uploadErr
	}
	if dumpErr != nil {
		return nil, fmt.Errorf("database dump did not complete")
	}
	artifact := &Artifact{ID: j.ID, JobID: j.ID, DestinationID: d.ID, Source: target.Source, ObjectKey: key, SHA256: digest, Bytes: bytes, Format: "age-v1+" + func() string {
		if target.Engine == "mysql" {
			return "mysql-sql"
		}
		return "postgresql-custom"
	}(), Scope: Scope(j.Source), ScheduleID: j.ScheduleID, CreatedAt: time.Now().UTC()}
	manifest, _ := json.Marshal(struct {
		SchemaVersion int       `json:"schema_version"`
		Artifact      *Artifact `json:"artifact"`
	}{1, artifact})
	if _, _, err = store.Put(ctx, key+".json", strings.NewReader(string(manifest)), 64<<10); err != nil {
		cleanup, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = store.Delete(cleanup, key)
		return nil, fmt.Errorf("backup manifest upload failed; data cleanup was attempted")
	}
	return artifact, nil
}

type dumpFormatWriter struct {
	target       io.Writer
	prefix, head []byte
	valid        bool
}

func (w *dumpFormatWriter) Write(data []byte) (int, error) {
	if w.valid {
		return w.target.Write(data)
	}
	count := len(data)
	needed := len(w.prefix) - len(w.head)
	n := min(needed, len(data))
	w.head = append(w.head, data[:n]...)
	data = data[n:]
	if len(w.head) == len(w.prefix) {
		if string(w.head) != string(w.prefix) {
			return 0, fmt.Errorf("database tool output is not the expected logical dump format")
		}
		if _, err := w.target.Write(w.head); err != nil {
			return 0, err
		}
		w.valid = true
		w.head = nil
		if len(data) > 0 {
			if _, err := w.target.Write(data); err != nil {
				return n, err
			}
		}
	}
	return count, nil
}

func (s *Service) restore(ctx context.Context, j Job) error {
	if j.Target == nil || !strings.HasPrefix(j.Target.Database, "hp_restore_") {
		return fmt.Errorf("restore requires a reviewed fresh database target")
	}
	a, err := s.Repo.BackupArtifact(ctx, j.ArtifactID)
	if err != nil {
		return err
	}
	if a.Bytes < 1 || a.DestinationID != j.DestinationID || a.Source.Engine != j.Target.Engine {
		return fmt.Errorf("artifact and restore target do not match")
	}
	d, err := s.Repo.BackupDestination(ctx, a.DestinationID)
	if err != nil {
		return err
	}
	// An engine-managed backup never passes through this server, so none of the
	// staging, size, digest and age checks below exist for it. Its restore is the
	// same park-and-poll cycle as its backup.
	if engineArtifact(a) {
		return s.engineRestore(ctx, j, a, d)
	}
	// A dump is staged on local disk and streamed, so it has to fit that bound.
	if a.Bytes > s.limit() {
		return fmt.Errorf("artifact and restore target do not match")
	}
	if a.ObjectKey != ObjectKey(d, a.ID) {
		return fmt.Errorf("artifact object key is outside its expected destination prefix")
	}
	credentials, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return err
	}
	identity, err := age.ParseX25519Identity(credentials.EncryptionIdentity)
	if err != nil {
		return fmt.Errorf("backup recovery identity is invalid")
	}
	directory := s.StateDir
	if directory == "" {
		directory = filepath.Join(os.TempDir(), "hakopod-backups")
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("backup staging directory is unavailable")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("backup staging directory must be private (0700)")
	}
	var disk syscall.Statfs_t
	if err = syscall.Statfs(directory, &disk); err != nil {
		return fmt.Errorf("backup staging free space is unavailable")
	}
	if uint64(disk.Bavail)*uint64(disk.Bsize) < uint64(a.Bytes)+(512<<20) {
		return fmt.Errorf("restore needs space for the encrypted artifact plus 512 MiB free")
	}
	file, err := os.CreateTemp(directory, "hp-backup-*.age")
	if err != nil {
		return fmt.Errorf("cannot create private encrypted restore staging file")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	store := s.storage(d, credentials)
	defer store.Close()
	stream, length, err := store.Get(ctx, a.ObjectKey)
	if err != nil {
		return err
	}
	if length != a.Bytes {
		stream.Close()
		return fmt.Errorf("stored backup size does not match its manifest")
	}
	hash := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(file, hash), io.LimitReader(stream, a.Bytes+1), make([]byte, 64<<10))
	stream.Close()
	if err != nil || n != a.Bytes || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("stored backup checksum verification failed; target was not modified")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	plaintext, err := age.Decrypt(file, identity)
	if err != nil {
		return fmt.Errorf("backup cannot be decrypted with the preserved recovery key")
	}
	// Verify every authenticated age frame, including EOF, before any SQL runs.
	if _, err = io.CopyBuffer(io.Discard, contextReader{ctx: ctx, reader: plaintext}, make([]byte, 64<<10)); err != nil {
		return fmt.Errorf("backup authentication failed; target was not modified")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	plaintext, err = age.Decrypt(file, identity)
	if err != nil {
		return fmt.Errorf("backup decryption failed")
	}
	return s.Runtime.Restore(ctx, *j.Target, contextReader{ctx: ctx, reader: plaintext})
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func (s *Service) retention(ctx context.Context) {
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	artifacts, err := s.Repo.ExpiredBackupArtifacts(check, 4)
	cancel()
	if err != nil {
		return
	}
	for _, a := range artifacts {
		if ctx.Err() != nil {
			return
		}
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.DeleteArtifact(cleanup, a)
		cancel()
		if err != nil {
			slog.Warn("backup retention cleanup deferred", "artifact", a.ID)
		}
	}
}
func (s *Service) TestDestination(ctx context.Context, d Destination) (int64, error) {
	if !s.testMu.TryLock() {
		return 0, fmt.Errorf("%w: another destination test is running", ErrConflict)
	}
	defer s.testMu.Unlock()
	c, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return 0, err
	}
	store := s.storage(d, c)
	defer store.Close()
	value := make([]byte, 128)
	if _, err = rand.Read(value); err != nil {
		return 0, err
	}
	key := ObjectKey(d, "test-"+randomID())
	n, checksum, err := store.Put(ctx, key, strings.NewReader(string(value)), 1024)
	if err != nil {
		return 0, err
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = store.Delete(cleanup, key)
	}()
	body, size, err := store.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, io.LimitReader(body, 1025))
	body.Close()
	if err != nil || size != n || hex.EncodeToString(hash.Sum(nil)) != checksum {
		return 0, fmt.Errorf("destination read-back checksum failed")
	}
	if err = store.Delete(ctx, key); err != nil {
		return 0, err
	}
	return n, nil
}
func (s *Service) DeleteArtifact(ctx context.Context, a Artifact) error {
	claimed, err := s.Repo.ClaimBackupArtifactDeletion(ctx, a.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("%w: artifact is in use by an active restore", ErrConflict)
	}
	d, err := s.Repo.BackupDestination(ctx, a.DestinationID)
	if err != nil {
		return err
	}
	if !ownedByArtifact(d, a) {
		return fmt.Errorf("artifact object key is outside the expected destination prefix")
	}
	c, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return err
	}
	storage := s.storage(d, c)
	defer storage.Close()
	if engineArtifact(a) {
		// An engine-managed backup is a tree, and deletion is bounded, so one call
		// may not finish it. The row is only marked deleted when the object store
		// reports nothing left; otherwise it stays pending and a later pass resumes.
		remaining, err := storage.DeletePrefix(ctx, a.ObjectKey)
		if err != nil {
			return err
		}
		if remaining {
			return fmt.Errorf("backup prefix deletion is incomplete; the artifact stays pending for a later pass")
		}
		return s.Repo.MarkBackupArtifactDeleted(ctx, a.ID)
	}
	if err = storage.Delete(ctx, a.ObjectKey); err != nil {
		return err
	}
	if err = storage.Delete(ctx, a.ObjectKey+".json"); err != nil {
		return err
	}
	return s.Repo.MarkBackupArtifactDeleted(ctx, a.ID)
}

// An artifact format the server writes for a backup the database engine
// performed itself. The store relaxes its digest requirement on exactly this
// prefix, and only the server ever writes the format column.
const engineArtifactPrefix = "engine:"

// MaxEngineElapsed bounds one engine-managed operation from the moment the job
// first started, across every re-claim, because a single poll says nothing about
// how long the whole thing has run. It is deliberately generous: a retryable
// object-store error keeps ClickHouse working for 40 to 80 minutes before it
// reports a failure, so a tighter bound would declare failure on a backup that
// is still making progress.
const MaxEngineElapsed = 2 * time.Hour

// Verification lists at most this many pages before accepting that the tree
// holds at least as many objects as the engine said it wrote. It is the same
// round-trip budget one prefix deletion spends.
const maxEngineVerifyPages = 10

// errEngineParked means the job was handed back to the queue so the next claim
// polls the engine again. It is not a failure and must never be recorded as one.
var errEngineParked = errors.New("backup parked while the engine works")

// errEngineCancelled carries the operator-facing opening line of a cancellation
// the engine path recorded. The rest of the message says what happened to what
// the engine had already written.
var errEngineCancelled = errors.New("Operation cancelled while the database engine was working.")

// EngineManaged reports whether the database engine writes its own backup
// straight to object storage, so no bytes pass through this server. It is the one
// place that decision is made.
func EngineManaged(s Source) bool {
	return s.Kind == "database" && s.Engine == "clickhouse"
}

func engineArtifact(a Artifact) bool { return strings.HasPrefix(a.Format, engineArtifactPrefix) }

// ownedByArtifact asserts an artifact addresses only its own objects inside its
// own destination. A tree cannot be compared to a single derived key, so an
// engine-managed artifact is checked for containment instead, which is no weaker:
// nothing outside that destination's namespace, under that artifact's own
// identifier, can satisfy it. A dump keeps the exact equality it always had.
func ownedByArtifact(d Destination, a Artifact) bool {
	if engineArtifact(a) {
		root := EnginePrefix(d, a.ID)
		return !strings.Contains(a.ObjectKey, "..") && strings.HasPrefix(a.ObjectKey, root)
	}
	return a.ObjectKey == ObjectKey(d, a.ID)
}

// The store keeps started_at across re-claims, so elapsed engine time stays
// truthful however many times the job was parked and picked up again.
func engineElapsed(j Job) time.Duration {
	if j.StartedAt == nil {
		return 0
	}
	return time.Since(*j.StartedAt)
}

var engineRefPlain = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// safeRef names an engine operation inside a job error, which the API serves and
// the dashboard renders. The engine chose that string, so anything which is not
// a plain bounded identifier is not repeated at all: an engine string can carry
// the statement that produced it, and that statement carries the object-storage
// credentials the engine was handed. The reference itself is preserved on the
// job row in either case, which is what a cleanup needs.
func safeRef(ref string) string {
	if engineRefPlain.MatchString(ref) {
		return "operation " + ref
	}
	return "the operation recorded on this job"
}

// park hands the job back to the queue, which is how it waits: it gives up the
// streaming slot and the lease but keeps its engine reference and start time. A
// parked job must never be heartbeated, because while parked it is queued and a
// heartbeat would report the lease lost. Failing to park is not a failure of the
// backup either: the reaper returns a job with an engine reference to the queue
// once its lease expires.
func (s *Service) park(ctx context.Context, j Job) error {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.Repo.ReleaseBackupJobToEngine(release, j.ID, j.Lease); err != nil {
		slog.Warn("engine backup could not be parked; its lease will expire instead", "job", j.ID)
	}
	return errEngineParked
}

// engineRef is the engine operation this job is waiting on. The claimed row is
// the authority: a reference recorded by an earlier claim must never be started
// a second time.
func (s *Service) engineRef(ctx context.Context, j Job) (string, error) {
	if j.EngineRef != "" {
		return j.EngineRef, nil
	}
	return s.Repo.BackupJobEngineRef(ctx, j.ID)
}

// engineCreate is the whole backup state machine for an engine that writes its
// own files. A claim with no reference starts the engine and parks. A claim with
// one polls once and parks again, until the engine reports a terminal state, the
// elapsed bound runs out, or a cancellation arrives.
func (s *Service) engineCreate(ctx context.Context, j Job, d Destination, c Credentials, target Target) (*Artifact, error) {
	if s.Engine == nil {
		return nil, fmt.Errorf("this server is not configured to run backups the database engine performs itself")
	}
	prefix := EnginePrefix(d, j.ID)
	ref, err := s.engineRef(ctx, j)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		if j.CancelRequested {
			return nil, fmt.Errorf("%w The engine was never asked to start, so nothing was written.", errEngineCancelled)
		}
		started, startErr := s.Engine.StartBackup(ctx, target, d, c, prefix)
		if startErr != nil {
			return nil, fmt.Errorf("the database engine did not accept the backup request")
		}
		lost, refErr := s.Repo.SetBackupJobEngineRef(ctx, j.ID, j.Lease, started)
		if refErr != nil || lost {
			// The engine is working with no recorded reference, so no later claim can
			// poll it or clean up after it. Say that plainly instead of parking a job
			// nothing can resume.
			return nil, fmt.Errorf("the engine backup started but its reference could not be recorded; objects under the backup prefix may require storage cleanup")
		}
		return nil, s.park(ctx, j)
	}
	if j.CancelRequested {
		return nil, s.engineCancel(ctx, d, c, prefix, ref)
	}
	if engineElapsed(j) > MaxEngineElapsed {
		// What the engine is doing after this point is unknown, and saying otherwise
		// would be an invention. The reference stays on the job for a cleanup.
		return nil, fmt.Errorf("the database engine did not report a result within %s and hakopod stopped waiting; %s may still be running and may have written objects under the backup prefix", MaxEngineElapsed, safeRef(ref))
	}
	status, err := s.Engine.PollBackup(ctx, target, ref)
	if err != nil {
		// A poll that could not be read says nothing about the backup. Park and read
		// it again; the elapsed bound ends an operation that never answers.
		return nil, s.park(ctx, j)
	}
	if status.Failed {
		return nil, fmt.Errorf("the database engine reported that the backup failed (%s)", safeRef(ref))
	}
	if !status.Done {
		return nil, s.park(ctx, j)
	}
	store := s.storage(d, c)
	defer store.Close()
	if err = verifyEngineBackup(ctx, store, prefix, status); err != nil {
		return nil, err
	}
	return &Artifact{ID: j.ID, JobID: j.ID, DestinationID: d.ID, Source: target.Source, ObjectKey: prefix, SHA256: "", Bytes: status.Bytes, Format: engineArtifactPrefix + target.Engine, Scope: Scope(j.Source), ScheduleID: j.ScheduleID, CreatedAt: time.Now().UTC()}, nil
}

// verifyEngineBackup is the only independent evidence this kind of backup ever
// gets. A dump proves itself three ways: the server counts the bytes, computes
// the digest, and authenticates every age frame before any SQL runs. Here the
// server sees none of the bytes, so a terminal success is the engine's own word.
// Confirm against the bucket that the tree exists and holds at least as many
// objects as the engine said it wrote, and refuse to record an artifact when it
// does not.
func verifyEngineBackup(ctx context.Context, store ObjectStore, prefix string, status EngineStatus) error {
	if status.Bytes < 1 || status.Files < 1 {
		return fmt.Errorf("the database engine reported a finished backup containing no data")
	}
	objects, token := 0, ""
	for page := 0; page < maxEngineVerifyPages; page++ {
		keys, next, err := store.ListPrefix(ctx, prefix, token)
		if err != nil {
			return fmt.Errorf("the backup could not be confirmed in object storage")
		}
		objects += len(keys)
		if int64(objects) >= status.Files || next == "" {
			break
		}
		token = next
	}
	if objects == 0 {
		return fmt.Errorf("the database engine reported a finished backup but object storage holds nothing under its prefix")
	}
	if int64(objects) < status.Files {
		return fmt.Errorf("object storage holds %d objects under the backup prefix, fewer than the %d files the database engine reported writing", objects, status.Files)
	}
	return nil
}

// engineCancel cannot recall the engine's work: this interface starts and polls,
// it cannot abort. What hakopod can do is remove what was written, so a cancelled
// backup does not leave a tree of objects that no artifact row will ever delete.
// A cancelled job cannot renew its lease, so this is bounded well inside one.
func (s *Service) engineCancel(ctx context.Context, d Destination, c Credentials, prefix, ref string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	store := s.storage(d, c)
	defer store.Close()
	remaining, err := store.DeletePrefix(cleanup, prefix)
	if err != nil || remaining {
		return fmt.Errorf("%w The engine may still be writing %s, so objects under the backup prefix may require storage cleanup.", errEngineCancelled, safeRef(ref))
	}
	return fmt.Errorf("%w The objects written so far were removed.", errEngineCancelled)
}

// engineRestore replays an engine-managed backup through the engine, on the same
// park-and-poll cycle as the backup. Every authorization check and the fresh
// hp_restore_ database requirement are the caller's and stay exactly as they are.
func (s *Service) engineRestore(ctx context.Context, j Job, a Artifact, d Destination) error {
	if s.Engine == nil {
		return fmt.Errorf("this server is not configured to run restores the database engine performs itself")
	}
	if !ownedByArtifact(d, a) {
		return fmt.Errorf("artifact object key is outside its expected destination prefix")
	}
	c, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return err
	}
	ref, err := s.engineRef(ctx, j)
	if err != nil {
		return err
	}
	if ref == "" {
		if j.CancelRequested {
			return fmt.Errorf("%w No restore was started, so the target was not modified.", errEngineCancelled)
		}
		started, startErr := s.Engine.StartRestore(ctx, *j.Target, d, c, a.ObjectKey, a.Source.Database)
		if startErr != nil {
			return fmt.Errorf("the database engine did not accept the restore request")
		}
		lost, refErr := s.Repo.SetBackupJobEngineRef(ctx, j.ID, j.Lease, started)
		if refErr != nil || lost {
			return fmt.Errorf("the engine restore started but its reference could not be recorded; inspect the restore database before retrying")
		}
		return s.park(ctx, j)
	}
	if j.CancelRequested {
		// The objects here are the backup itself, so nothing is deleted. The engine
		// may already have written into the new database, which the operator owns.
		return fmt.Errorf("%w The engine may still be writing into the restore database; inspect it before retrying.", errEngineCancelled)
	}
	if engineElapsed(j) > MaxEngineElapsed {
		return fmt.Errorf("the database engine did not report a restore result within %s and hakopod stopped waiting; %s may still be running, so inspect the restore database before retrying", MaxEngineElapsed, safeRef(ref))
	}
	status, err := s.Engine.PollRestore(ctx, *j.Target, ref)
	if err != nil {
		return s.park(ctx, j)
	}
	if status.Failed {
		return fmt.Errorf("the database engine reported that the restore failed (%s); inspect the restore database before retrying", safeRef(ref))
	}
	if !status.Done {
		return s.park(ctx, j)
	}
	return nil
}
