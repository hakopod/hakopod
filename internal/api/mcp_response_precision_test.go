package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPResponsePreservesExactNumbers(t *testing.T) {
	original := httptest.NewRequest("POST", "/api/v1/mcp", nil)
	ctx := context.WithValue(context.Background(), mcpContextKey{}, original)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"revision": json.Number("9007199254740993")})
	})
	var out map[string]any
	if err := mcpRequester(handler)(ctx, "GET", "/applications/app", nil, "", &out); err != nil {
		t.Fatal(err)
	}
	if out["revision"] != json.Number("9007199254740993") {
		t.Fatal(out)
	}
}
