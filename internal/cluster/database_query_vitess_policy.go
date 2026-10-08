package cluster

import (
	"github.com/hakopod/hakopod/internal/database"
	"strings"
)

// validateVitessQueryDirectives preserves ordinary comments and literals while
// refusing directives that change bounded or complete execution guarantees.
// MySQL executable comments, nested comments and incomplete tokens are refused
// because interpreting them safely requires the server's parser configuration.
func validateVitessQueryDirectives(statement string) error {
	if err := scanVitessQueryDirectives(statement, true); err != nil {
		return err
	}
	return scanVitessQueryDirectives(statement, false)
}

// Both escape modes must be safe; this avoids relying on the session SQL mode.
func scanVitessQueryDirectives(statement string, backslashEscapes bool) error {
	unsupported := func() error {
		return &database.QueryError{Code: "database_query_statement_unsupported", Outcome: "not_started"}
	}
	if len(statement) > database.QueryMaxSQLBytes {
		return unsupported()
	}
	for i := 0; i < len(statement); {
		switch statement[i] {
		case '\'', '"', '`':
			quote := statement[i]
			i++
			closed := false
			for i < len(statement) {
				if statement[i] == '\\' && backslashEscapes {
					if i+1 >= len(statement) {
						return unsupported()
					}
					i += 2
					continue
				}
				if statement[i] == quote {
					if i+1 < len(statement) && statement[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return unsupported()
			}
		case '#':
			for i < len(statement) && statement[i] != '\n' && statement[i] != '\r' {
				i++
			}
		case '-':
			if i+2 < len(statement) && statement[i+1] == '-' && statement[i+2] <= 32 {
				i += 3
				for i < len(statement) && statement[i] != '\n' && statement[i] != '\r' {
					i++
				}
			} else {
				i++
			}
		case '/':
			if i+1 >= len(statement) || statement[i+1] != '*' {
				i++
				continue
			}
			start := i
			i += 2
			if i < len(statement) && statement[i] == '!' {
				return unsupported()
			}
			for i+1 < len(statement) && !(statement[i] == '*' && statement[i+1] == '/') {
				if statement[i] == '/' && statement[i+1] == '*' {
					return unsupported()
				}
				i++
			}
			if i+1 >= len(statement) {
				return unsupported()
			}
			end := i + 2
			if strings.HasPrefix(statement[start:end], "/*vt+") {
				// Match the pinned parser: whitespace-delimited tokens, key before '='.
				fields := strings.Fields(statement[start:end])
				for _, field := range fields[1 : len(fields)-1] {
					key, _, _ := strings.Cut(field, "=")
					switch strings.ToUpper(key) {
					case "SCATTER_ERRORS_AS_WARNINGS", "IGNORE_MAX_MEMORY_ROWS", "IGNORE_MAX_PAYLOAD_SIZE", "MULTI_SHARD_AUTOCOMMIT":
						return unsupported()
					}
				}
			}
			i = end
		default:
			i++
		}
	}
	return nil
}
