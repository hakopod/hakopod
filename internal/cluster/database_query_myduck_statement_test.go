package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func TestMyDuckSingleStatement(t *testing.T) {
	for _, sql := range []string{"SELECT 1", "SELECT 1; -- end", "SELECT ';', 'it''s;fine', \"semi;colon\"", "/* outer /* nested */ */ SELECT $1"} {
		if !myduckSingleStatement(sql) {
			t.Errorf("valid statement rejected: %q", sql)
		}
	}
	for _, sql := range []string{"", "-- comment", "SELECT 1; SELECT 2", "SELECT 1;;", "SELECT 1;/* comment */INSERT INTO t VALUES (1)", "SELECT 'unterminated", "SELECT $$unterminated", "SELECT 1 /* unterminated", "SELECT 'back\\slash'", "SELECT 1; -- comment\nSELECT 2", "SELECT 1; -- comment\rSELECT 2", "SELECT 1; -- comment\r\nSELECT 2", "SELECT 1; -- comment\rSELECT 2\n", "SELECT $$a;b$$", "SELECT $tag$a;b$tag$", "SELECT E'a;'", "SELECT b'a;'", "SELECT U&'a;'", "SELECT `a;b`"} {
		if myduckSingleStatement(sql) {
			t.Errorf("ambiguous or multiple statements accepted: %q", sql)
		}
	}
}

func TestMyDuckMultipleStatementsRejectedBeforeRuntimeAccess(t *testing.T) {
	write := false
	client := &Client{}
	_, err := client.queryMyDuckSQL(context.Background(), myduckFixture(), database.QueryRequest{SQL: "SELECT 1; SELECT 2", ReadOnly: &write, ExecutionMode: "nontransactional"}, nil)
	var failure *database.QueryError
	if !errors.As(err, &failure) || failure.Outcome != "not_started" {
		t.Fatal("multiple statements reached runtime access")
	}
	arguments := myduckQueryArguments([]any{"semicolon; quote' dollar$$"})
	if len(arguments) != 3 || arguments[2] != "semicolon; quote' dollar$$" {
		t.Fatal("bound delimiter changed")
	}
}
