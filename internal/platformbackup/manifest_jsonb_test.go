package platformbackup

import (
	"encoding/json"
	"testing"
)

func TestManifestDigestSurvivesJSONBSpecRoundTrip(t *testing.T) {
	first, _ := fixtureManifest()
	first.PlatformSpec = json.RawMessage(`{"schema_version":1,"kind":"supabase","config":{"large":9007199254740993,"name":"example"}}`)
	second := first
	second.PlatformSpec = json.RawMessage(`{ "config": { "name": "example", "large": 9007199254740993 }, "kind": "supabase", "schema_version": 1 }`)
	if first.Digest() != second.Digest() {
		t.Fatal("JSON object order and whitespace changed the recovery identity")
	}
	second.ManifestSHA256 = first.Digest()
	if err := second.Validate(); err != nil {
		t.Fatal(err)
	}
	second.PlatformSpec = json.RawMessage(`{"schema_version":1,"kind":"supabase","config":{"large":9007199254740992,"name":"example"}}`)
	if first.Digest() == second.Digest() {
		t.Fatal("distinct exact integer spec values shared a recovery identity")
	}
}
