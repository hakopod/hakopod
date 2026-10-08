package api

import (
	"encoding/json"
	"github.com/hakopod/hakopod/internal/database"
	"net/http/httptest"
	"testing"
)

func TestDatabaseQueryUnsupportedSyntaxExplanation(t *testing.T) {
	w := httptest.NewRecorder()
	writeDatabaseQueryError(w, "operation", &database.QueryError{Code: "database_query_statement_syntax_unsupported", Outcome: "not_started"})
	var body struct {
		Error   struct{ Code, Message string }
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != 422 || body.Error.Code != "database_query_statement_syntax_unsupported" || body.Outcome != "not_started" || body.Error.Message != "This statement syntax is unsupported. Use one statement with standard quoted strings and identifiers. Put other values in bound parameters." {
		t.Fatal("unsupported syntax response differs")
	}
}
