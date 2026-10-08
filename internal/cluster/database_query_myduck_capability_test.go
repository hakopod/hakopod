package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func TestMyDuckQueryUnsupportedModesFailBeforeClusterAccess(t *testing.T) {
	var client *Client
	write := false
	for _, scenario := range []struct {
		name, code string
		request    database.QueryRequest
	}{
		{"readonly", "database_query_read_only_unsupported", database.QueryRequest{SQL: "SELECT 1"}},
		{"transaction", "database_query_execution_mode_unsupported", database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write, ExecutionMode: "transaction"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := client.QueryDatabase(context.Background(), database.Resource{Spec: database.Spec{Engine: "duckdb"}}, scenario.request)
			var failure *database.QueryError
			if !errors.As(err, &failure) || failure.Code != scenario.code || failure.Outcome != "not_started" {
				t.Fatal("unsupported mode reached runtime access")
			}
		})
	}
}
