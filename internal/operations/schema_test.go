package operations

import (
	"encoding/json"
	"strings"
	"testing"
)

func schemaJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var schema map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestCompiledSchemaConstraints(t *testing.T) {
	cases := []struct {
		name, schema, valid, invalid string
	}{
		{"anyOf", `{"anyOf":[{"type":"string","pattern":"^ok$"},{"type":"null"}]}`, `null`, `4`},
		{"allOf", `{"allOf":[{"type":"integer","minimum":3},{"maximum":5}]}`, `4`, `6`},
		{"oneOf exactly one", `{"oneOf":[{"type":"integer"},{"type":"number","minimum":3}]}`, `2`, `3`},
		{"conditional", `{"type":"object","properties":{"mode":{"enum":["read","write"]},"revision":{"type":"integer"}},"if":{"properties":{"mode":{"const":"write"}}},"then":{"required":["revision"]}}`, `{"mode":"write","revision":1}`, `{"mode":"write"}`},
		{"union", `{"type":["string","null"]}`, `"ok"`, `false`},
		{"array minimum", `{"type":"array","minItems":1,"maxItems":2,"uniqueItems":true,"items":{"type":"integer"}}`, `[1,2]`, `[]`},
		{"array maximum", `{"type":"array","minItems":1,"maxItems":2,"items":{"type":"integer"}}`, `[1,2]`, `[1,2,3]`},
		{"array unique", `{"type":"array","uniqueItems":true}`, `[1,2]`, `[1,1.0]`},
		{"exact const", `{"const":9007199254740993}`, `9007199254740993`, `9007199254740992`},
		{"exact maximum", `{"type":"integer","maximum":9007199254740992}`, `9007199254740992`, `9007199254740993`},
		{"exact enum", `{"enum":[9007199254740993]}`, `9007199254740993`, `9007199254740992`},
		{"Unicode length", `{"type":"string","minLength":3,"maxLength":3}`, `"猫猫猫"`, `"猫猫"`},
		{"pattern", `{"type":"string","pattern":"^[a-z]{2}$"}`, `"ok"`, `"OK"`},
		{"closed object", `{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":false}`, `{"value":"ok"}`, `{"unexpected":"private-value"}`},
		{"property maximum", `{"type":"object","maxProperties":1}`, `{"one":1}`, `{"one":1,"two":2}`},
		{"not", `{"not":{"const":"forbidden"}}`, `"allowed"`, `"forbidden"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := schemaJSON(t, tc.schema)
			compiled, err := compileInputSchemas(nil, []map[string]any{schema})
			if err != nil {
				t.Fatal(err)
			}
			key, _ := json.Marshal(schema)
			for i, raw := range []string{tc.valid, tc.invalid} {
				var value any
				if err := Decode([]byte(raw), &value); err != nil {
					t.Fatal(err)
				}
				err := compiled[string(key)].Validate(value)
				if (err == nil) != (i == 0) {
					t.Fatalf("case %d: valid=%t", i, err == nil)
				}
			}
		})
	}
}

func TestEmbeddedSchemaReferencesAndSiblings(t *testing.T) {
	schema := schemaJSON(t, `{"$ref":"#/components/schemas/Value","maximum":3}`)
	components := map[string]map[string]any{"Value": schemaJSON(t, `{"type":"integer","minimum":1}`)}
	compiled, err := compileInputSchemas(components, []map[string]any{schema})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal(schema)
	if compiled[string(key)].Validate(json.Number("2")) != nil || compiled[string(key)].Validate(json.Number("4")) == nil {
		t.Fatal("reference or sibling constraint was not enforced")
	}
	for _, ref := range []string{"https://example.invalid/private.json", "file:///private/schema.json", "http://127.0.0.1/schema.json", "#/components/schemas/Missing"} {
		if _, err := compileInputSchemas(nil, []map[string]any{{"$ref": ref}}); err == nil {
			t.Fatal("unavailable schema resource was accepted")
		}
	}
}

func TestSchemaAnnotationsAndOpenObjects(t *testing.T) {
	for _, schema := range []map[string]any{
		schemaJSON(t, `{"type":"object","properties":{"known":{"type":"string"}}}`),
		schemaJSON(t, `{"type":"string","format":"date-time"}`),
	} {
		compiled, err := compileInputSchemas(nil, []map[string]any{schema})
		if err != nil {
			t.Fatal(err)
		}
		var value any = "domain handler validates the format"
		if schema["type"] == "object" {
			value = map[string]any{"unknown": true}
		}
		key, _ := json.Marshal(schema)
		if compiled[string(key)].Validate(value) != nil {
			t.Fatal("compiler imposed a constraint absent from the schema")
		}
	}
}

func TestSchemaInstanceBoundsAndRedaction(t *testing.T) {
	var nested any = true
	for i := 0; i < 34; i++ {
		nested = map[string]any{"unconstrained": nested}
	}
	many := make([]any, 11)
	for i := range many {
		many[i] = make([]any, 1000)
	}
	for _, value := range []any{nested, many, make([]any, 1001), json.Number("1e999999999"), json.Number(strings.Repeat("9", 129)), json.Number("null")} {
		remaining := maxInstanceNodes
		if boundSchemaInstance(value, 0, &remaining) == nil {
			t.Fatal("unbounded instance was accepted")
		}
	}
	for _, op := range catalog {
		if op.ID != "scaleService" {
			continue
		}
		for _, input := range []map[string]any{
			{"expected_revision": "private-value", "replicas": json.Number("1")},
			{"expected_revision": json.Number("1"), "replicas": json.Number("1"), "private-property-name": true},
			{"expected_revision": json.Number("1.234567890123456789"), "replicas": json.Number("1")},
		} {
			err := validate(input, op.Body, 0)
			if err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "1.234567890123456789") {
				t.Fatal("schema error was missing or exposed submitted data")
			}
		}
		return
	}
	t.Fatal("scale operation is missing")
}

func TestContractObjectExtensionsAndWebhookPayloads(t *testing.T) {
	intent := `{"kind":"backup","project":"p","environment":"dev","source_platform_id":"0123456789abcdef0123456789abcdef","expected_source_revision":1}`
	request := strings.TrimSuffix(intent, "}") + `,"confirm_target_name":"reviewed-target"}`
	review := `{"id":"review","intent":` + intent + `,"request_hash":"hash","authority_fingerprint":"authority","expires_at":"2026-10-08T12:00:00Z"}`
	accept := strings.TrimSuffix(request, "}") + `,"review":` + review + `}`
	logs := `{"entries":[],"histogram":[],"scanned":0,"matched":0,"truncated":false,"pods":0,"window_seconds":60,"warnings":[],"source":"installation","observed_at":"2026-10-08T12:00:00Z"}`
	for name, raw := range map[string]string{
		"ManagedPlatformRecoveryRequest":       request,
		"ManagedPlatformRecoveryAcceptRequest": accept,
		"InstallationLogQueryResult":           logs,
	} {
		schema := map[string]any{"$ref": "#/components/schemas/" + name}
		compiled, err := compileInputSchemas(doc.Components.Schemas, []map[string]any{schema})
		if err != nil {
			t.Fatal(err)
		}
		key, _ := json.Marshal(schema)
		var value map[string]any
		if err := Decode([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		if err := compiled[string(key)].Validate(value); err != nil {
			t.Fatalf("extension rejected valid fixture: %s", name)
		}
		value["unknown"] = true
		if compiled[string(key)].Validate(value) == nil {
			t.Fatalf("extension accepted unknown field: %s", name)
		}
	}
	webhooks := map[string]bool{"githubWebhook": false, "gitlabWebhook": false, "namedGitWebhook": false, "gitHubAppWebhook": false}
	for _, op := range catalog {
		if _, exists := webhooks[op.ID]; exists {
			if err := validate(map[string]any{"provider_field": map[string]any{"payload": true}}, op.Body, 0); err != nil {
				t.Fatal("provider payload schema is closed", op.ID)
			}
			webhooks[op.ID] = true
		}
	}
	for name, found := range webhooks {
		if !found {
			t.Fatal("webhook contract is missing", name)
		}
	}
}
