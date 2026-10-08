package cluster

import "strings"

// MyDuck's PostgreSQL endpoint accepts multiple statements in an extended Parse.
// Reject ambiguous text before dispatch; values belong in bound parameters.
func myduckSingleStatement(sql string) bool {
	ended, seen := false, false
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if i+1 < len(sql) && sql[i:i+2] == "--" {
			j := strings.IndexAny(sql[i+2:], "\r\n")
			if j < 0 {
				return seen
			}
			i += j + 3
			continue
		}
		if i+1 < len(sql) && sql[i:i+2] == "/*" {
			depth := 1
			i += 2
			for i < len(sql) && depth > 0 {
				if i+1 < len(sql) && sql[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(sql) && sql[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return false
			}
			continue
		}
		if ended {
			return false
		}
		if c == ';' {
			if !seen {
				return false
			}
			ended = true
			i++
			continue
		}
		seen = true
		if c == '`' || c == '\\' || c >= 128 || c == 0 {
			return false
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			j := i + 1
			for j < len(sql) && (sql[j] >= 'a' && sql[j] <= 'z' || sql[j] >= 'A' && sql[j] <= 'Z' || sql[j] >= '0' && sql[j] <= '9' || sql[j] == '_' || sql[j] == '$') {
				j++
			}
			if j < len(sql) && (sql[j] == '\'' || sql[j] == '"' || sql[j] == '&') {
				return false
			}
			i = j
			continue
		}
		if c == '\'' || c == '"' {
			quote := c
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == '\\' {
					return false
				}
				if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return false
			}
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			if j == i+1 || j < len(sql) && (sql[j] == '$' || sql[j] == '_' || sql[j] >= 'a' && sql[j] <= 'z' || sql[j] >= 'A' && sql[j] <= 'Z') {
				return false
			}
			i = j
			continue
		}
		i++
	}
	return seen
}
