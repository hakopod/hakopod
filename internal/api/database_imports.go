package api

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) prepareDatabaseImport(w http.ResponseWriter, r *http.Request) {
	var input backup.ImportSpec
	if !decode(w, r, &input) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	if !s.backupEncryption(w) {
		return
	}
	i, err := s.Store.PrepareBackupImport(r.Context(), who(r), input, idem)
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 201, i)
}
func (s *Server) databaseImport(w http.ResponseWriter, r *http.Request) {
	i, err := s.Store.BackupImport(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, i)
}

type importReader struct {
	ctx    context.Context
	source io.Reader
	check  func(context.Context) error
	next   time.Time
}

func (r *importReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if time.Now().After(r.next) {
		if err := r.check(r.ctx); err != nil {
			return 0, err
		}
		r.next = time.Now().Add(time.Second)
	}
	return r.source.Read(p)
}
func (s *Server) uploadDatabaseImport(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		problem(w, 400, "invalid_request", "Upload the raw database archive as application/octet-stream.")
		return
	}
	select {
	case s.databaseImports <- struct{}{}:
		defer func() { <-s.databaseImports }()
	default:
		problem(w, 429, "busy", "Two archive uploads are already running. Try again shortly.")
		return
	}
	i, err := s.Store.ClaimBackupImport(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	if i.Status == "completed" {
		a, err := s.Store.BackupArtifact(r.Context(), i.ArtifactID)
		if err != nil {
			backupFailure(w, err)
			return
		}
		write(w, 200, a)
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != i.Spec.Bytes {
		_ = s.Store.ReleaseBackupImport(r.Context(), i)
		problem(w, 400, "invalid_request", "Archive size differs from the reviewed import.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Minute))
	body := http.MaxBytesReader(w, r.Body, backup.ImportMaxBytes+1)
	reader := &importReader{ctx: ctx, source: body, check: func(ctx context.Context) error { return s.Store.CheckBackupImport(ctx, who(r), i) }}
	a, err := s.Backups.ImportArchive(ctx, i, reader)
	if err == nil {
		err = s.Store.FinishBackupImport(ctx, who(r), i, *a)
	}
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		// A lost commit response must never remove an archive that was committed.
		current, e := s.Store.BackupImport(store.WithBackupPrincipal(cleanup, who(r)), who(r), i.ID)
		if e == nil && current.Status == "completed" {
			stored, e := s.Store.BackupArtifact(store.WithBackupPrincipal(cleanup, who(r)), current.ArtifactID)
			if e == nil {
				write(w, 200, stored)
				return
			}
		}
		if e == nil && current.Status == "uploading" && current.Lease == i.Lease {
			if s.Backups.DiscardImportedArchive(store.WithBackupPrincipal(cleanup, who(r)), i) == nil {
				_ = s.Store.ReleaseBackupImport(cleanup, i)
			}
		}
		backupFailure(w, err)
		return
	}
	write(w, 201, a)
}
