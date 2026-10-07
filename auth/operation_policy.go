package auth

import "github.com/hakopod/hakopod/internal/operations"

// OperationRequiresCredentials reports the canonical contract credential boundary.
// The caller must separately enforce the route's normal scope and write permissions.
func OperationRequiresCredentials(method, path string) bool {
	return operations.ConcreteRouteRequiresCredentials(method, path)
}
