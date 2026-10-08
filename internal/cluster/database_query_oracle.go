package cluster

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	goora "github.com/sijms/go-ora/v3"
	"github.com/sijms/go-ora/v3/network"
)

// Install the decoder ceiling before query preparation. Management connections
// retain the driver's defaults; query connections bound advertised allocations.
func boundOracleQueryConnection(conn *sql.Conn) error {
	return conn.Raw(func(raw any) error {
		native, ok := raw.(*goora.Connection)
		if !ok {
			return driver.ErrBadConn
		}
		session, ok := native.GetSession().(*network.Session)
		if !ok {
			return driver.ErrBadConn
		}
		session.SetReadLimit(database.QueryMaxBytes)
		return nil
	})
}

func sqlQueryFailureCode(engine string, err error) string {
	if engine == "oracle" && errors.Is(err, network.ErrReadLimit) {
		return "database_query_result_limit"
	}
	return "database_query_failed"
}
