package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/agent"
)

func TestHTTPMCPAuditCSVTransport(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, cursor string
		status                          int
		wantError                       bool
	}{
		{"CSV page", "text/csv; charset=utf-8", "id,action\n1,create\n", "42", 200, false},
		{"wrong media type", "application/json", `{"secret":"not CSV"}`, "", 200, true},
		{"invalid cursor", "text/csv", "id,action\n", "not-a-cursor", 200, true},
		{"oversized page", "text/csv", strings.Repeat("x", (1<<20)+1), "", 200, true},
		{"canonical denial", "application/json", `{"error":{"code":"forbidden","message":"Denied by canonical policy."}}`, "", 403, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/audit/export" {
					t.Fatal("audit export used an unexpected endpoint")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Hakopod-Next-Cursor", tc.cursor)
				w.Header().Set("Set-Cookie", "must-not-forward=fixture")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			original := httptest.NewRequest("POST", "/api/v1/mcp", nil)
			ctx := context.WithValue(context.Background(), mcpContextKey{}, original)
			var out agent.AuditExport
			err := mcpRequester(routes)(ctx, "GET", "/audit/export?identity_id=fixture", nil, "", &out)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected CSV decoding result: %v", err)
			}
			if err == nil && (out.CSV != tc.body || out.NextCursor != tc.cursor) {
				t.Fatal("CSV page or resume cursor changed")
			}
			if err != nil && (out.CSV != "" || out.NextCursor != "") {
				t.Fatal("failed CSV response exposed partial data")
			}
		})
	}
}
