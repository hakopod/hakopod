package api_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

func TestSlackSettingsGateEntitlementAdminAndOAuthState(t *testing.T) {
	h := newAuthHarness(t, func(config *api.AuthConfig) { config.PublicURL = "https://dashboard.example.test" })
	owner := h.owner()

	status := h.call("GET", "/integrations/slack", owner, nil, 200)
	if status["available"] != false || status["configured"] != false || status["reason"] != "license_required" {
		t.Fatalf("unlicensed Slack status disclosed incorrect setup: %#v", status)
	}
	h.call("POST", "/integrations/slack/connect", owner, map[string]string{"client_id": "fixture-id", "client_secret": "fixture-secret"}, 402)

	// A project member never receives installation-wide integration access.
	invite := h.call("POST", "/projects/demo/invites", owner, map[string]string{"email": "slack-viewer@example.test", "role": "viewer"}, 201)
	inviteURL, err := url.Parse(invite["invite_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	viewer := h.call("POST", "/auth/invites/accept", "", map[string]string{"token": inviteURL.Query().Get("token"), "name": "Slack viewer", "password": "viewer fixture password"}, 200)["token"].(string)
	h.call("GET", "/integrations/slack", viewer, nil, 403)

	h.grantAccess(owner, store.SlackFeature)
	status = h.call("GET", "/integrations/slack", owner, nil, 200)
	if status["available"] != true || status["setup_available"] != true || status["manifest"] == nil {
		t.Fatalf("licensed Slack setup unavailable: %#v", status)
	}
	connect := h.call("POST", "/integrations/slack/connect", owner, map[string]string{"client_id": "fixture-id", "client_secret": "fixture-secret"}, 200)
	authorizationURL, err := url.Parse(connect["authorization_url"].(string))
	if err != nil || authorizationURL.Host != "slack.com" || authorizationURL.Path != "/oauth/v2/authorize" {
		t.Fatalf("bad Slack authorization URL: %q %v", connect["authorization_url"], err)
	}
	state := authorizationURL.Query().Get("state")
	if state == "" {
		t.Fatal("missing OAuth state")
	}
	// The challenge is bound to the precise initiating browser session. This
	// check runs before any Slack token exchange and consumes the one-use state.
	secondSession := h.call("POST", "/auth/login", "", map[string]string{"email": "owner@example.test", "password": "correct horse battery staple"}, 200)["token"].(string)
	callback := "/integrations/slack/callback?state=" + url.QueryEscape(state) + "&code=fixture-code"
	h.call("GET", callback, secondSession, nil, 401)
	h.call("GET", callback, owner, nil, 401)
}

func TestExpiredSlackConfigurationRemainsVisibleAndCanDisconnect(t *testing.T) {
	h := newAuthHarness(t, func(config *api.AuthConfig) { config.PublicURL = "https://dashboard.example.test" })
	owner := h.owner()
	h.grantAccess(owner, store.SlackFeature)
	principal, err := h.db.Authenticate(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := h.db.SlackIntegrationGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	configured, err := h.db.SaveSlackIntegration(context.Background(), principal, store.SlackIntegration{
		TeamID: "Tfixture", TeamName: "Fixture team", Events: []string{"alarm.opened"}, ClientConfig: []byte("encrypted-client"), BotConfig: []byte("encrypted-bot"),
	}, generation)
	if err != nil {
		t.Fatal(err)
	}
	configured, err = h.db.SetSlackChannel(context.Background(), principal, "Cfixture", "fixture", false, configured.Events, configured.Revision)
	if err != nil {
		t.Fatal(err)
	}
	testRequest := h.call("POST", "/integrations/slack/test", owner, map[string]int64{"expected_revision": configured.Revision}, 202)
	var snappedRevision int64
	if err = h.db.Pool.QueryRow(context.Background(), "SELECT integration_revision FROM slack_event_outbox WHERE id=$1", testRequest["id"].(string)).Scan(&snappedRevision); err != nil || snappedRevision != configured.Revision {
		t.Fatalf("test delivery did not snapshot Slack revision: %d %v", snappedRevision, err)
	}
	licenseStatus := h.call("GET", "/license", owner, nil, 200)
	claims := testLicenseClaims(licenseStatus["installation_id"].(string), int64(licenseStatus["sequence"].(float64))+1)
	claims.Features = []string{store.SlackFeature}
	claims.IssuedAt = time.Now().Add(-2 * time.Hour).Unix()
	claims.NotBefore = claims.IssuedAt
	claims.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	raw := testSignedLicense(h.licenseKey, claims)
	digest := sha256.Sum256([]byte(raw))
	if _, err = h.db.Pool.Exec(context.Background(), "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=$3 WHERE singleton", raw, digest[:], claims.Sequence); err != nil {
		t.Fatal(err)
	}
	status := h.call("GET", "/integrations/slack", owner, nil, 200)
	if status["configured"] != true || status["available"] != false || status["reason"] != "license_required" || int64(status["revision"].(float64)) != configured.Revision {
		t.Fatalf("expired connection was hidden or exposed as usable: %#v", status)
	}
	h.call("DELETE", "/integrations/slack", owner, map[string]int64{"expected_revision": configured.Revision}, 200)
	if _, err := h.db.SlackIntegration(context.Background()); err == nil {
		t.Fatal("expired Slack integration was not removed")
	}
}

func TestSlackGenerationFencesStaleReconnectAfterEditAndDisconnect(t *testing.T) {
	h := newAuthHarness(t, func(config *api.AuthConfig) { config.PublicURL = "https://dashboard.example.test" })
	owner := h.owner()
	h.grantAccess(owner, store.SlackFeature)
	ctx := context.Background()
	principal, err := h.db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := h.db.SlackIntegrationGeneration(ctx)
	if err != nil || generation != 0 {
		t.Fatalf("initial Slack generation=%d err=%v", generation, err)
	}
	first, err := h.db.SaveSlackIntegration(ctx, principal, store.SlackIntegration{
		TeamID: "Tone", TeamName: "First workspace", Events: []string{"alarm.opened"}, ClientConfig: []byte("first-client"), BotConfig: []byte("first-bot"),
	}, generation)
	if err != nil || first.Revision != 1 {
		t.Fatalf("initial Slack connect=%#v err=%v", first, err)
	}
	edited, err := h.db.SetSlackEvents(ctx, principal, []string{"alarm.opened", "audit"}, first.Revision)
	if err != nil || edited.Revision != 2 {
		t.Fatalf("Slack edit=%#v err=%v", edited, err)
	}
	// This models an OAuth callback that began before a newer edit.
	if _, err = h.db.SaveSlackIntegration(ctx, principal, store.SlackIntegration{
		TeamID: "Tstale", TeamName: "Stale workspace", Events: []string{"alarm.opened"}, ClientConfig: []byte("stale-client"), BotConfig: []byte("stale-bot"),
	}, first.Revision); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale reconnect replaced newer settings: %v", err)
	}
	if err = h.db.DeleteSlackIntegration(ctx, principal, edited.Revision); err != nil {
		t.Fatal(err)
	}
	generation, err = h.db.SlackIntegrationGeneration(ctx)
	if err != nil || generation != 3 {
		t.Fatalf("disconnect did not advance persistent generation: %d %v", generation, err)
	}
	// The same stale callback must not recreate a connection after disconnect.
	if _, err = h.db.SaveSlackIntegration(ctx, principal, store.SlackIntegration{
		TeamID: "Tstale", TeamName: "Stale workspace", Events: []string{"alarm.opened"}, ClientConfig: []byte("stale-client"), BotConfig: []byte("stale-bot"),
	}, edited.Revision); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale reconnect recreated removed integration: %v", err)
	}
	current, err := h.db.SaveSlackIntegration(ctx, principal, store.SlackIntegration{
		TeamID: "Tnew", TeamName: "New workspace", Events: []string{"alarm.resolved"}, ClientConfig: []byte("new-client"), BotConfig: []byte("new-bot"),
	}, generation)
	if err != nil || current.Revision != 4 {
		t.Fatalf("fresh reconnect did not use monotonic generation: %#v %v", current, err)
	}
}
