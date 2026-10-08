package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func TestOracleQueryScopeRefusedBeforeRuntime(t *testing.T) {
	free := database.Spec{Engine: "oracle", Version: "23.26", Mode: "standalone", Shards: 1,
		TLS: &database.TLSConfig{Mode: "required"}, Oracle: &database.OracleConfig{Edition: "free"}}
	write, read := false, true
	for _, tc := range []struct {
		name    string
		change  func(*database.Spec)
		request database.QueryRequest
		code    string
	}{
		{"enterprise", func(s *database.Spec) { s.Oracle.Edition = "enterprise" }, database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write}, "database_query_engine_unsupported"},
		{"version", func(s *database.Spec) { s.Version = "19" }, database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write}, "database_query_engine_unsupported"},
		{"custom_image", func(s *database.Spec) { s.Oracle.Image = database.OracleFreeImage }, database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write}, "database_query_engine_unsupported"},
		{"free_readonly", func(*database.Spec) {}, database.QueryRequest{SQL: "SELECT 1", ReadOnly: &read}, "database_query_read_only_unsupported"},
		{"free_transaction", func(*database.Spec) {}, database.QueryRequest{SQL: "SELECT 1", ReadOnly: &write, ExecutionMode: "transaction"}, "database_query_execution_mode_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := free
			oracle := *free.Oracle
			spec.Oracle = &oracle
			tc.change(&spec)
			var client *Client
			_, err := client.QueryDatabase(context.Background(), database.Resource{Spec: spec}, tc.request)
			var failure *database.QueryError
			if !errors.As(err, &failure) || failure.Code != tc.code || failure.Outcome != "not_started" {
				t.Fatal("Oracle scope reached runtime")
			}
		})
	}
}
