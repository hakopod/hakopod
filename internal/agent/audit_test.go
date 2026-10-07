package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAuditExportTransport(t *testing.T) {
	for _, tc := range []struct {
		media, data, cursor string
		ok                  bool
	}{{"text/csv; charset=utf-8", "id\n1\n", "2", true}, {"application/json", "{}", "", false}, {"text/csv", strings.Repeat("x", (1<<20)+1), "", false}, {"text/csv", "id\n", "bad", false}} {
		res := &http.Response{Header: http.Header{"Content-Type": {tc.media}, "X-Hakopod-Next-Cursor": {tc.cursor}}, Body: io.NopCloser(strings.NewReader(tc.data))}
		var out AuditExport
		if err := DecodeAuditExport(res, &out); (err == nil) != tc.ok {
			t.Fatal("CSV bounds/type", err)
		}
	}
	s := NewWithOptions(nil, Scope{}, Options{}, 1)
	if _, err := s.Call(context.Background(), "audit_export", json.RawMessage(`{"identity_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)); err == nil {
		t.Fatal("export without installation")
	}
}

func TestAuditExportCurrentAuthority(t *testing.T) {
	calls := 0
	r := func(ctx context.Context, method, path string, in any, key string, out any) error {
		calls++
		if path == "/me" {
			return errors.New("revoked")
		}
		t.Fatal("export reached without current authority")
		return nil
	}
	s := NewWithOptions(r, Scope{}, Options{Installation: true, AllowAdmin: true}, 1)
	if _, err := s.Call(context.Background(), "audit_export", json.RawMessage(`{"identity_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)); err == nil || calls != 1 {
		t.Fatal("authority fence", err, calls)
	}
}
