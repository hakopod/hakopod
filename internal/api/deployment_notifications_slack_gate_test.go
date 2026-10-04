package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/license"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type notificationSlackGateFixture struct {
	db     *store.Store
	server *Server
	h      http.Handler
	app    store.Application
	token  string
	key    ed25519.PrivateKey
}

func newNotificationSlackGateFixture(t *testing.T, mode string) notificationSlackGateFixture {
	t.Helper()
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "notification-slack-gate")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := spec.Parse([]byte("name='notification-slack-gate'\n[services.api]\nimage='nginx:alpine'"))
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := db.Accept(ctx, principal, "demo", "development", parsed, 0, "notification-slack-gate")
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, accepted.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db.LicenseVerifier = license.NewVerifier(map[string]ed25519.PublicKey{"notification-slack-gate": public})
	server := &Server{Store: db, Auth: AuthConfig{DeploymentMode: mode, EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))}}
	return notificationSlackGateFixture{db: db, server: server, h: server.Handler(), app: app, token: token, key: key}
}

func (f notificationSlackGateFixture) grant(t *testing.T, features []string, expired bool) {
	t.Helper()
	ctx := context.Background()
	status, err := f.db.LicenseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	expires := now + 3600
	if expired {
		expires = now - 1
	}
	claims := license.Claims{Version: 1, KeyID: "notification-slack-gate", LicenseID: strings.Repeat("a", 32), InstallationID: status.InstallationID, Customer: "Notification Slack gate", Plan: "pro", Sequence: status.Sequence + 1, IssuedAt: now - 60, NotBefore: now - 60, ExpiresAt: expires, Features: features}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	token := "hl1." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.key, append([]byte(license.Domain), payload...)))
	digest := sha256.Sum256([]byte(token))
	if _, err = f.db.Pool.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=$3,revision=revision+1 WHERE singleton", token, digest[:], claims.Sequence); err != nil {
		t.Fatal(err)
	}
}

func (f notificationSlackGateFixture) call(t *testing.T, method, suffix string, body any, want int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/applications/"+f.app.ID+"/notifications"+suffix, strings.NewReader(string(store.JSON(body))))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, suffix, w.Code, want, w.Body.String())
	}
	return result
}

func notificationInput(kind, destination string, enabled bool, revision int64) map[string]any {
	return map[string]any{"name": kind + " target", "kind": kind, "destination": destination, "signing_secret": strings.Repeat("s", 32), "enabled": enabled, "events": []string{"failed"}, "expected_revision": revision}
}

func TestLegacySlackWebhookGateSelfHostedLifecycle(t *testing.T) {
	f := newNotificationSlackGateFixture(t, cluster.DeploymentSelfHosted)
	slack := "https://hooks.slack.com/services/FIXTURE/ONLY/TOKEN"
	generic := notificationInput("webhook", slack, true, 0)
	if got := f.call(t, http.MethodPost, "", generic, http.StatusPaymentRequired); got["error"].(map[string]any)["code"] != "license_required" {
		t.Fatal(got)
	}
	paused := notificationInput("slack", slack, false, 0)
	f.call(t, http.MethodPost, "", paused, http.StatusPaymentRequired)
	// Slack hosting is detected from a generic webhook URL too; a lookalike host
	// stays an ordinary signed webhook.
	other := notificationInput("webhook", "https://hooks.not-slack.com/services/FIXTURE", true, 0)
	f.call(t, http.MethodPost, "", other, http.StatusCreated)

	f.grant(t, []string{store.SlackFeature}, false)
	created := f.call(t, http.MethodPost, "", generic, http.StatusCreated)
	if created["slack_managed"] != true {
		t.Fatalf("generic Slack target did not receive safe classification: %#v", created)
	}
	id := created["id"].(string)
	revision := int64(created["revision"].(float64))

	// The update omits both secret fields. The server must gate the saved,
	// decrypted generic destination instead of treating the blank request fields
	// as a non-Slack webhook.
	f.grant(t, nil, false)
	update := notificationInput("webhook", "", true, revision)
	update["signing_secret"] = ""
	if got := f.call(t, http.MethodPut, "/"+id, update, http.StatusPaymentRequired); got["error"].(map[string]any)["code"] != "license_required" {
		t.Fatal(got)
	}
	f.call(t, http.MethodPost, "/"+id+"/test", map[string]int64{"expected_revision": revision}, http.StatusPaymentRequired)

	// Administrators can still disable or delete an existing direct Slack target
	// after Pro expires. Other channel maintenance remains unaffected.
	update["enabled"] = false
	update["name"] = "webhook target"
	updated := f.call(t, http.MethodPut, "/"+id, update, http.StatusOK)
	f.call(t, http.MethodDelete, "/"+id, map[string]int64{"expected_revision": int64(updated["revision"].(float64))}, http.StatusOK)

	f.grant(t, []string{store.SlackFeature}, true)
	f.call(t, http.MethodPost, "", notificationInput("slack", slack, true, 0), http.StatusPaymentRequired)
}

func TestLegacySlackWebhookGateManagedCloudAndDelivery(t *testing.T) {
	f := newNotificationSlackGateFixture(t, cluster.DeploymentManagedCloud)
	f.grant(t, []string{store.SlackFeature}, false)
	slack := "https://hooks.slack.com/services/FIXTURE/ONLY/TOKEN"
	got := f.call(t, http.MethodPost, "", notificationInput("webhook", slack, true, 0), http.StatusConflict)
	if got["error"].(map[string]any)["code"] != "slack_managed_by_cloud" || !strings.Contains(got["error"].(map[string]any)["message"].(string), "Settings → Integrations → Slack") {
		t.Fatal(got)
	}

	// A target saved while Pro was active must not make a network request after a
	// downgrade. The delivery gate resolves its application from the target row.
	selfHosted := newNotificationSlackGateFixture(t, cluster.DeploymentSelfHosted)
	selfHosted.grant(t, []string{store.SlackFeature}, false)
	created := selfHosted.call(t, http.MethodPost, "", notificationInput("webhook", slack, true, 0), http.StatusCreated)
	selfHosted.grant(t, nil, false)
	called := false
	client := &http.Client{Transport: notificationRoundTrip(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}
	outcome, message := selfHosted.server.deliverNotification(context.Background(), client, store.NotificationDelivery{TargetID: created["id"].(string), TargetRevision: int64(created["revision"].(float64)), Payload: store.JSON(map[string]any{"application_id": "untrusted-payload-app", "status": "failed"})})
	if outcome != "skipped" || !strings.Contains(message, "Pro") || called {
		t.Fatalf("outcome=%q message=%q called=%v", outcome, message, called)
	}
}
