package operations

import (
	"context"
	"strings"
	"testing"
)

func TestApprovalContext(t *testing.T) {
	ctx := context.Background()
	if ApprovalFrom(ctx) != "" {
		t.Fatal("empty context has approval")
	}
	if got := ApprovalFrom(WithApproval(ctx, "apr_1-x")); got != "apr_1-x" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"", "a b", "a\r\nb", strings.Repeat("a", 65)} {
		if ApprovalFrom(WithApproval(ctx, bad)) != "" {
			t.Fatalf("accepted %q", bad)
		}
	}
}
