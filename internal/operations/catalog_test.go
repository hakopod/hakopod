package operations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func fakeRequest(t *testing.T, calls *[]string, project string) RequestFunc {
	return func(_ context.Context, m, p string, _ any, _ string, out any) error {
		*calls = append(*calls, m+" "+p)
		raw := []byte(`{"id":"app","project":"` + project + `","environment":"dev"}`)
		return json.Unmarshal(raw, out)
	}
}
func TestCatalogSafetyAndCompleteness(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range Catalog() {
		if o.ID == "" || seen[o.ID] {
			t.Fatalf("missing or duplicate operation %q", o.ID)
		}
		seen[o.ID] = true
		if strings.TrimSpace(o.Summary) == "" {
			t.Fatalf("operation lacks an explicit summary: %s", o.ID)
		}
		if o.Available == (o.Exclusion != "") {
			t.Fatalf("inconsistent availability %s", o.ID)
		}
	}
	for _, id := range []string{"createKey", "mcpMessage", "createTerminal", "createDeployment", "listDNSProviders", "putDNSProvider"} {
		found := false
		for _, o := range Catalog() {
			if o.ID == id {
				found = true
				if o.Available || o.Exclusion == "" {
					t.Fatalf("unsafe operation %s", id)
				}
			}
		}
		if !found {
			t.Fatalf("contract omission %s", id)
		}
	}
	first := Catalog()
	first[0].ID = "changed"
	first[0].Parameters = append(first[0].Parameters, parameter{Name: "injected"})
	if Catalog()[0].ID == "changed" {
		t.Fatal("catalog exposed mutable state")
	}
}

func TestCatalogPathParametersMatchRouteTemplates(t *testing.T) {
	for _, operation := range catalog {
		expected := map[string]bool{}
		for _, segment := range strings.Split(operation.Path, "/") {
			if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
				expected[strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")] = true
			}
		}
		seen := map[string]bool{}
		for _, parameter := range operation.Parameters {
			key := parameter.In + ":" + parameter.Name
			if seen[key] {
				t.Fatalf("duplicate parameter: %s %s", operation.ID, key)
			}
			seen[key] = true
			if parameter.In != "path" {
				continue
			}
			if !expected[parameter.Name] || !parameter.Required || parameter.Schema == nil {
				t.Fatalf("invalid path parameter: %s %s", operation.ID, parameter.Name)
			}
			delete(expected, parameter.Name)
		}
		if len(expected) != 0 {
			t.Fatalf("missing path parameters: %s", operation.ID)
		}
	}
}
func TestRejectUnsafeInvocation(t *testing.T) {
	cases := []Invocation{
		{Operation: "getApplication", Path: map[string]string{"id": "../keys"}},
		{Operation: "getApplication", Path: map[string]string{"id": "app", "other": "x"}},
		{Operation: "getApplication", Path: map[string]string{"id": "app"}, Query: map[string]string{"method": "DELETE"}},
		{Operation: "listApplications", Query: map[string]string{"project": "other"}},
		{Operation: "listAlarms", Query: map[string]string{"limit": "100000"}},
		{Operation: "scaleService", Path: map[string]string{"id": "app", "service": "web"}, Body: json.RawMessage(`{"expected_revision":1,"replicas":2,"headers":{}}`), IdempotencyKey: "abcdefgh"},
		{Operation: "scaleService", Path: map[string]string{"id": "app", "service": "web"}, Body: json.RawMessage(`{"expected_revision":1,"replicas":2}`), IdempotencyKey: "abc\r\nx"},
		{Operation: "revealDatabaseCredentials", Path: map[string]string{"id": "db"}},
	}
	for _, in := range cases {
		t.Run(in.Operation+in.Path["id"], func(t *testing.T) {
			calls := []string{}
			_, err := Invoke(context.Background(), fakeRequest(t, &calls, "p"), Scope{"p", "dev"}, true, in)
			if err == nil {
				t.Fatal("unsafe request accepted")
			}
			if len(calls) != 0 {
				t.Fatalf("unsafe request reached API %v", calls)
			}
		})
	}
}
func TestCanonicalScopeAndWriteGate(t *testing.T) {
	calls := []string{}
	r := fakeRequest(t, &calls, "p")
	in := Invocation{Operation: "scaleService", Path: map[string]string{"id": "app", "service": "web"}, Body: json.RawMessage(`{"expected_revision":1,"replicas":2}`), IdempotencyKey: "retry-key"}
	if _, err := Invoke(context.Background(), r, Scope{"p", "dev"}, false, in); err == nil {
		t.Fatal("write gate bypass")
	}
	if len(calls) != 0 {
		t.Fatal("disabled mutation reached API")
	}
	if _, err := Invoke(context.Background(), r, Scope{"p", "dev"}, true, in); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ";") != "GET /applications/app;POST /applications/app/services/web/scale" {
		t.Fatal(calls)
	}
	calls = nil
	if _, err := Invoke(context.Background(), fakeRequest(t, &calls, "other"), Scope{"p", "dev"}, true, in); err == nil {
		t.Fatal("cross scope mutation accepted")
	}
	if len(calls) != 1 {
		t.Fatal(calls)
	}
}
func TestScopeInjectionAndNestedReferences(t *testing.T) {
	calls := []string{}
	if _, err := Invoke(context.Background(), fakeRequest(t, &calls, "p"), Scope{"p", "dev"}, false, Invocation{Operation: "listApplications"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "environment=dev") || !strings.Contains(calls[0], "project=p") {
		t.Fatal(calls)
	}
	calls = nil
	in := Invocation{Operation: "startServiceMove", Path: map[string]string{"id": "app", "service": "web"}, Body: json.RawMessage(`{"destination_id":"other","destination_service":"web","source_revision":1,"destination_revision":1}`), IdempotencyKey: "retry-key"}
	if _, err := Invoke(context.Background(), fakeRequest(t, &calls, "other"), Scope{"p", "dev"}, true, in); err == nil {
		t.Fatal("cross scope destination accepted")
	}
	if len(calls) != 1 || calls[0] != "GET /applications/other" {
		t.Fatal(calls)
	}
}
func TestDiscoveryPagination(t *testing.T) {
	first, err := Discovery("", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	page := first["operations"].([]Operation)
	if len(page) != 2 || page[0].Body != nil {
		t.Fatal("expanded or unbounded discovery")
	}
	cursor, ok := first["next_cursor"].(string)
	if !ok {
		t.Fatal("missing next cursor")
	}
	second, err := Discovery(cursor, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if second["operations"].([]Operation)[0].ID <= cursor {
		t.Fatal("pagination repeated operation")
	}
	if first["total"].(int) != first["available"].(int)+first["excluded"].(int) {
		t.Fatal("inconsistent coverage counts")
	}
	detail, err := Discovery("", "", "scaleService", 2)
	if err != nil || detail["operation"].(Operation).Body == nil {
		t.Fatal("missing operation schema")
	}
}
func TestDecodePreservesExactNumbers(t *testing.T) {
	var body map[string]any
	if err := Decode([]byte(`{"expected_revision":9007199254740993,"replicas":2}`), &body); err != nil {
		t.Fatal(err)
	}
	if body["expected_revision"] != json.Number("9007199254740993") {
		t.Fatal(body)
	}
	for _, o := range Catalog() {
		if o.ID == "scaleService" {
			if err := validate(body, o.Body, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal(string(raw))
	}
	out, err := Discovery("", "", "createManagedDatabase", 1)
	if err != nil {
		t.Fatal(err)
	}
	schemas := out["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas["ManagedDatabaseSpec"]; !ok {
		t.Fatal("selected operation omitted referenced schema")
	}
}
func TestGenericBuildCannotEnableAutomaticDeploy(t *testing.T) {
	calls := []string{}
	in := Invocation{Operation: "createSourceBuild", Body: json.RawMessage(`{"project":"p","environment":"dev","name":"build","repository":"example/repo","auto_build":true,"auto_deploy":true}`)}
	if _, err := Invoke(context.Background(), fakeRequest(t, &calls, "p"), Scope{"p", "dev"}, true, in); err == nil {
		t.Fatal("automatic deploy enabled through generic write opt-in")
	}
	if len(calls) != 0 {
		t.Fatal(calls)
	}
	in = Invocation{Operation: "runSourceBuild", Path: map[string]string{"id": "build"}, Body: json.RawMessage(`{"expected_config_revision":1}`), IdempotencyKey: "retry-key"}
	r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
		calls = append(calls, m+" "+p)
		return json.Unmarshal([]byte(`{"project":"p","environment":"dev","auto_deploy":true}`), out)
	}
	if _, err := Invoke(context.Background(), r, Scope{"p", "dev"}, true, in); err == nil {
		t.Fatal("auto deploy build run accepted")
	}
	if len(calls) != 1 {
		t.Fatal(calls)
	}
	discovery, err := Discovery("", "", "", 1)
	if err != nil || len(discovery["contract_sha256"].(string)) != 64 {
		t.Fatal(discovery, err)
	}
}
func TestScopedApplicationNameIsNotResourceID(t *testing.T) {
	calls := []string{}
	r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
		calls = append(calls, m+" "+p)
		return json.Unmarshal([]byte(`{"items":[]}`), out)
	}
	_, err := Invoke(context.Background(), r, Scope{"p", "dev"}, false, Invocation{Operation: "listWorkloadSecrets", Query: map[string]string{"application": "named-app"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || strings.Contains(calls[0], "/applications/") || !strings.Contains(calls[0], "application=named-app") || !strings.Contains(calls[0], "project=p") {
		t.Fatal(calls)
	}
}
func TestOperationPolicyCompletenessAndFailClosed(t *testing.T) {
	for _, op := range Catalog() {
		policy := op.Policy
		if op.Available && len(policy.Permissions) == 0 {
			t.Fatalf("missing canonical permissions %s", op.ID)
		}
		if policy.Category == "" || policy.Boundary == "" || policy.Category == "unclassified" {
			t.Fatalf("unclassified contract operation %s", op.ID)
		}
	}
	policy, exclusion := classify(Operation{ID: "futureOperation", Method: "GET", Path: "/applications/{id}"})
	if exclusion == "" || policy.Category != "unclassified" {
		t.Fatal("new operation inherits prefix access", policy, exclusion)
	}
}

func TestReviewedPlatformExactScopeAndDeploymentGate(t *testing.T) {
	in := Invocation{Operation: "reviewManagedPlatform", Body: json.RawMessage(`{"project":"p","environment":"dev","id":"platform","expected_revision":1,"kind":"delete","spec":{},"confirm_name":"db"}`)}
	calls := []string{}
	if _, err := Invoke(context.Background(), fakeRequest(t, &calls, "p"), Scope{"p", "dev"}, true, in); err == nil || len(calls) != 0 {
		t.Fatal("deployment gate bypass", err, calls)
	}
	// Test references directly so a schema rejection cannot hide a missing scope check.
	if err := preflight(context.Background(), fakeRequest(t, &calls, "other"), Scope{"p", "dev"}, Operation{ID: "reviewManagedPlatform"}, nil, nil, map[string]any{"id": "platform"}); err == nil {
		t.Fatal("cross-scope platform accepted")
	}
	if len(calls) != 1 || calls[0] != "GET /managed-platforms/platform" {
		t.Fatal(calls)
	}
}
func TestExplicitReferenceMappings(t *testing.T) {
	calls := []string{}
	if err := references(context.Background(), fakeRequest(t, &calls, "p"), Scope{"p", "dev"}, map[string]string{"destination_id": "applications"}, map[string]any{"destination_id": "app"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "GET /applications/app" {
		t.Fatal(calls)
	}
	calls = nil
	if err := references(context.Background(), fakeRequest(t, &calls, "other"), Scope{"p", "dev"}, nil, map[string]any{"destination_id": "backup-destination"}); err != nil || len(calls) != 0 {
		t.Fatal("unmapped backup destination treated as application", err, calls)
	}
}

func TestOperationResourceScopeResolution(t *testing.T) {
	for _, resource := range []string{"database-operations", "database-public-endpoint-operations", "external-database-operations"} {
		calls := []string{}
		r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
			calls = append(calls, m+" "+p)
			raw := `{"project":"other","environment":"dev"}`
			if len(calls) == 1 {
				raw = `{"database_id":"db"}`
			}
			return json.Unmarshal([]byte(raw), out)
		}
		if err := resourceScope(context.Background(), r, Scope{"p", "dev"}, resource, "operation"); err == nil || len(calls) != 2 {
			t.Fatal("operation failed to resolve owned database", resource, err, calls)
		}
	}
}

func TestAlarmExactCredentialAndShowcaseFixedScope(t *testing.T) {
	for _, id := range []string{"readAlarm", "acknowledgeAlarm"} {
		calls := 0
		r := func(_ context.Context, m, p string, _ any, _ string, out any) error {
			calls++
			if p != "/me" {
				t.Fatal("alarm sent before exact key fence")
			}
			return json.Unmarshal([]byte(`{"project":"other","environment":"dev","credential_type":"machine"}`), out)
		}
		if _, err := Invoke(context.Background(), r, Scope{"p", "dev"}, true, Invocation{Operation: id, Path: map[string]string{"id": "incident"}}); err == nil || calls != 1 {
			t.Fatal("alarm exact credential", id, err)
		}
	}
	if err := preflight(context.Background(), nil, Scope{"p", "dev"}, Operation{Policy: Policy{FixedScope: map[string]string{"project": "demo", "environment": "development"}}}, nil, nil, nil); err == nil {
		t.Fatal("showcase scope")
	}
}

func TestDiscoveryPreservesContractSemanticText(t *testing.T) {
	summaries, descriptions := 0, 0
	for _, op := range Catalog() {
		var entry struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(doc.Paths[op.Path][strings.ToLower(op.Method)], &entry); err != nil {
			t.Fatal(err)
		}
		if op.Summary != entry.Summary || op.Description != entry.Description {
			t.Fatal("catalog semantic text differs", op.ID)
		}
		if entry.Summary != "" {
			summaries++
		}
		if entry.Description != "" {
			descriptions++
		}
		detail, err := Discovery("", "", op.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		got := detail["operation"].(Operation)
		if got.Summary != entry.Summary || got.Description != entry.Description {
			t.Fatal("detail lost semantic text", op.ID)
		}
	}
	if summaries == 0 || descriptions == 0 {
		t.Fatal("contract lacks regression samples")
	}
	cursor := ""
	for {
		result, err := Discovery(cursor, "", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range result["operations"].([]Operation) {
			var entry struct {
				Summary     string `json:"summary"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(doc.Paths[op.Path][strings.ToLower(op.Method)], &entry); err != nil {
				t.Fatal(err)
			}
			if op.Summary != entry.Summary || op.Description != entry.Description {
				t.Fatal("page lost semantic text", op.ID)
			}
		}
		next, ok := result["next_cursor"].(string)
		if !ok {
			break
		}
		cursor = next
	}
	raw, err := json.Marshal(Operation{ID: "missing-description"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["summary"] != nil || fields["description"] != nil {
		t.Fatal("invented empty semantic fields")
	}
}
