package cluster

import (
	"errors"
	"fmt"
	"github.com/sijms/go-ora/v3/network"
	"testing"
)

func TestOracleQueryReceiveLimitCodeRequiresSentinel(t *testing.T) {
	for _, err := range []error{network.ErrReadLimit, fmt.Errorf("wrapped: %w", network.ErrReadLimit)} {
		if got := sqlQueryFailureCode("oracle", err); got != "database_query_result_limit" {
			t.Fatal(got)
		}
		if got := sqlQueryFailureCode("mysql", err); got != "database_query_failed" {
			t.Fatal(got)
		}
	}
	if got := sqlQueryFailureCode("oracle", errors.New(network.ErrReadLimit.Error())); got != "database_query_failed" {
		t.Fatal("matched text without sentinel", got)
	}
}
