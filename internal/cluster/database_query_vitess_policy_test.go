package cluster

import (
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"strings"
	"testing"
)

func TestVitessQueryDirectivePolicy(t *testing.T) {
	for _, name := range []string{"SCATTER_ERRORS_AS_WARNINGS", "IGNORE_MAX_MEMORY_ROWS", "IGNORE_MAX_PAYLOAD_SIZE", "MULTI_SHARD_AUTOCOMMIT"} {
		for _, suffix := range []string{"", "=1", "=false"} {
			statement := "SELECT /*vt+ harmless=1 " + strings.ToLower(name) + suffix + " */ 1"
			var failure *database.QueryError
			if !errors.As(validateVitessQueryDirectives(statement), &failure) || failure.Code != "database_query_statement_unsupported" || failure.Outcome != "not_started" {
				t.Fatal(statement, failure)
			}
		}
	}
}
func TestVitessQueryDirectivePolicyPreservesLiteralsAndComments(t *testing.T) {
	for _, statement := range []string{
		"SELECT '/*vt+ IGNORE_MAX_MEMORY_ROWS */'", "SELECT \"/*vt+ MULTI_SHARD_AUTOCOMMIT */\"", "SELECT `IGNORE_MAX_PAYLOAD_SIZE`",
		"SELECT 'doubled'' /*vt+ IGNORE_MAX_MEMORY_ROWS */'",
		"SELECT /* ordinary IGNORE_MAX_MEMORY_ROWS */ 1", "SELECT /*vt+ QUERY_TIMEOUT_MS=100 */ 1", "SELECT # /*vt+ IGNORE_MAX_MEMORY_ROWS */\n1",
		"SELECT -- /*vt+ IGNORE_MAX_MEMORY_ROWS */\n1", "SELECT /*vt+ harmless=IGNORE_MAX_MEMORY_ROWS */ 1",
	} {
		if err := validateVitessQueryDirectives(statement); err != nil {
			t.Fatal(statement, err)
		}
	}
}
func TestVitessQueryDirectivePolicyRejectsAmbiguity(t *testing.T) {
	for _, statement := range []string{
		`SELECT 'escaped\' /*vt+ IGNORE_MAX_MEMORY_ROWS */'`,
		"SELECT /*! IGNORE_MAX_MEMORY_ROWS */ 1", "SELECT /* outer /* nested */ 1", "SELECT 'unterminated", "SELECT /* unclosed",
		"SELECT 1--x /*vt+ IGNORE_MAX_MEMORY_ROWS */", strings.Repeat("x", database.QueryMaxSQLBytes+1),
	} {
		if validateVitessQueryDirectives(statement) == nil {
			t.Fatal(statement)
		}
	}
}
