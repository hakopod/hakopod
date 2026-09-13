package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

type auditRecord struct {
	ID         int64           `json:"id"`
	IdentityID string          `json:"identity_id"`
	KeyID      string          `json:"key_id"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource"`
	Time       time.Time       `json:"time"`
	Metadata   json.RawMessage `json:"metadata"`
}

// User history and export are separate from the Free recent security log.
func (s *Server) auditHistory(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	if err := s.Store.RequireFeatures(r.Context(), "audit_history"); err != nil {
		authFailure(w, err)
		return
	}
	identity := r.URL.Query().Get("identity_id")
	if len(identity) != 32 || strings.IndexFunc(identity, func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') }) >= 0 {
		authFailure(w, store.ErrInput)
		return
	}
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			authFailure(w, store.ErrInput)
			return
		}
	}
	exporting := r.URL.Path == "/api/v1/audit/export"
	limit := 100
	if exporting {
		limit = 1000
	}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT id,identity_id,key_id,action,resource,time,metadata FROM audit_events WHERE identity_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, identity, before, limit+1)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	items := []auditRecord{}
	for rows.Next() {
		var item auditRecord
		if err = rows.Scan(&item.ID, &item.IdentityID, &item.KeyID, &item.Action, &item.Resource, &item.Time, &item.Metadata); err != nil {
			failure(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	var next int64
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	if !exporting {
		write(w, 200, map[string]any{"items": items, "next_cursor": next})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hakopod-user-audit.csv"`)
	if next > 0 {
		w.Header().Set("X-Hakopod-Next-Cursor", strconv.FormatInt(next, 10))
	}
	output := csv.NewWriter(w)
	_ = output.Write([]string{"id", "identity_id", "key_id", "action", "resource", "time", "metadata"})
	for _, item := range items {
		_ = output.Write([]string{strconv.FormatInt(item.ID, 10), item.IdentityID, item.KeyID, csvText(item.Action), csvText(item.Resource), item.Time.UTC().Format(time.RFC3339Nano), csvText(string(item.Metadata))})
	}
	output.Flush()
}

// Spreadsheet applications must treat operator-controlled names as text.
func csvText(value string) string {
	if strings.ContainsAny(value[:min(1, len(value))], "=+-@\t\r\n") {
		return "'" + value
	}
	return value
}
