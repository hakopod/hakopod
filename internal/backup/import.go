package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/hakopod/hakopod/internal/database"
)

const ImportMaxBytes int64 = 2 << 30

type ImportSpec struct {
	DestinationID string    `json:"destination_id"`
	SourceName    string    `json:"source_name"`
	Engine        string    `json:"engine"`
	SourceVersion string    `json:"source_version"`
	CapturedAt    time.Time `json:"captured_at"`
	Bytes         int64     `json:"bytes"`
	SHA256        string    `json:"sha256"`
}

func (s ImportSpec) Validate(now time.Time) error {
	if !identifierID.MatchString(s.DestinationID) || !identifier.MatchString(s.SourceName) || s.Bytes < 16 || s.Bytes > ImportMaxBytes || len(s.SHA256) != 64 {
		return fmt.Errorf("%w: provide a destination, short source name, size up to 2 GiB and SHA-256 checksum", ErrInput)
	}
	digest, err := hex.DecodeString(s.SHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(s.SHA256) != s.SHA256 {
		return fmt.Errorf("%w: invalid archive checksum", ErrInput)
	}
	if s.CapturedAt.IsZero() || s.CapturedAt.After(now) {
		return fmt.Errorf("%w: provide the archive's captured recovery point", ErrInput)
	}
	if s.Engine == "postgresql" && (s.SourceVersion == "17" || s.SourceVersion == "18") || s.Engine == "redis" && s.SourceVersion == "8" {
		return nil
	}
	return fmt.Errorf("%w: import PostgreSQL 17/18 custom dumps or standalone Redis 8 RDB files", ErrInput)
}

type Import struct {
	ID         string     `json:"id"`
	Spec       ImportSpec `json:"spec"`
	Status     string     `json:"status"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ArtifactID string     `json:"artifact_id,omitempty"`
	Lease      string     `json:"-"`
}

// ImportArchive preserves a supplied Docker archive as an encrypted, independently
// read-back artifact. It never contacts or changes the source Docker application.
// The capture time is the operator's attestation, not an inferred current snapshot.
func (s *Service) ImportArchive(ctx context.Context, in Import, source io.Reader) (*Artifact, error) {
	if err := in.Spec.Validate(time.Now()); err != nil {
		return nil, err
	}
	d, err := s.Repo.BackupDestination(ctx, in.Spec.DestinationID)
	if err != nil {
		return nil, err
	}
	credentials, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return nil, err
	}
	recipient, err := age.ParseX25519Recipient(d.EncryptionRecipient)
	if err != nil {
		return nil, err
	}
	objectStore := s.storage(d, credentials)
	defer objectStore.Close()
	plainHash := sha256.New()
	raw := io.LimitReader(io.TeeReader(source, plainHash), in.Spec.Bytes+1)
	buffered := bufio.NewReaderSize(raw, 32<<10)
	if in.Spec.Engine == "postgresql" {
		version, err := postgresArchiveVersion(buffered)
		if err != nil || version != in.Spec.SourceVersion {
			return nil, fmt.Errorf("%w: PostgreSQL custom archive version does not match the reviewed source", ErrInput)
		}
	} else {
		if err := redisArchiveVersion(buffered); err != nil {
			return nil, fmt.Errorf("%w: import a standalone Redis 8 RDB file with its original version metadata", ErrInput)
		}
	}
	reader, writer := io.Pipe()
	produced := make(chan error, 1)
	go func() {
		encrypted, e := age.Encrypt(writer, recipient)
		if e == nil {
			var count int64
			if in.Spec.Engine == "redis" {
				manifest := database.RedisArchive{SchemaVersion: 1, DatabaseID: in.ID, Revision: 1, Shards: []database.Member{{Name: "docker-standalone", UID: in.ID, Role: "primary"}}}
				e = database.WriteRedisArchive(encrypted, manifest, func(_ database.Member, w io.Writer) error {
					var err error
					count, err = io.Copy(w, buffered)
					return err
				})
			} else {
				count, e = io.Copy(encrypted, buffered)
			}
			if e == nil && (count != in.Spec.Bytes || hex.EncodeToString(plainHash.Sum(nil)) != in.Spec.SHA256) {
				e = fmt.Errorf("%w: archive size or checksum differs from the review", ErrInput)
			}
			if e == nil {
				e = encrypted.Close()
			}
		}
		_ = writer.CloseWithError(e)
		produced <- e
	}()
	key := ObjectKey(d, in.ID)
	n, digest, err := objectStore.Put(ctx, key, reader, min(s.limit(), ImportMaxBytes+(32<<20)))
	_ = reader.CloseWithError(err)
	producerErr := <-produced
	if producerErr != nil {
		return nil, producerErr
	}
	if err != nil {
		return nil, err
	}
	format := "age-v1+postgresql-custom"
	if in.Spec.Engine == "redis" {
		format = "age-v1+redis-shards-v1"
	}
	a := &Artifact{ID: in.ID, JobID: in.ID, DestinationID: d.ID, Source: Source{Kind: "docker_import", Engine: in.Spec.Engine, ExternalName: in.Spec.SourceName}, ObjectKey: key, SHA256: digest, Bytes: n, Format: format, SourceVersion: in.Spec.SourceVersion, CapturedAt: &in.Spec.CapturedAt, CreatedAt: time.Now().UTC()}
	a.Scope = Scope(a.Source) + " Imported from Docker; the capture time was supplied by the operator. The source application was not contacted or changed."
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = objectStore.Delete(ctx, key)
		_ = objectStore.Delete(ctx, key+".json")
	}
	if err = s.withVerifiedArchive(ctx, *a, d, func(io.Reader) error { return nil }); err != nil {
		cleanup()
		return nil, err
	}
	verified := time.Now().UTC()
	a.VerifiedAt = &verified
	manifest, _ := json.Marshal(struct {
		SchemaVersion int       `json:"schema_version"`
		Artifact      *Artifact `json:"artifact"`
	}{1, a})
	if _, _, err = objectStore.Put(ctx, key+".json", bytes.NewReader(manifest), 64<<10); err != nil {
		cleanup()
		return nil, err
	}
	return a, nil
}

// Redis 7.4 also writes format 12. Require the original redis-ver AUX field,
// whose bounded plain-string encoding is written first by Redis 8 SAVE.
func redisArchiveVersion(reader *bufio.Reader) error {
	header, err := reader.Peek(10)
	if err != nil || string(header[:9]) != "REDIS0012" || header[9] != 0xfa {
		return ErrInput
	}
	offset := 10
	readString := func() (string, error) {
		b, err := reader.Peek(offset + 1)
		if err != nil || b[offset] >= 64 {
			return "", ErrInput
		}
		n := int(b[offset])
		offset++
		b, err = reader.Peek(offset + n)
		if err != nil {
			return "", err
		}
		value := string(b[offset : offset+n])
		offset += n
		return value, nil
	}
	key, err := readString()
	if err != nil || key != "redis-ver" {
		return ErrInput
	}
	version, err := readString()
	if err != nil || !strings.HasPrefix(version, "8.") {
		return ErrInput
	}
	return nil
}

// PostgreSQL 17/18 use custom archive version 1.16. Peek keeps every byte in the
// stream. Integer and string bounds reject malformed headers before upload.
func postgresArchiveVersion(reader *bufio.Reader) (string, error) {
	header, err := reader.Peek(12)
	if err != nil || string(header[:5]) != "PGDMP" || header[5] != 1 || header[6] != 16 || header[7] != 0 || header[8] != 4 || header[9] != 8 || header[10] != 1 || header[11] > 3 {
		return "", fmt.Errorf("unsupported PostgreSQL archive header")
	}
	offset := 12
	integer := func() (int, error) {
		b, e := reader.Peek(offset + 5)
		if e != nil {
			return 0, e
		}
		v := int(uint32(b[offset+1]) | uint32(b[offset+2])<<8 | uint32(b[offset+3])<<16 | uint32(b[offset+4])<<24)
		if b[offset] > 1 {
			return 0, ErrInput
		}
		if b[offset] == 1 {
			v = -v
		}
		offset += 5
		return v, nil
	}
	for i := 0; i < 7; i++ {
		if _, err = integer(); err != nil {
			return "", err
		}
	}
	readString := func() (string, error) {
		n, e := integer()
		if e != nil || n < 0 || n > 4096 || offset+n > 16<<10 {
			return "", ErrInput
		}
		b, e := reader.Peek(offset + n)
		if e != nil {
			return "", e
		}
		v := string(b[offset : offset+n])
		offset += n
		return v, nil
	}
	if _, err = readString(); err != nil {
		return "", err
	}
	remote, err := readString()
	if err != nil {
		return "", err
	}
	major := strings.SplitN(remote, ".", 2)[0]
	if _, err = strconv.Atoi(major); err != nil {
		return "", err
	}
	return major, nil
}

func (s *Service) DiscardImportedArchive(ctx context.Context, i Import) error {
	d, err := s.Repo.BackupDestination(ctx, i.Spec.DestinationID)
	if err != nil {
		return err
	}
	credentials, err := OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return err
	}
	objects := s.storage(d, credentials)
	defer objects.Close()
	if err = objects.Delete(ctx, ObjectKey(d, i.ID)); err != nil {
		return err
	}
	return objects.Delete(ctx, ObjectKey(d, i.ID)+".json")
}
