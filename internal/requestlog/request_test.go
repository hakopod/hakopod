package requestlog

import (
	"encoding/hex"
	"strings"
	"testing"
)

func fixture(base string) string {
	return "2026-09-19T12:00:00.123456Z HAKOPOD_REQUEST_V1|11|42|POST|HTTP/2.0|503|123|1|2|3|4|10|sH--|0|2|https~|hp-abc_api_service|SRV_1|" + hex.EncodeToString([]byte(base)) + "|TLSv1.3|203.0.113.19"
}
func TestParseRequestPrivacyAndIdentity(t *testing.T) {
	line := fixture("app.example.com/items/42?token=never-retain#secret")
	e, err := Parse(line, "pod-uid", "ingress-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != 503 || e.DurationMS != 10 || e.Host != "app.example.com" || e.Path != "/items/42" || e.ClientNetwork != "203.0.113.0/24" || e.Backend != "hp-abc_api_service" {
		t.Fatalf("unexpected record: %+v", e)
	}
	if strings.Contains(string(e.JSON()), "never-retain") || strings.Contains(string(e.JSON()), "203.0.113.19") {
		t.Fatal("private input retained")
	}
	same, _ := Parse(line, "pod-uid", "ingress-1")
	other, _ := Parse(line, "other-pod", "ingress-1")
	if same.ID != e.ID || other.ID == e.ID {
		t.Fatal("deduplication identity incorrect")
	}
	for _, bad := range []string{"not a log", strings.Replace(line, "|503|", "|bogus|", 1), strings.Replace(line, "|123|", "|-2|", 1), fixture("host/path\nInjected"), line + "|extra"} {
		if _, err := Parse(bad, "uid", "pod"); err == nil {
			t.Fatal("accepted malformed record")
		}
	}
}
func TestIngressFormatDoesNotCaptureSecrets(t *testing.T) {
	for _, bad := range []string{"%r ", "%HU", "%hr", "%hs", "req.hdr", "req.body", "query", "%ci"} {
		if strings.Contains(Format, bad) {
			t.Fatalf("unsafe log format %s", bad)
		}
	}
}
