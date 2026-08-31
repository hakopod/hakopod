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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"filippo.io/age"
)

type Service struct {
	Repo          Repository
	Runtime       Runtime
	CredentialKey []byte
	StateDir      string
	MaxBytes      int64
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
	return NewS3(d, c)
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
	if err != nil || revoked {
		finish, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		return s.Repo.FinishBackupJob(finish, j, "cancelled", "Operation authority is no longer valid or its lease could not be renewed.", nil)
	}
	var wg sync.WaitGroup
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
	status, message := "succeeded", ""
	if err != nil {
		status = "failed"
		message = err.Error()
		if wasCancelled {
			status = "cancelled"
			message = "Operation cancelled, timed out, or lost its authority. A newly created restore database may be partial; inspect it before retrying."
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
	if a.Bytes < 1 || a.Bytes > s.limit() || a.DestinationID != j.DestinationID || a.Source.Engine != j.Target.Engine {
		return fmt.Errorf("artifact and restore target do not match")
	}
	d, err := s.Repo.BackupDestination(ctx, a.DestinationID)
	if err != nil {
		return err
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
	if a.ObjectKey != ObjectKey(d, a.ID) {
		return fmt.Errorf("artifact object key is outside the expected destination prefix")
	}
	c, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return err
	}
	storage := s.storage(d, c)
	defer storage.Close()
	if err = storage.Delete(ctx, a.ObjectKey); err != nil {
		return err
	}
	if err = storage.Delete(ctx, a.ObjectKey+".json"); err != nil {
		return err
	}
	return s.Repo.MarkBackupArtifactDeleted(ctx, a.ID)
}
