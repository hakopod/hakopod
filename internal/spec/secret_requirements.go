package spec

import (
	"sort"
	"strings"
)

// LocalSecretNames covers inherited defaults, files and generated connection
// bindings, including jobs. External provider references keep their own resolver.
func LocalSecretNames(app Application) []string {
	seen := map[string]bool{}
	for _, service := range RuntimeEnvironment(app).Services {
		for _, ref := range SecretReferences(service) {
			if ref.Ref != "" {
				seen[ref.Ref] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type MissingSecretsError struct {
	Names []string `json:"missing_secrets"`
}

func (e *MissingSecretsError) Error() string {
	return "Save the missing application secrets before deploying: " + strings.Join(e.Names, ", ")
}
