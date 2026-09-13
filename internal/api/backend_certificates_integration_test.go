package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestBackendCertificateRoutesEnforceApplicationScope(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	root, err := db.Bootstrap(ctx, "certificate-operator")
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := db.Authenticate(ctx, root)
	app, _ := spec.Parse([]byte("name='mail'\n[services.smtp]\nimage='python:3.13-alpine'"))
	initial, err := db.Accept(ctx, principal, "demo", "development", app, 0, "certificate-api-test")
	if err != nil {
		t.Fatal(err)
	}
	token := func(name, application string, permissions ...string) string {
		t.Helper()
		_, value, err := db.CreateKey(ctx, principal, store.KeyInput{Name: name, Project: "demo", Environment: "development", Application: application, Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	reader := token("certificate-reader", "mail", "deployments:read")
	other := token("other-app", "other-app", "deployments:write", "deployments:read")
	handler := (&api.Server{Store: db}).Handler()
	for _, tc := range []struct {
		method, key, path string
		status            int
	}{
		{"POST", "", "smtp", 401},
		{"POST", reader, "smtp", 403},
		{"POST", other, "smtp", 403},
		{"GET", other, "smtp", 403},
		{"GET", reader, "smtp", 503},
		{"POST", root, "missing", 404},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/applications/"+initial.ApplicationID+"/services/"+tc.path+"/certificates", strings.NewReader(`{"hostname":"smtp.example.com","certificate_pem":"certificate","private_key_pem":"sensitive-fixture-key"}`))
		if tc.key != "" {
			r.Header.Set("Authorization", "Bearer "+tc.key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "sensitive-fixture-key") {
			t.Fatalf("%s route status=%d, want=%d; key redaction=%t", tc.method, w.Code, tc.status, !strings.Contains(w.Body.String(), "sensitive-fixture-key"))
		}
	}
}
