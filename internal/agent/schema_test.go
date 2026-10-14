package agent

import (
	"encoding/json"
	"testing"
)

func TestToolInputSchemasNeverSerializeNullFields(t *testing.T) {
	variants := []Options{
		{},
		{AllowDeploy: true, AllowWrite: true, AllowExec: true, AllowSQL: true, AllowSQLWrite: true, AllowAdmin: true, AllowCredentials: true, AllowTerminal: true, AllowHostTerminal: true},
		{Installation: true, AllowAdmin: true},
		{HostOnly: true, AllowHostTerminal: true},
	}
	for _, options := range variants {
		server := NewWithOptions(nil, Scope{Project: "demo", Environment: "production"}, options, 1)
		raw, err := json.Marshal(map[string]any{"tools": server.Tools()})
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Tools []struct {
				Name        string                     `json:"name"`
				InputSchema map[string]json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, tool := range decoded.Tools {
			if req, ok := tool.InputSchema["required"]; ok && (string(req) == "null" || len(req) == 0 || req[0] != '[') {
				t.Errorf("%s: required is %s", tool.Name, req)
			}
			if props := tool.InputSchema["properties"]; len(props) == 0 || props[0] != '{' {
				t.Errorf("%s: properties is %s", tool.Name, props)
			}
		}
		var generic any
		_ = json.Unmarshal(raw, &generic)
		assertNoNullSchemaKeys(t, "tools", generic)
	}
}

func assertNoNullSchemaKeys(t *testing.T, path string, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if child == nil && (k == "required" || k == "properties" || k == "items") {
				t.Errorf("%s.%s is null", path, k)
			}
			assertNoNullSchemaKeys(t, path+"."+k, child)
		}
	case []any:
		for _, child := range x {
			assertNoNullSchemaKeys(t, path+"[]", child)
		}
	}
}
