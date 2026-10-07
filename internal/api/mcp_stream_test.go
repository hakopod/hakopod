package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPEventSamplingKeepsCallerAndCursor(t *testing.T) {
	original := httptest.NewRequest("POST", "/api/v1/mcp?project=p&environment=dev", nil)
	type marker struct{}
	ctx := context.WithValue(original.Context(), marker{}, "caller")
	original = original.WithContext(ctx)
	ctx = context.WithValue(ctx, mcpContextKey{}, original)
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(marker{}) != "caller" || r.URL.Path != "/api/v1/deployments/d/events" || r.Header.Get("Last-Event-ID") != "42" {
			t.Fatal("caller or route changed")
		}
		fmt.Fprint(w, "id: 43\ndata: {\"id\":43}\n\n")
	})
	sample, err := mcpStreamRequester(routes)(ctx, "/deployments/d/events", "42")
	if err != nil || sample.NextCursor != "43" || len(sample.Events) != 1 {
		t.Fatal(sample, err)
	}
}
