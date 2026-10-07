package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
)

func (c *Client) querySQLEngine(ctx context.Context, d database.Resource, q database.QueryRequest, check func(context.Context) error) (database.QueryResult, error) {
	switch d.Spec.Engine {
	case "duckdb":
		return c.queryMyDuckSQL(ctx, d, q, check)
	case "mysql", "vitess", "oracle":
		return c.querySQLDriver(ctx, d, q, check)
	case "clickhouse":
		return c.queryClickHouseSQL(ctx, d, q, check)
	}
	return database.QueryResult{}, &database.QueryError{Code: "database_query_engine_unsupported", Outcome: "not_started"}
}
