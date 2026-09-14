package auth

import (
	"net/http/httptest"
	"testing"
)

func TestIdentityRouteBoundary(t *testing.T) {
	for _, v := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/v1/auth/status", true}, {"POST", "/api/v1/auth/login", true},
		{"GET", "/api/v1/auth/oauth/github/callback", true}, {"GET", "/api/v1/auth/oauth/google/start", true}, {"GET", "/api/v1/auth/oauth/gitlab/start", true},
		{"DELETE", "/api/v1/auth/sessions/abc", true}, {"GET", "/api/v1/applications", false}, {"POST", "/api/v1/deployments", false},
		{"GET", "/api/v1/users", false}, {"POST", "/api/v1/teams", false}, {"GET", "/api/v1/installation/login-providers/github", false},
		{"POST", "/api/v1/auth/onboarding", false}, {"GET", "/api/v1/auth/oauth/unknown/start", false}, {"GET", "/api/v1/auth/sessions/abc", false},
	} {
		if got := identityRoute(httptest.NewRequest(v.method, v.path, nil)); got != v.allowed {
			t.Errorf("%s %s allowed=%v", v.method, v.path, got)
		}
	}
}
