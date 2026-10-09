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
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize acceptance for this retry key before checking whether it exists.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cleanup:"+p.ID+":"+idem); err != nil {
		failure(w, err)
		return
	}
	var id, reviewID, keyID, status string
	var inventory cleanupInventory
	var receipt map[string]any
	err = tx.QueryRow(r.Context(), `SELECT o.id,o.review_id,o.key_id,o.status,o.receipt,r.inventory FROM server_cleanup_operations o JOIN server_cleanup_reviews r ON r.id=o.review_id WHERE o.identity_id=$1 AND o.idempotency_key=$2`, p.ID, idem).Scan(&id, &reviewID, &keyID, &status, &receipt, &inventory)
	if err == nil {
		if reviewID != in.ReviewID || keyID != p.KeyID {
			problem(w, 409, "idempotency_conflict", "This retry key belongs to another cleanup review or credential.")
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		failure(w, err)
		return
	} else {
		if err = tx.QueryRow(r.Context(), `SELECT inventory FROM server_cleanup_reviews WHERE id=$1 AND identity_id=$2 AND key_id=$3 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, in.ReviewID, p.ID, p.KeyID).Scan(&inventory); err != nil {
			problem(w, 409, "cleanup_review_expired", "Cleanup review is missing, expired or already used.")
			return
		}
		id = store.NewID()
		status = "running"
		if _, err = tx.Exec(r.Context(), `INSERT INTO server_cleanup_operations(id,review_id,identity_id,key_id,idempotency_key,status) VALUES($1,$2,$3,$4,$5,'running')`, id, in.ReviewID, p.ID, p.KeyID, idem); err != nil {
			failure(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE server_cleanup_reviews SET consumed_at=now() WHERE id=$1`, in.ReviewID); err != nil {
			failure(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'server.cleanup.requested',$3,$4)`, p.ID, p.KeyID, id, store.JSON(map[string]any{"review_id": in.ReviewID, "planned_bytes": inventory.PlannedBytes, "items": len(inventory.Items)})); err != nil {
			failure(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		failure(w, err)
		return
	}
	if status != "succeeded" && status != "failed" {
		// The helper journals exact item identities before deletion. Replaying this
		// request resumes the same operation; a concurrent dispatch cannot delete twice.
		var observed map[string]any
		if err = s.cleanupMaintenance(r, "/cleanup/execute", map[string]any{"schema_version": 1, "operation_id": id, "items": inventory.Items}, &observed); err == nil {
			receipt = observed
		} else {
			receipt = nil
		}
	}
	s.serverCleanupResult(w, r, id, status, receipt)
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
	// A status read observes the helper; it never starts or resumes removal.
	if status != "succeeded" && status != "failed" {
		receipt = nil
	}
	s.serverCleanupResult(w, r, id, status, receipt)
}

func validCleanupReceipt(id string, receipt map[string]any) bool {
	schema, ok := receipt["schema_version"].(float64)
	if !ok || schema != 1 || receipt["operation_id"] != id {
		return false
	}
	state, _ := receipt["status"].(string)
	switch state {
	case "not_started", "running", "interrupted", "succeeded":
	default:
		return false
	}
	if state == "not_started" {
		return true
	}
	count := 0
	for _, name := range []string{"removed", "skipped", "uncertain"} {
		entries, ok := receipt[name].([]any)
		if !ok {
			return false
		}
		count += len(entries)
	}
	if count > 256 {
		return false
	}
	for _, name := range []string{"planned_bytes", "available_before_bytes"} {
		value, ok := receipt[name].(float64)
		if !ok || value < 0 {
			return false
		}
	}
	if state == "succeeded" {
		for _, name := range []string{"removed_bytes", "available_after_bytes", "reclaimed_bytes"} {
			value, ok := receipt[name].(float64)
			if !ok || value < 0 {
				return false
			}
		}
	}
	return true
}

func (s *Server) serverCleanupResult(w http.ResponseWriter, r *http.Request, id, status string, receipt map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	if status == "succeeded" || status == "failed" {
		write(w, 200, map[string]any{"id": id, "status": status, "receipt": receipt})
		return
	}
	if receipt == nil {
		if err := s.cleanupMaintenance(r, "/cleanup/status?operation_id="+id, nil, &receipt); err != nil {
			write(w, 202, map[string]any{"id": id, "status": "running", "message": "The cleanup receipt is unavailable. Refresh status or retry the same request."})
			return
		}
	}
	if !validCleanupReceipt(id, receipt) {
		problem(w, 503, "cleanup_receipt_invalid", "Cleanup receipt is unavailable or invalid. Keep the same retry key when retrying.")
		return
	}
	complete := receipt["status"] == "succeeded"
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var storedStatus string
	if err = tx.QueryRow(r.Context(), `SELECT status FROM server_cleanup_operations WHERE id=$1 FOR UPDATE`, id).Scan(&storedStatus); err != nil {
		failure(w, err)
		return
	}
	// A slower status request must not replace the completed receipt.
	if storedStatus == "succeeded" || storedStatus == "failed" {
		if err = tx.QueryRow(r.Context(), `SELECT receipt FROM server_cleanup_operations WHERE id=$1`, id).Scan(&receipt); err != nil {
			failure(w, err)
			return
		}
		status = storedStatus
	} else {
		status = "running"
		if complete {
			status = "succeeded"
		}
		if _, err = tx.Exec(r.Context(), `UPDATE server_cleanup_operations SET status=$2,receipt=$3,finished_at=CASE WHEN $2='succeeded' THEN now() ELSE NULL END WHERE id=$1`, id, status, store.JSON(receipt)); err != nil {
			failure(w, err)
			return
		}
		if complete {
			p := who(r)
			if _, err = tx.Exec(r.Context(), `INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'server.cleanup.completed',$3,$4)`, p.ID, p.KeyID, id, store.JSON(receipt)); err != nil {
				failure(w, err)
				return
			}
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		failure(w, err)
		return
	}
	code := http.StatusAccepted
	if status == "succeeded" || status == "failed" {
		code = http.StatusOK
	}
	write(w, code, map[string]any{"id": id, "status": status, "receipt": receipt})
}
