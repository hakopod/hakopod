package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type cleanupItem struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
}
type cleanupInventory struct {
	SchemaVersion int              `json:"schema_version"`
	ObservedAt    time.Time        `json:"observed_at"`
	Filesystem    map[string]int64 `json:"filesystem"`
	Items         []cleanupItem    `json:"items"`
	Protected     []string         `json:"protected"`
	PlannedBytes  int64            `json:"planned_bytes"`
}
type cleanupReview struct {
	ID        string           `json:"id"`
	Inventory cleanupInventory `json:"inventory"`
	ExpiresAt time.Time        `json:"expires_at"`
}

func (s *Server) cleanupMaintenance(r *http.Request, path string, body any, out any) error {
	var raw io.Reader
	method := http.MethodGet
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(r.Context(), method, "http://maintenance"+path, raw)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.maintenanceClient().Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	if err != nil {
		return err
	}
	if response.StatusCode != 200 || len(data) > 256<<10 {
		return store.ErrConflict
	}
	return json.Unmarshal(data, out)
}

func (s *Server) serverCleanupReview(w http.ResponseWriter, r *http.Request) {
	if !s.installationOwner(w, r) {
		return
	}
	var inventory cleanupInventory
	if err := s.cleanupMaintenance(r, "/cleanup/preview", nil, &inventory); err != nil {
		problem(w, 503, "cleanup_unavailable", "Server cleanup inventory is unavailable.")
		return
	}
	if inventory.SchemaVersion != 1 || len(inventory.Items) > 256 || inventory.PlannedBytes < 0 {
		problem(w, 503, "cleanup_unavailable", "Server cleanup returned an invalid bounded inventory.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	id := store.NewID()
	expires := time.Now().UTC().Add(10 * time.Minute)
	p := who(r)
	if _, err := s.Store.Pool.Exec(r.Context(), `INSERT INTO server_cleanup_reviews(id,identity_id,key_id,inventory,expires_at) VALUES($1,$2,$3,$4,$5)`, id, p.ID, p.KeyID, store.JSON(inventory), expires); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, cleanupReview{ID: id, Inventory: inventory, ExpiresAt: expires})
}

func (s *Server) serverCleanupExecute(w http.ResponseWriter, r *http.Request) {
	if !s.installationOwner(w, r) {
		return
	}
	var in struct {
		ReviewID     string `json:"review_id"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &in) {
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) < 8 || len(idem) > 128 || in.Confirmation != "remove reviewed files" {
		problem(w, 400, "confirmation_required", "Review the exact files, then type remove reviewed files.")
		return
	}
	p := who(r)
	var existingID, existingReview, existingStatus string
	var existingReceipt map[string]any
	err := s.Store.Pool.QueryRow(r.Context(), `SELECT id,review_id,status,receipt FROM server_cleanup_operations WHERE identity_id=$1 AND idempotency_key=$2`, p.ID, idem).Scan(&existingID, &existingReview, &existingStatus, &existingReceipt)
	if err == nil {
		if existingReview != in.ReviewID {
			problem(w, 409, "idempotency_conflict", "This retry key belongs to another cleanup review.")
			return
		}
		s.serverCleanupResult(w, r, existingID, existingStatus, existingReceipt, false, nil)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var inventory cleanupInventory
	if err = tx.QueryRow(r.Context(), `SELECT inventory FROM server_cleanup_reviews WHERE id=$1 AND identity_id=$2 AND key_id=$3 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, in.ReviewID, p.ID, p.KeyID).Scan(&inventory); err != nil {
		problem(w, 409, "cleanup_review_expired", "Cleanup review is missing, expired or already used.")
		return
	}
	op := store.NewID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO server_cleanup_operations(id,review_id,identity_id,key_id,idempotency_key,status) VALUES($1,$2,$3,$4,$5,'running')`, op, in.ReviewID, p.ID, p.KeyID, idem); err != nil {
		failure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE server_cleanup_reviews SET consumed_at=now() WHERE id=$1`, in.ReviewID); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		failure(w, err)
		return
	}
	if err = s.Store.RuntimeAudit(r.Context(), p, "server.cleanup.requested", op, map[string]any{"review_id": in.ReviewID, "planned_bytes": inventory.PlannedBytes, "items": len(inventory.Items)}); err != nil {
		failure(w, err)
		return
	}
	var receipt map[string]any
	err = s.cleanupMaintenance(r, "/cleanup/execute", map[string]any{"schema_version": 1, "operation_id": op, "items": inventory.Items}, &receipt)
	s.serverCleanupResult(w, r, op, "running", receipt, true, func() error { return err })
}

func (s *Server) serverCleanupOperation(w http.ResponseWriter, r *http.Request) {
	if !s.installationOwner(w, r) {
		return
	}
	p := who(r)
	id := r.PathValue("id")
	var status string
	var receipt map[string]any
	if err := s.Store.Pool.QueryRow(r.Context(), `SELECT status,receipt FROM server_cleanup_operations WHERE id=$1 AND identity_id=$2`, id, p.ID).Scan(&status, &receipt); err != nil {
		failure(w, err)
		return
	}
	s.serverCleanupResult(w, r, id, status, receipt, false, nil)
}

func (s *Server) serverCleanupResult(w http.ResponseWriter, r *http.Request, id, status string, receipt map[string]any, executed bool, executeErr func() error) {
	w.Header().Set("Cache-Control", "no-store")
	if status == "succeeded" || status == "failed" {
		write(w, 200, map[string]any{"id": id, "status": status, "receipt": receipt})
		return
	}
	if executeErr != nil && executeErr() != nil {
		var observed map[string]any
		if err := s.cleanupMaintenance(r, "/cleanup/status?operation_id="+id, nil, &observed); err != nil {
			write(w, 202, map[string]any{"id": id, "status": "running"})
			return
		}
		receipt = observed
	}
	if receiptStatus, _ := receipt["status"].(string); receiptStatus == "running" {
		write(w, 202, map[string]any{"id": id, "status": "running"})
		return
	}
	if schema, _ := receipt["schema_version"].(float64); schema != 1 || receipt["operation_id"] != id {
		problem(w, 503, "cleanup_receipt_invalid", "Cleanup receipt is unavailable or invalid.")
		return
	}
	if receiptStatus, _ := receipt["status"].(string); receiptStatus == "failed" {
		_, _ = s.Store.Pool.Exec(r.Context(), `UPDATE server_cleanup_operations SET status='failed',receipt=$2,finished_at=now() WHERE id=$1 AND status='running'`, id, store.JSON(receipt))
		problem(w, 409, "cleanup_failed", "Cleanup stopped safely. Changed or protected files were not removed.")
		return
	}
	p := who(r)
	if err := s.Store.RuntimeAudit(r.Context(), p, "server.cleanup.completed", id, receipt); err != nil {
		failure(w, err)
		return
	}
	if _, err := s.Store.Pool.Exec(r.Context(), `UPDATE server_cleanup_operations SET status='succeeded',receipt=$2,finished_at=now() WHERE id=$1 AND status='running'`, id, store.JSON(receipt)); err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"id": id, "status": "succeeded", "receipt": receipt, "executed": executed})
}
