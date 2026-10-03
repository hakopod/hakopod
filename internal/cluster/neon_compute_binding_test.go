package cluster

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNeonComputeBindingAllowsOneWriterAndBoundsConnections(t *testing.T) {
	template := []byte(`{"spec":{"mode":"Primary","cluster":{"roles":[],"settings":[{"name":"max_connections","value":"100000","vartype":"integer"},{"name":"max_connections","value":"9000","vartype":"integer"},{"name":"superuser_reserved_connections","value":"500","vartype":"integer"},{"name":"reserved_connections","value":"300","vartype":"integer"},{"name":"work_mem","value":"4MB","vartype":"string"}]}},"compute_ctl_config":{}}`)
	for _, item := range []struct{ name, mode string }{{"compute-0", "Primary"}, {"compute-1", "Replica"}, {"compute-5", "Replica"}} {
		t.Run(item.name, func(t *testing.T) {
			raw, err := bindNeonComputeConfig(template, "tenant", "timeline", []string{"sk0:5454", "sk1:5454", "sk2:5454"}, item.name)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err = json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			spec := body["spec"].(map[string]any)
			if spec["mode"] != item.mode || spec["tenant_id"] != "tenant" || spec["timeline_id"] != "timeline" {
				t.Fatal("compute role or durable identity was not bound")
			}
			settings := spec["cluster"].(map[string]any)["settings"].([]any)
			values := map[string]string{}
			for _, setting := range settings {
				entry := setting.(map[string]any)
				name := entry["name"].(string)
				if _, found := values[name]; found {
					t.Fatal("duplicate setting survived")
				}
				values[name] = entry["value"].(string)
			}
			if !reflect.DeepEqual(values, map[string]string{"max_connections": "64", "superuser_reserved_connections": "4", "reserved_connections": "0", "work_mem": "4MB"}) {
				t.Fatal("connection bounds were overridden or unrelated settings changed")
			}
			// Restore rebinds the new tenant/timeline without promoting replicas.
			rebound, err := bindNeonComputeConfig(raw, "restored-tenant", "restored-timeline", []string{"new-sk:5454"}, item.name)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(rebound, &body); err != nil {
				t.Fatal(err)
			}
			if body["spec"].(map[string]any)["mode"] != item.mode {
				t.Fatal("restore promoted a replica")
			}
		})
	}
}

func TestNeonComputeBindingRejectsUnknownIdentityAndMalformedSettings(t *testing.T) {
	for _, name := range []string{"primary", "compute-01", "compute--1", "compute-6"} {
		if _, err := bindNeonComputeConfig([]byte(`{"spec":{"cluster":{}}}`), "tenant", "timeline", nil, name); err == nil {
			t.Fatal("invalid compute identity accepted")
		}
	}
	for _, source := range []string{`{"spec":{}}`, `{"spec":{"cluster":{"settings":{}}}}`, `{"spec":{"cluster":{"settings":["bad"]}}}`, `{"spec":{"cluster":{"settings":[{}]}}}`} {
		if _, err := bindNeonComputeConfig([]byte(source), "tenant", "timeline", nil, "compute-0"); err == nil {
			t.Fatal("malformed compute settings accepted")
		}
	}
}
