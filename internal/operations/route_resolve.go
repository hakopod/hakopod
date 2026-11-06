package operations

import "strings"

// ResolveRoute finds the contract operation for a concrete request path given
// without the /api/v1 prefix. When a literal segment and a parameter both fit,
// the operation with more literal segments wins. Category is empty for
// operations the contract leaves unclassified.
func ResolveRoute(method, path string) (id, category string, op Operation, ok bool) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\?#\x00") {
		return "", "", Operation{}, false
	}
	return ResolveSegments(method, strings.Split(strings.TrimPrefix(path, "/"), "/"))
}

// ResolveSegments is ResolveRoute for a path already split on "/" and
// unescaped segment by segment, as http.ServeMux matches an escaped path. A
// segment holding an escaped slash therefore stays one segment.
func ResolveSegments(method string, actual []string) (id, category string, op Operation, ok bool) {
	total := 0
	for _, part := range actual {
		total += len(part) + 1
	}
	if total > 4096 || len(actual) > 64 {
		return "", "", Operation{}, false
	}
	best := -1
	for _, candidate := range catalog {
		if candidate.Method != method {
			continue
		}
		expected := strings.Split(strings.TrimPrefix(candidate.Path, "/"), "/")
		if len(expected) != len(actual) {
			continue
		}
		literals := 0
		for i, part := range expected {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				if !segment.MatchString(actual[i]) || actual[i] == "." || actual[i] == ".." {
					literals = -1
					break
				}
			} else if part == actual[i] {
				literals++
			} else {
				literals = -1
				break
			}
		}
		if literals > best {
			best, op, ok = literals, candidate, true
		}
	}
	if !ok {
		return "", "", Operation{}, false
	}
	category = op.Policy.Category
	if category == "unclassified" {
		category = ""
	}
	return op.ID, category, op, true
}

// OperationByID returns the contract operation with this id.
func OperationByID(id string) (Operation, bool) {
	for _, op := range catalog {
		if op.ID == id {
			return op, true
		}
	}
	return Operation{}, false
}
