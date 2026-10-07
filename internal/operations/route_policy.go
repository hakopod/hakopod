package operations

import "strings"

// ConcreteRouteRequiresCredentials resolves the concrete route against the contract.
// Unknown or malformed paths match no operation and grant no route authority.
func ConcreteRouteRequiresCredentials(method, path string) bool {
	if len(path) > 4096 || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "%\\?#\x00") {
		return false
	}
	actual := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, op := range catalog {
		if op.Method != method || !op.Policy.CredentialRequired {
			continue
		}
		expected := strings.Split(strings.TrimPrefix(op.Path, "/"), "/")
		if len(actual) != len(expected) {
			continue
		}
		matches := true
		for i, part := range expected {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				if !segment.MatchString(actual[i]) || actual[i] == "." || actual[i] == ".." {
					matches = false
					break
				}
			} else if part != actual[i] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
