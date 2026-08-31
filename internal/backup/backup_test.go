package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDestination(t *testing.T) (Destination, Credentials, []byte) {
	t.Helper()
	id, recipient, err := NewEncryptionIdentity("")
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	d := Destination{ID: strings.Repeat("a", 32), Name: "test", Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "test-backups", Prefix: "test", PathStyle: true, AllowHTTP: true, EncryptionRecipient: recipient}
	c := Credentials{AccessKeyID: "access", SecretAccessKey: "secret", EncryptionIdentity: id}
	d.EncryptedCredentials, err = SealCredentials(key, d.ID, c)
	if err != nil {
		t.Fatal(err)
	}
	return d, c, key
}
func TestCredentialEncryptionAndInputBounds(t *testing.T) {
	d, c, key := testDestination(t)
	if bytes.Contains(d.EncryptedCredentials, []byte(c.SecretAccessKey)) {
		t.Fatal("plaintext credential stored")
	}
	opened, err := OpenCredentials(key, d)
	if err != nil || opened != c {
		t.Fatal("credential round trip", err)
	}
	other := d
	other.ID = strings.Repeat("b", 32)
	if _, err = OpenCredentials(key, other); err == nil {
		t.Fatal("credential replay across destination IDs succeeded")
	}
	if _, err = OpenCredentials(bytes.Repeat([]byte{8}, 32), d); err == nil {
		t.Fatal("wrong key succeeded")
	}
	d.EncryptedCredentials[len(d.EncryptedCredentials)-1] ^= 1
	if _, err = OpenCredentials(key, d); err == nil {
		t.Fatal("corrupted credentials succeeded")
	}
	valid := DestinationInput{Name: "test", Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "test-bucket"}
	if err = valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://s3.example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com?token=secret"} {
		bad := valid
		bad.Endpoint = endpoint
		if bad.Validate() == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	bad := valid
	bad.Prefix = "../outside"
	if bad.Validate() == nil {
		t.Fatal("traversing prefix accepted")
	}
}

type testRuntime struct {
	data         string
	failure      error
	restored     string
	restoreCalls int
}

func (r *testRuntime) Targets(context.Context) ([]Target, error) { return nil, nil }
func (r *testRuntime) Resolve(_ context.Context, s Source) (Target, error) {
	return Target{Source: s, Available: true}, nil
}
func (r *testRuntime) Dump(_ context.Context, _ Target, w io.Writer) error {
	_, err := io.WriteString(w, r.data)
	if err != nil {
		return err
	}
	return r.failure
}
func (r *testRuntime) Restore(_ context.Context, _ Target, v io.Reader) error {
	r.restoreCalls++
	b, err := io.ReadAll(v)
	r.restored = string(b)
	return err
}

type testObjects struct {
	data    []byte
	deleted int
	fail    bool
}

func (o *testObjects) Put(_ context.Context, key string, r io.Reader, limit int64) (int64, string, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return 0, "", err
	}
	if o.fail {
		return 0, "", errors.New("storage failed")
	}
	if strings.HasSuffix(key, ".age") {
		o.data = b
	}
	hash := sha256.Sum256(b)
	return int64(len(b)), hex.EncodeToString(hash[:]), nil
}
func (o *testObjects) Get(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(o.data)), int64(len(o.data)), nil
}
func (o *testObjects) Delete(context.Context, string) error { o.deleted++; return nil }
func (o *testObjects) Close()                               {}

type testRepository struct {
	destination Destination
	artifact    Artifact
}

func (r *testRepository) BackupDestination(context.Context, string) (Destination, error) {
	return r.destination, nil
}
func (r *testRepository) ClaimBackupJob(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}
func (r *testRepository) HeartbeatBackupJob(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *testRepository) FinishBackupJob(context.Context, Job, string, string, *Artifact) error {
	return nil
}
func (r *testRepository) BackupArtifact(context.Context, string) (Artifact, error) {
	return r.artifact, nil
}
func (r *testRepository) QueueDueBackups(context.Context) error { return nil }
func (r *testRepository) ExpiredBackupArtifacts(context.Context, int) ([]Artifact, error) {
	return nil, nil
}
func (r *testRepository) MarkBackupArtifactDeleted(context.Context, string) error { return nil }
func (r *testRepository) ClaimBackupArtifactDeletion(context.Context, string) (bool, error) {
	return true, nil
}
func TestEncryptedBackupRestoreAndCorruptionBeforeMutation(t *testing.T) {
	d, _, key := testDestination(t)
	runtime := &testRuntime{data: "PGDMP" + strings.Repeat("database row with binary \x00 content\n", 2000)}
	objects := &testObjects{}
	repo := &testRepository{destination: d}
	service := &Service{Repo: repo, Runtime: runtime, CredentialKey: key, StateDir: filepath.Join(t.TempDir(), "private"), ObjectStore: func(Destination, Credentials) ObjectStore { return objects }}
	source := Source{Kind: "database", Engine: "postgresql", ApplicationID: strings.Repeat("c", 32), Service: "db", Database: "app"}
	j := Job{ID: strings.Repeat("d", 32), Kind: "backup", DestinationID: d.ID, Source: source}
	a, err := service.create(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(objects.data, []byte("database row")) {
		t.Fatal("backup stored without encryption")
	}
	repo.artifact = *a
	restore := Job{Kind: "restore", DestinationID: d.ID, ArtifactID: a.ID, Target: &Target{Source: Source{Kind: "database", Engine: "postgresql", Database: "hp_restore_12345678901234567890"}}}
	if err = service.restore(context.Background(), restore); err != nil {
		t.Fatal(err)
	}
	if runtime.restored != runtime.data || runtime.restoreCalls != 1 {
		t.Fatal("restore did not preserve bytes")
	}
	objects.data[len(objects.data)-1] ^= 1
	if err = service.restore(context.Background(), restore); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if runtime.restoreCalls != 1 {
		t.Fatal("corrupt backup mutated target")
	}
	// Even a forged manifest checksum cannot bypass age authentication.
	hash := sha256.Sum256(objects.data)
	repo.artifact.SHA256 = hex.EncodeToString(hash[:])
	if err = service.restore(context.Background(), restore); err == nil {
		t.Fatal("bad age authentication accepted")
	}
	if runtime.restoreCalls != 1 {
		t.Fatal("unauthenticated backup mutated target")
	}
	files, err := os.ReadDir(service.StateDir)
	if err != nil || len(files) != 0 {
		t.Fatal("restore staging was not cleaned", err)
	}
}
func TestFailedDumpNeverBecomesArtifact(t *testing.T) {
	d, _, key := testDestination(t)
	runtime := &testRuntime{data: "incomplete dump", failure: errors.New("dump failed")}
	objects := &testObjects{}
	service := &Service{Repo: &testRepository{destination: d}, Runtime: runtime, CredentialKey: key, ObjectStore: func(Destination, Credentials) ObjectStore { return objects }}
	a, err := service.create(context.Background(), Job{ID: strings.Repeat("e", 32), DestinationID: d.ID, Source: Source{Engine: "postgresql"}})
	if err == nil || a != nil {
		t.Fatal("failed producer became a successful artifact")
	}
}
