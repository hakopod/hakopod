package api_test

import (
	"context"
	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"net/http/httptest"
	"testing"
)

func TestCloudOperatorSavedSMTPAndLoginProviders(t *testing.T) {
	h := newAuthHarness(t, nil)
	token := h.owner()
	h.config.DeploymentMode = cluster.DeploymentManagedCloud
	h.config.SMTPAllowDelivery = true
	h.config.SMTPAddress = "smtp.example.test:587"
	h.config.SMTPFrom = "operator@example.test"
	h.config.GitHubClientID = "fixture-github-client"
	h.config.GitHubClientSecret = "fixture-github-secret"
	h.server = httptest.NewServer((&api.Server{Store: h.db, Auth: h.config, OperatorRuntime: true, CloudControlPlane: true}).Handler())
	t.Cleanup(h.server.Close)
	if _, err := h.db.Pool.Exec(context.Background(), "UPDATE installation_license SET token='',token_digest=NULL"); err != nil {
		t.Fatal(err)
	}
	input := smtpInput("smtp.example.test:587", "starttls", 0)
	input["enabled"] = false
	input["password"] = "fixture-smtp-secret"
	saved := h.call("PUT", "/installation/smtp", token, input, 200)
	assertSMTPRedacted(t, saved, "fixture-smtp-secret")
	if h.call("GET", "/auth/status", "", nil, 200)["email_delivery"] != false {
		t.Fatal("Cloud ignored saved disabled mail setting")
	}
	input["enabled"] = true
	input["expected_revision"] = 1
	delete(input, "password")
	h.call("PUT", "/installation/smtp", token, input, 200)
	if h.call("GET", "/auth/status", "", nil, 200)["email_delivery"] != true {
		t.Fatal("Cloud did not enable saved mail setting")
	}
	path := "/installation/login-providers/github"
	initial := h.call("GET", path, token, nil, 200)
	if initial["enabled"] != true || initial["secret_configured"] != true {
		t.Fatal("existing operator provider not available")
	}
	provider := map[string]any{"enabled": false, "client_id": "fixture-github-client", "expected_revision": 0}
	h.call("PUT", path, token, provider, 200)
	if h.call("GET", path, token, nil, 200)["enabled"] != false {
		t.Fatal("provider was not disabled")
	}
	// The authentication handler shares persisted settings with the operator runtime.
	control := httptest.NewServer((&api.Server{Store: h.db, Auth: h.config, CloudControlPlane: true}).Handler())
	t.Cleanup(control.Close)
	prior := h.server
	h.server = control
	status := h.call("GET", "/auth/status", "", nil, 200)
	providers, _ := status["providers"].([]any)
	for _, p := range providers {
		if p == "github" {
			t.Fatal("disabled provider still advertised by sign-in")
		}
	}
	h.server = prior
	provider["enabled"] = true
	provider["expected_revision"] = 1
	h.call("PUT", path, token, provider, 200)
	// Merely running a managed-cloud customer API must not grant these controls.
	customer := httptest.NewServer((&api.Server{Store: h.db, Auth: h.config}).Handler())
	t.Cleanup(customer.Close)
	h.server = customer
	h.call("GET", "/installation/smtp", token, nil, 403)
	h.call("GET", path, token, nil, 403)
}
