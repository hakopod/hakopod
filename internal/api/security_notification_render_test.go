package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecurityNotificationRendering(t *testing.T) {
	payload := map[string]any{"type": "security_advisory", "application_name": "web", "project": "demo", "environment": "production", "service": "api",
		"severity": "high", "title": "Node <issue>", "evidence": "node 22.3.0", "fix_reason": "", "link": "https://hakopod.example/a"}
	subject, text, ok := securityNotificationText(payload)
	if !ok || subject != "Security update affects web" || !strings.Contains(text, "Severity: high") || !strings.Contains(text, "Evidence: node 22.3.0") || !strings.Contains(text, "https://hakopod.example/a") || strings.Contains(text, "Next step") {
		t.Fatalf("%q\n%s", subject, text)
	}
	t.Logf("subject: %s\n%s", subject, text)
	raw, _ := json.Marshal(payload)
	slack := slackDeliveryText("security.advisory", raw)
	if !strings.HasPrefix(slack, "Hakopod Security advisory\nNode &lt;issue&gt;\n") || !strings.Contains(slack, "Node &lt;issue&gt;") || !strings.Contains(slack, "Service: api") {
		t.Fatal(slack)
	}
	t.Logf("slack:\n%s", slack)
	if _, _, ok := securityNotificationText(map[string]any{"status": "failed"}); ok {
		t.Fatal("deployment payload treated as security")
	}
	payload["type"] = "security_patch_failed"
	payload["advisory_id"], payload["error"] = "HKP-1", "rollout failed"
	if _, text, ok := securityNotificationText(payload); !ok || !strings.Contains(text, "Automatic patch failed: HKP-1\nError: rollout failed") {
		t.Fatal(text)
	}
}
