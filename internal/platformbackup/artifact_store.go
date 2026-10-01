package platformbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"time"

	"filippo.io/age"
	"github.com/hakopod/hakopod/internal/backup"
)

type StoredArtifact struct {
	ID              string
	DestinationID   string
	ObjectKey       string
	EncryptedBytes  int64
	EncryptedSHA256 string
	Manifest        Manifest
}

type ArtifactRepository interface {
	BackupDestination(context.Context, string) (backup.Destination, error)
	ReservePlatformRecoveryUpload(context.Context, Operation, string) error
	StagePlatformRecoveryArtifact(context.Context, Operation, Manifest, string, int64, string) (StoredArtifact, error)
	StagedPlatformRecoveryArtifact(context.Context, string) (StoredArtifact, error)
	PlatformRecoveryArtifact(context.Context, string) (StoredArtifact, error)
	PublishPlatformRecoveryArtifact(context.Context, Operation, StoredArtifact) error
	DiscardPlatformRecoveryArtifact(context.Context, string) error
}

type EncryptedStore struct {
	Repo                 ArtifactRepository
	CredentialKey        []byte
	BlockedEndpointCIDRs []netip.Prefix
	MaxBytes             int64
	ObjectStore          func(backup.Destination, backup.Credentials) backup.ObjectStore
}

func (s *EncryptedStore) storage(d backup.Destination, c backup.Credentials) backup.ObjectStore {
	if s.ObjectStore != nil {
		return s.ObjectStore(d, c)
	}
	return backup.NewS3(d, c, s.BlockedEndpointCIDRs...)
}
func (s *EncryptedStore) limit() int64 {
	if s.MaxBytes > 0 && s.MaxBytes <= MaxArchiveBytes {
		return s.MaxBytes
	}
	return backup.DefaultMaxBytes
}

func (s *EncryptedStore) PutEncrypted(ctx context.Context, op Operation, manifest Manifest, produce func(io.Writer) error) (string, error) {
	d, err := s.Repo.BackupDestination(ctx, manifest.DestinationID)
	if err != nil {
		return "", err
	}
	if op.DestinationID != d.ID || op.DestinationRevision != d.Revision {
		return "", fmt.Errorf("backup destination revision changed")
	}
	credentials, err := backup.OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return "", err
	}
	recipient, err := age.ParseX25519Recipient(d.EncryptionRecipient)
	if err != nil {
		return "", fmt.Errorf("backup encryption recipient is invalid")
	}
	store := s.storage(d, credentials)
	defer store.Close()
	key := backup.ObjectKey(d, op.ID)
	if err = s.Repo.ReservePlatformRecoveryUpload(ctx, op, key); err != nil {
		return "", err
	}
	reader, writer := io.Pipe()
	produced := make(chan error, 1)
	go func() {
		encrypted, e := age.Encrypt(writer, recipient)
		if e == nil {
			e = produce(encrypted)
			if e == nil {
				e = encrypted.Close()
			}
		}
		_ = writer.CloseWithError(e)
		produced <- e
	}()
	bytes, digest, uploadErr := store.Put(ctx, key, reader, s.limit())
	_ = reader.CloseWithError(uploadErr)
	produceErr := <-produced
	if uploadErr != nil {
		return "", uploadErr
	}
	if produceErr != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = store.Delete(cleanup, key)
		return "", fmt.Errorf("Supabase archive capture did not complete")
	}
	staged, err := s.Repo.StagePlatformRecoveryArtifact(ctx, op, manifest, key, bytes, digest)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = store.Delete(cleanup, key)
		return "", err
	}
	return staged.ID, nil
}

func (s *EncryptedStore) OpenDecrypted(ctx context.Context, id string) (io.ReadCloser, error) {
	artifact, err := s.Repo.PlatformRecoveryArtifact(ctx, id)
	if err != nil {
		return nil, err
	}
	d, err := s.Repo.BackupDestination(ctx, artifact.DestinationID)
	if err != nil {
		return nil, err
	}
	credentials, err := backup.OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return nil, err
	}
	identity, err := age.ParseX25519Identity(credentials.EncryptionIdentity)
	if err != nil {
		return nil, fmt.Errorf("backup encryption identity is invalid")
	}
	store := s.storage(d, credentials)
	body, size, err := store.Get(ctx, artifact.ObjectKey)
	if err != nil {
		store.Close()
		return nil, err
	}
	if size != artifact.EncryptedBytes {
		body.Close()
		store.Close()
		return nil, fmt.Errorf("encrypted artifact size changed")
	}
	verified := &digestReadCloser{source: body, hash: sha256.New(), expectedBytes: size, expectedDigest: artifact.EncryptedSHA256}
	plain, err := age.Decrypt(verified, identity)
	if err != nil {
		verified.Close()
		store.Close()
		return nil, fmt.Errorf("Supabase archive could not be decrypted")
	}
	return &compoundReadCloser{Reader: plain, closers: []io.Closer{verified, closeFunc(store.Close)}}, nil
}

func (s *EncryptedStore) Publish(ctx context.Context, op Operation, manifest Manifest, staged string) error {
	artifact, err := s.Repo.StagedPlatformRecoveryArtifact(ctx, staged)
	if err != nil {
		return err
	}
	if artifact.Manifest.Digest() != manifest.Digest() {
		return fmt.Errorf("staged recovery manifest changed")
	}
	return s.Repo.PublishPlatformRecoveryArtifact(ctx, op, artifact)
}
func (s *EncryptedStore) Discard(ctx context.Context, id string) error {
	artifact, err := s.Repo.StagedPlatformRecoveryArtifact(ctx, id)
	if err != nil {
		return err
	}
	d, err := s.Repo.BackupDestination(ctx, artifact.DestinationID)
	if err != nil {
		return err
	}
	credentials, err := backup.OpenCredentials(s.CredentialKey, d)
	if err != nil {
		return err
	}
	store := s.storage(d, credentials)
	defer store.Close()
	if err = store.Delete(ctx, artifact.ObjectKey); err != nil {
		return err
	}
	return s.Repo.DiscardPlatformRecoveryArtifact(ctx, id)
}

type closeFunc func()

func (f closeFunc) Close() error { f(); return nil }

type compoundReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (c *compoundReadCloser) Close() error {
	var first error
	for _, closer := range c.closers {
		if err := closer.Close(); first == nil && err != nil {
			first = err
		}
	}
	return first
}

type digestReadCloser struct {
	source         io.ReadCloser
	hash           io.Writer
	expectedBytes  int64
	expectedDigest string
	read           int64
	eof            bool
}

func (d *digestReadCloser) Read(p []byte) (int, error) {
	n, err := d.source.Read(p)
	if n > 0 {
		_, _ = d.hash.Write(p[:n])
		d.read += int64(n)
	}
	if err == io.EOF {
		d.eof = true
	}
	return n, err
}
func (d *digestReadCloser) Close() error {
	err := d.source.Close()
	sum, ok := d.hash.(interface{ Sum([]byte) []byte })
	if err == nil && (!d.eof || d.read != d.expectedBytes || !ok || hex.EncodeToString(sum.Sum(nil)) != d.expectedDigest) {
		return fmt.Errorf("encrypted artifact digest changed")
	}
	return err
}
