package cluster

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestVitessRoutingRejectsDrift(t *testing.T) {
	want := []byte(`{"sharded":true,"vindexes":{"hash":{"type":"hash"}},"tables":{"orders":{"column_vindexes":[{"name":"hash","column":"customer_id"}]}}}`)
	good := []byte(`{"sharded":true,"vindexes":{"hash":{"type":"hash","params":{},"owner":""}},"tables":{"orders":{"columnVindexes":[{"name":"hash","column":"customer_id","columns":[]}],"type":""}},"require_explicit_routing":false,"foreign_key_mode":"unspecified","multi_tenant_spec":null}`)
	if !vitessSchemaMatches(want, good) {
		t.Fatal("native default fields changed the routing comparison")
	}
	for _, raw := range []string{
		`{"sharded":false}`,
		`{"sharded":true,"vindexes":{"hash":{"type":"unicode_loose_md5"}},"tables":{"orders":{"column_vindexes":[{"name":"hash","column":"customer_id"}]}}}`,
		`{"sharded":true,"vindexes":{"hash":{"type":"hash"}},"tables":{"orders":{"column_vindexes":[{"name":"hash","column":"other_id"}]}}}`,
		`{"sharded":true,"vindexes":{"hash":{"type":"hash"}},"tables":{"orders":{"column_vindexes":[{"name":"hash","column":"customer_id"}]}},"require_explicit_routing":true}`,
		`{} {}`,
	} {
		if vitessSchemaMatches(want, []byte(raw)) {
			t.Fatalf("accepted routing drift: %s", raw)
		}
	}
	if vitessSchemaMatches([]byte(`{"sharded":false}`), []byte(`{"sharded":false,"foreign_key_mode":"managed"}`)) {
		t.Fatal("accepted a changed foreign-key mode")
	}
	if !vitessSchemaMatches([]byte(`{"sharded":false}`), []byte(`{"sharded":false,"vindexes":{},"tables":{},"foreign_key_mode":"unspecified"}`)) {
		t.Fatal("native standalone enum default changed the routing comparison")
	}
}

func TestVitessNativeRequiresOwnedVerifiedPrimary(t *testing.T) {
	d := vitessTestDatabase()
	d.Spec.Mode = "cluster"
	d.Spec.Replicas = 1
	members := []database.Member{{Name: "primary", UID: "one", Shard: "-", Ready: true}, {Name: "replica", UID: "two", Shard: "-", Ready: true}}
	good := func() []vitessNativeView {
		views := []vitessNativeView{{UUID: "one", Secure: 1, Version: database.VitessMySQLVersion}, {UUID: "two", ReadOnly: 1, Secure: 1, Version: database.VitessMySQLVersion}}
		views[1].Channels = append(views[1].Channels, struct{ Host, SSL, Verify, CA string }{"primary." + DatabaseNamespace(d.ID) + ".svc.cluster.local", "YES", "YES", vitessTLSPath + "/ca.crt"})
		return views
	}
	if _, err := verifyVitessNativeViews(d, members, good()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func([]vitessNativeView){
		"unverified":       func(v []vitessNativeView) { v[1].Channels[0].Verify = "NO" },
		"wrong-host":       func(v []vitessNativeView) { v[1].Channels[0].Host = "foreign.example.com" },
		"duplicate-server": func(v []vitessNativeView) { v[1].UUID = "one" },
		"two-primary":      func(v []vitessNativeView) { v[1].ReadOnly = 0; v[1].Channels = nil },
		"plaintext":        func(v []vitessNativeView) { v[1].Secure = 0 },
		"apply-error":      func(v []vitessNativeView) { v[1].ApplyErrors = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			v := good()
			change(v)
			if _, err := verifyVitessNativeViews(d, members, v); err == nil {
				t.Fatal("accepted unhealthy native state")
			}
		})
	}
}
