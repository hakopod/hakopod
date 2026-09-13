package spec

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestVirtualNetworkStrictConfiguration(t *testing.T) {
	v, err := ParseVirtualNetwork([]byte("schema_version=1\nname='commerce'\n[segments.data]\napplications=['orders','catalog']"))
	if err != nil || !v.Allows("data", "orders") || v.Allows("data", "other") || v.Allows("unknown", "orders") {
		t.Fatalf("invalid allowlist behavior: %v", err)
	}
	if v.Segments["data"].Applications[0] != "catalog" {
		t.Fatal("members are not canonical")
	}
	for _, raw := range []string{
		"name='bad'\n[segments.data]\napplications=['*']",
		"name='bad'\n[segments.data]\napplications=['orders','orders']",
		"schema_version=9\nname='bad'\n[segments.data]\napplications=[]",
		"name='new'\n[segments.data]\napplications=[]",
		"name='bad'\nunknown='hidden-value'\n[segments.data]\napplications=[]",
	} {
		if _, err := ParseVirtualNetwork([]byte(raw)); err == nil || strings.Contains(err.Error(), "hidden-value") {
			t.Fatalf("invalid/unsafe error for network: %v", err)
		}
	}
}

func TestApplicationVirtualNetworkRoundTripAndPeers(t *testing.T) {
	input := "name='orders'\n[networks.data]\nvirtual_network='commerce'\nsegment='data'\ninternal=true\n[services.api]\nimage='python:3.13-alpine'\nport=8080\nnetworks=['data']\n[services.api.network_access]\nfrom=[]\nfrom_applications=['catalog/web']"
	app, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := toml.Marshal(app)
	roundTrip, err := Parse(raw)
	if err != nil || roundTrip.Networks["data"].VirtualNetwork != "commerce" || roundTrip.Services["api"].NetworkAccess.FromApplications[0] != "catalog/web" {
		t.Fatal("shared networks did not round trip", err)
	}
	for _, bad := range []string{
		strings.Replace(input, "segment='data'", "segment=''", 1),
		strings.Replace(input, "catalog/web", "other/env/app/web", 1),
		strings.Replace(input, "catalog/web", "orders/api", 1),
		strings.Replace(input, "virtual_network='commerce'\nsegment='data'\n", "", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("invalid shared network accepted")
		}
	}
}
