package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/slackevents"
	"github.com/hakopod/hakopod/internal/store"
)

func TestSlackManifestAndCallbackRequirePublicHTTPSOrigin(t *testing.T) {
	for _, origin := range []string{"", "http://dashboard.example", "https://dashboard.example/path", "https://dashboard.example?x=1", "https://user@dashboard.example"} {
		if _, err := (&Server{Auth: AuthConfig{PublicURL: origin}}).slackCallbackURL(); err == nil {
			t.Fatalf("unsafe callback origin accepted: %q", origin)
		}
	}
	s := &Server{Auth: AuthConfig{PublicURL: "https://dashboard.example"}}
	callback, err := s.slackCallbackURL()
	if err != nil || callback != "https://dashboard.example/api/integrations/slack/callback" {
		t.Fatalf("callback=%q err=%v", callback, err)
	}
	manifest := s.slackManifest()
	bot, ok := manifest.Features["bot_user"].(map[string]any)
	if !ok || bot["display_name"] != "Hakopod" || bot["always_online"] != false {
		t.Fatalf("manifest does not define bot user: %#v", manifest.Features)
	}
	config := s.slackOAuthConfig(slackClientConfiguration{ClientID: "id", ClientSecret: "secret"})
	if config.Endpoint.AuthURL != "https://slack.com/oauth/v2/authorize" || config.Endpoint.TokenURL != "https://slack.com/api/oauth.v2.access" || config.RedirectURL != callback {
		t.Fatalf("Slack endpoints or callback changed: %#v", config)
	}
	authorize, err := url.Parse(slackAuthorizationURL(config, "state"))
	if err != nil || authorize.Query().Get("scope") != "chat:write,channels:read,groups:read" {
		t.Fatalf("Slack scope encoding=%q err=%v", authorize.Query().Get("scope"), err)
	}
}

func TestSlackOAuthScopeAndCloudEventPayloadValidation(t *testing.T) {
	if !slackScopesGranted("chat:write,channels:read,groups:read") || slackScopesGranted("chat:write channels:read") {
		t.Fatal("OAuth scope validation incorrect")
	}
	payload := json.RawMessage(`{"project":"demo","environment":"production","summary":"safe"}`)
	firstJob := store.SlackCloudDelivery{SourceID: "shared-a", Kind: "alarm.opened", SourceEventID: 41, Payload: payload}
	event, err := cloudSlackEvent(firstJob)
	if err != nil || !strings.HasPrefix(event.ID, "slack:") || len(event.ID) != 70 || event.Project != "demo" {
		t.Fatalf("valid Cloud event rejected: %#v %v", event, err)
	}
	retried, err := cloudSlackEvent(firstJob)
	if err != nil || retried.ID != event.ID {
		t.Fatalf("Cloud event ID changed across retry: %#v %v", retried, err)
	}
	longSource := strings.Repeat("s", 128)
	longEvent, err := cloudSlackEvent(store.SlackCloudDelivery{SourceID: longSource, Kind: "deployment.cancellation.requested", SourceEventID: 99, Payload: payload})
	if err != nil || len(longEvent.ID) > 160 || longEvent.ID == event.ID {
		t.Fatalf("Cloud event ID is not safely bounded and distinct: %#v %v", longEvent, err)
	}
	encoded, err := json.Marshal(event)
	if err != nil || !strings.Contains(string(encoded), `"source_id":"shared-a"`) || strings.Contains(string(encoded), `"SourceID"`) {
		t.Fatalf("Cloud relay wire envelope changed: %s %v", encoded, err)
	}
	if _, err = cloudSlackEvent(store.SlackCloudDelivery{SourceID: "shared-a", Kind: "audit", SourceEventID: 42, Payload: json.RawMessage(`{"project":123}`)}); err == nil {
		t.Fatal("malformed Cloud event payload accepted")
	}
	if _, err = cloudSlackEvent(store.SlackCloudDelivery{SourceID: "shared-a", Kind: "audit", SourceEventID: 43, Payload: json.RawMessage(`{"project":"demo","environment":""}`)}); err == nil {
		t.Fatal("unscoped Cloud event payload accepted")
	}
}

type slackRoundTrip func(*http.Request) (*http.Response, error)

func (f slackRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSlackPostMessageUsesSafePayloadAndClassifiesProviderResponses(t *testing.T) {
	var sent map[string]any
	client := &http.Client{Transport: slackRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://slack.com/api/chat.postMessage" {
			t.Fatalf("unexpected Slack request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer xoxb-fixture" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "Hakopod-Slack/1" {
			t.Fatalf("unexpected Slack headers: %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	outcome, message, retry := postSlackMessage(context.Background(), client, "xoxb-fixture", "Cfixture", slackDeliveryText("alarm.opened", store.JSON(map[string]any{"summary": "failed command includes a secret", "project": "demo", "environment": "development", "application_id": "app-1", "resource_name": "api", "alarm_id": 7})))
	if outcome != "sent" || message != "" || retry != 0 {
		t.Fatalf("unexpected success outcome: %q %q %v", outcome, message, retry)
	}
	if sent["channel"] != "Cfixture" || sent["mrkdwn"] != false || sent["parse"] != "none" || sent["unfurl_links"] != false || sent["unfurl_media"] != false || sent["text"] != "Hakopod alarm opened\nProject: demo\nEnvironment: development\nApplication ID: app-1\nResource: api\nAlarm ID: 7" {
		t.Fatalf("unsafe or rich-text Slack body: %#v", sent)
	}
	if strings.Contains(sent["text"].(string), "secret") {
		t.Fatalf("sensitive alarm summary escaped the Slack message: %#v", sent)
	}

	for _, tc := range []struct {
		name             string
		status           int
		body, retryAfter string
		want             string
		wantRetry        time.Duration
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"ok":false,"error":"ratelimited"}`, retryAfter: "120", want: "pending", wantRetry: 2 * time.Minute},
		{name: "provider failure", status: http.StatusServiceUnavailable, body: `{"ok":false}`, want: "pending"},
		{name: "slack transient error", status: http.StatusOK, body: `{"ok":false,"error":"internal_error"}`, want: "pending"},
		{name: "rejected OAuth token", status: http.StatusOK, body: `{"ok":false,"error":"invalid_auth"}`, want: "failed"},
		{name: "redirect", status: http.StatusFound, body: ``, want: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: slackRoundTrip(func(*http.Request) (*http.Response, error) {
				header := make(http.Header)
				if tc.retryAfter != "" {
					header.Set("Retry-After", tc.retryAfter)
				}
				return &http.Response{StatusCode: tc.status, Header: header, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			outcome, message, retry := postSlackMessage(context.Background(), client, "xoxb-fixture", "Cfixture", "safe")
			if outcome != tc.want || retry != tc.wantRetry || strings.Contains(message, "xoxb-fixture") {
				t.Fatalf("outcome=%q retry=%v message=%q", outcome, retry, message)
			}
		})
	}
}

func TestSlackDeliveryTextCoversCatalogWithSafeRuntimeContext(t *testing.T) {
	payload := store.JSON(map[string]any{
		"project": "demo", "environment": "development", "application_id": "app_01H8R23PNRQRQ2MGZ1V6N1M7XV", "application_name": "api", "service": "worker",
		"deployment_id": "deployment-1", "revision": 7, "runtime_phase": "recovery", "recovery_revision": 6,
	})
	for _, definition := range slackevents.Catalog() {
		text := slackDeliveryText(definition.ID, payload)
		if text == "" {
			t.Fatalf("catalog event %q did not render", definition.ID)
		}
		if definition.ID == "alarm.opened" || definition.ID == "alarm.resolved" || definition.ID == "audit" {
			continue
		}
		if !strings.Contains(text, "Hakopod "+definition.Label) || !strings.Contains(text, "Application ID: app_01H8R23PNRQRQ2MGZ1V6N1M7XV") || !strings.Contains(text, "Runtime phase: recovery") || !strings.Contains(text, "Revision: 7") || !strings.Contains(text, "Recovery revision: 6") {
			t.Fatalf("catalog event %q rendered incomplete text: %q", definition.ID, text)
		}
	}
	if text := slackDeliveryText("service.image.updated", store.JSON(map[string]any{"project": "<unsafe>&\x00", "runtime_phase": "not-a-phase", "revision": -1})); strings.Contains(text, "not-a-phase") || !strings.Contains(text, "&lt;unsafe&gt;&amp;") {
		t.Fatalf("unsafe Slack context rendered: %q", text)
	}
	if text := slackDeliveryText("not-a-real-event", payload); text != "" {
		t.Fatalf("unknown event rendered: %q", text)
	}
}
