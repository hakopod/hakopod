package spec

import "strings"

// SecretRef contains locations only. Credentials and resolved values never enter
// a specification or immutable deployment history.
type SecretRef struct {
	Ref      string `json:"ref,omitempty" toml:"ref,omitempty"`
	Provider string `json:"provider,omitempty" toml:"provider,omitempty"`
	Path     string `json:"path,omitempty" toml:"path,omitempty"`
	Key      string `json:"key,omitempty" toml:"key,omitempty"`
}

func (r SecretRef) Valid() bool {
	if r.Ref != "" {
		return namePattern.MatchString(r.Ref) && r.Provider == "" && r.Path == "" && r.Key == ""
	}
	return namePattern.MatchString(r.Provider) && ValidSecretPath(r.Path) && len(r.Key) > 0 && len(r.Key) <= 128 && secretPathSegment(r.Key)
}

// ValidSecretPath accepts an empty root or a relative path without escapes,
// traversal, query syntax or ambiguous separators.
func ValidSecretPath(path string) bool {
	if len(path) > 512 {
		return false
	}
	if path == "" {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if !secretPathSegment(segment) {
			return false
		}
	}
	return true
}

func secretPathSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
