package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestOracleQueryUnsupportedModesFailBeforeClusterAccess(t *testing.T) {
	var c *Client
	write := false
	readonly := true
	for _, scenario := range []struct {
		name, code string
		request    database.QueryRequest
	}{
		{"default_readonly", "database_query_read_only_unsupported", database.QueryRequest{SQL: "SELECT 1", ExecutionMode: "nontransactional"}},
		{"explicit_readonly", "database_query_read_only_unsupported", database.QueryRequest{SQL: "SELECT 1", ReadOnly: &readonly, ExecutionMode: "nontransactional"}},
		{"default_transaction", "database_query_execution_mode_unsupported", database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write}},
		{"explicit_transaction", "database_query_execution_mode_unsupported", database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write, ExecutionMode: "transaction"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := c.querySQLDriver(context.Background(), database.Resource{Spec: database.Spec{Engine: "oracle"}}, scenario.request, nil)
			var failure *database.QueryError
			if !errors.As(err, &failure) || failure.Code != scenario.code || failure.Outcome != "not_started" {
				t.Fatal("unsupported Oracle mode reached runtime access")
			}
		})
	}
	capabilities := database.CapabilitiesForQuery("oracle")
	if capabilities.Supported || capabilities.ReadOnlySupported || capabilities.ReadOnlyEnforcement != "unsupported" || capabilities.TransactionalDML || capabilities.TransactionalDDL || len(capabilities.ExecutionModes) != 1 || capabilities.ExecutionModes[0] != "nontransactional" {
		t.Fatal("Oracle advertised unqualified transaction guarantees")
	}
}
