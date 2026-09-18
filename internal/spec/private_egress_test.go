package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestPrivateEgressReferences(t *testing.T) {
	app, err := Parse([]byte(minimum + `private_egress = ["orders-db", "cache"]`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.Services["web"].PrivateEgress, []string{"cache", "orders-db"}) {
		t.Fatal("references are not canonical")
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, toml.Marshal} {
		raw, err := marshal(app)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip Application
		if raw[0] == '{' {
			err = json.Unmarshal(raw, &roundtrip)
		} else {
			roundtrip, err = Parse(raw)
		}
		if err != nil || !reflect.DeepEqual(app.Services["web"].PrivateEgress, roundtrip.Services["web"].PrivateEgress) {
			t.Fatal("round trip lost grant", err)
		}
	}
	before, _ := Parse([]byte(minimum))
	found := false
	for _, change := range Diff(&before, app) {
		found = found || change.Field == "private_egress" && change.Service == "web"
	}
	if !found {
		t.Fatal("grant missing from review diff")
	}
	for _, value := range []string{`["orders-db", "orders-db"]`, `["*"]`, `["10.20.30.0/24"]`, `["https://db.example"]`, `[""]`, `[` + strings.Repeat(`"orders-db",`, 16) + `"cache"]`} {
		if _, err := Parse([]byte(minimum + "private_egress = " + value)); err == nil {
			t.Fatalf("invalid references accepted: %s", value)
		}
	}
}
