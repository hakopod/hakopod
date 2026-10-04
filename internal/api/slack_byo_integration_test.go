package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

func slackRelayCall(t *testing.T, h *authHarness, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(store.JSON(body))
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&result)
	return response.StatusCode, result
}

func TestSlackBYORelayRequiresManagedCloudAndDedicatedScopedKey(t *testing.T) {
	selfHosted := newAuthHarness(t, nil)
	owner := selfHosted.owner()
	selfHosted.call("PUT", "/integrations/slack/cloud-events/source", owner, map[string]string{"source_id": "relay-a", "project": "demo", "environment": "development"}, http.StatusNotFound)

	h := newAuthHarness(t, func(config *api.AuthConfig) { config.DeploymentMode = cluster.DeploymentManagedCloud })
	owner = h.owner()
	// This node has no local signed Slack entitlement. Cloud, rather than this
	// relay node, enforces the workspace's Pro entitlement at delivery time.
	h.call("PUT", "/integrations/slack/cloud-events/source", owner, map[string]string{"source_id": "relay-a", "project": "demo", "environment": "development"}, http.StatusOK)
	principal, err := h.db.Authenticate(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	_, relay, err := h.db.CreateKey(context.Background(), principal, store.KeyInput{Name: "Slack relay", Project: "demo", Environment: "development", Permissions: []string{"slack:relay"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := slackRelayCall(t, h, "PUT", "/integrations/slack/cloud-events/source", relay, map[string]string{"source_id": "relay-b", "project": "demo", "environment": "development"}); status != http.StatusForbidden {
		t.Fatalf("relay key configured source: status=%d", status)
	}
	if _, err := h.db.Pool.Exec(context.Background(), "INSERT INTO environments(project,name) VALUES('demo','production') ON CONFLICT DO NOTHING"); err != nil {
		t.Fatal(err)
	}
	_, otherScope, err := h.db.CreateKey(context.Background(), principal, store.KeyInput{Name: "Other relay", Project: "demo", Environment: "production", Permissions: []string{"slack:relay"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := slackRelayCall(t, h, "POST", "/integrations/slack/cloud-events/claim", otherScope, nil); status != http.StatusForbidden {
		t.Fatalf("wrong-scope relay claimed event: status=%d", status)
	}
	if _, err = h.db.Pool.Exec(context.Background(), "INSERT INTO slack_event_outbox(delivery_target,runtime_source_id,event_kind,source_id,payload) VALUES('cloud','relay-a','audit',41,$1)", store.JSON(map[string]any{"project": "demo", "environment": "development", "event_id": 41})); err != nil {
		t.Fatal(err)
	}
	status, claim := slackRelayCall(t, h, "POST", "/integrations/slack/cloud-events/claim", relay, nil)
	if status != http.StatusOK || claim["delivery_id"] == "" || claim["lease_id"] == "" {
		t.Fatalf("dedicated relay did not receive its event: status=%d body=%#v", status, claim)
	}
	if status, _ = slackRelayCall(t, h, "POST", "/integrations/slack/cloud-events/ack", otherScope, map[string]any{"delivery_id": claim["delivery_id"], "lease_id": claim["lease_id"], "outcome": "accepted"}); status != http.StatusForbidden {
		t.Fatalf("wrong-scope relay acknowledged event: status=%d", status)
	}
	if status, _ = slackRelayCall(t, h, "POST", "/integrations/slack/cloud-events/ack", relay, map[string]any{"delivery_id": claim["delivery_id"], "lease_id": claim["lease_id"], "outcome": "accepted"}); status != http.StatusOK {
		t.Fatalf("dedicated relay acknowledgement failed: status=%d", status)
	}
}
