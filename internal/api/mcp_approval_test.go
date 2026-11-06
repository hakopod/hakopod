package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/operations"
)

func TestMCPApprovalHeaderAndMessage(t *testing.T) {
	var got []string
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Hakopod-Approval"))
		if r.Header.Get("Hakopod-Approval") == "" {
			w.Header().Set("Hakopod-Approval-Id", "apr_1")
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"approval_required","message":"Agent policy \"Prod\" requires approval. Approval apr_1 expires at 2099-01-01T00:00:00Z. Retry the identical request with the header Hakopod-Approval: apr_1 after it is approved."}}`)
			return
		}
		io.WriteString(w, `{}`)
	})
	request := mcpRequester(routes)
	ctx := context.WithValue(context.Background(), mcpContextKey{}, httptest.NewRequest("POST", "/mcp", nil))
	var out map[string]any
	err := request(ctx, "GET", "/applications", nil, "", &out)
	if err == nil || !strings.Contains(err.Error(), "Approval apr_1 expires at 2099-01-01T00:00:00Z") {
		t.Fatal(err)
	}
	if err := request(operations.WithApproval(ctx, "apr_1"), "GET", "/applications", nil, "", &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "" || got[1] != "apr_1" {
		t.Fatal(got)
	}
}
