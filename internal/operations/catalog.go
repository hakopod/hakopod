// Package operations exposes a bounded subset of the versioned REST contract.
package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	contract "github.com/hakopod/hakopod/api"
)

const Version = "v1"
const MaxBytes = 1 << 20

type RequestFunc func(context.Context, string, string, any, string, any) error
type Scope struct{ Project, Environment string }
type Operation struct {
	ID           string         `json:"id"`
	Summary      string         `json:"summary,omitempty"`
	Description  string         `json:"description,omitempty"`
	Method       string         `json:"method"`
	Path         string         `json:"path"`
	Mutating     bool           `json:"mutating"`
	Available    bool           `json:"available"`
	Exclusion    string         `json:"exclusion,omitempty"`
	Parameters   []parameter    `json:"parameters,omitempty"`
	Body         map[string]any `json:"body_schema,omitempty"`
	Policy       Policy         `json:"policy"`
	bodyRequired bool
}
type parameter struct {
	Name     string         `json:"name"`
	In       string         `json:"in"`
	Required bool           `json:"required"`
	Schema   map[string]any `json:"schema"`
}
type Invocation struct {
	Operation      string            `json:"operation"`
	Path           map[string]string `json:"path,omitempty"`
	Query          map[string]string `json:"query,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

var segment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]{0,255}$`)
var doc struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
}
var catalog []Operation
var contractSHA256 string

func init() {
	sum := sha256.Sum256(contract.OpenAPI)
	contractSHA256 = hex.EncodeToString(sum[:])
	decoder := json.NewDecoder(bytes.NewReader(contract.OpenAPI))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		panic(err)
	}
	for path, methods := range doc.Paths {
		for method, raw := range methods {
			if method != "get" && method != "post" && method != "put" && method != "patch" && method != "delete" {
				continue
			}
			var entry struct {
				ID          string      `json:"operationId"`
				Summary     string      `json:"summary"`
				Description string      `json:"description"`
				Policy      Policy      `json:"x-hakopod-agent"`
				Parameters  []parameter `json:"parameters"`
				RequestBody struct {
					Required bool `json:"required"`
					Content  map[string]struct {
						Schema map[string]any `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&entry); err != nil {
				panic(err)
			}
			op := Operation{ID: entry.ID, Summary: entry.Summary, Description: entry.Description, Method: strings.ToUpper(method), Path: path, Mutating: method != "get", Parameters: entry.Parameters, Body: entry.RequestBody.Content["application/json"].Schema, bodyRequired: entry.RequestBody.Required}
			op.Policy = entry.Policy
			op.Policy, op.Exclusion = classify(op)
			op.Available = op.Exclusion == ""
			catalog = append(catalog, op)
		}
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].ID < catalog[j].ID })
	if err := compileCatalogSchemas(); err != nil {
		panic(err)
	}
}
func Catalog() []Operation {
	raw, _ := json.Marshal(catalog)
	var out []Operation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&out)
	return out
}
func Decode(raw []byte, out any) error {
	if len(raw) > MaxBytes {
		return errors.New("request exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
func Invoke(ctx context.Context, request RequestFunc, scope Scope, allowWrite bool, in Invocation) (any, error) {
	return invoke(ctx, request, scope, allowWrite, in, false, false, false)
}
func invoke(ctx context.Context, request RequestFunc, scope Scope, allowWrite bool, in Invocation, installation, credentials, deploy bool) (any, error) {
	raw, _ := json.Marshal(in)
	if len(raw) > MaxBytes {
		return nil, errors.New("request exceeds 1 MiB")
	}
	if !installation && (scope.Project == "" || scope.Environment == "") {
		return nil, errors.New("project and environment are required")
	}
	var op *Operation
	for i := range catalog {
		if catalog[i].ID == in.Operation {
			op = &catalog[i]
			break
		}
	}
	if op == nil {
		return nil, errors.New("unknown operation")
	}
	if op.Policy.CredentialRequired && !credentials {
		return nil, errors.New("credential operations require explicit credential opt-in")
	}
	if op.Policy.Category == "deploy" && !deploy {
		return nil, errors.New("deployment operations require explicit deployment opt-in")
	}
	if !op.Available && !installation {
		return nil, errors.New(op.Exclusion)
	}
	if op.Policy.ScopeRequirement == "project_credential" {
		if err := requireProjectCredential(ctx, request, scope); err != nil {
			return nil, err
		}
	}
	if op.Mutating && !allowWrite {
		return nil, errors.New("mutation requires explicit write opt-in")
	}
	if len(in.IdempotencyKey) > 128 || strings.ContainsAny(in.IdempotencyKey, "\r\n\x00") {
		return nil, errors.New("invalid idempotency key")
	}
	path := op.Path
	query := url.Values{}
	allowedPath := map[string]bool{}
	allowedQuery := map[string]bool{}
	for _, p := range op.Parameters {
		switch p.In {
		case "path":
			allowedPath[p.Name] = true
		case "query":
			allowedQuery[p.Name] = true
		case "header":
			if p.Name == "Idempotency-Key" && p.Required && len(in.IdempotencyKey) < 8 {
				return nil, errors.New("idempotency key requires 8 to 128 characters")
			}
		}
	}
	for k, v := range in.Path {
		if !allowedPath[k] || !segment.MatchString(v) || v == "." || v == ".." {
			return nil, errors.New("invalid path parameter")
		}
		for _, p := range op.Parameters {
			if p.In == "path" && p.Name == k {
				if err := validate(v, p.Schema, 0); err != nil {
					return nil, errors.New("invalid path parameter value")
				}
			}
		}
		path = strings.ReplaceAll(path, "{"+k+"}", url.PathEscape(v))
	}
	if strings.Contains(path, "{") {
		return nil, errors.New("missing path parameter")
	}
	for k, v := range in.Query {
		if !allowedQuery[k] || len(v) > 2048 || strings.ContainsAny(v, "\r\n\x00") {
			return nil, errors.New("invalid query parameter")
		}
		if k == "limit" || k == "page" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100 {
				return nil, errors.New("list limit and page must be 1 to 100")
			}
		}
		for _, p := range op.Parameters {
			if p.In == "query" && p.Name == k {
				var value any = v
				switch p.Schema["type"] {
				case "integer", "number":
					n, err := strconv.ParseFloat(v, 64)
					if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
						return nil, errors.New("invalid numeric query parameter")
					}
					value = json.Number(v)
				case "boolean":
					b, err := strconv.ParseBool(v)
					if err != nil {
						return nil, errors.New("invalid boolean query parameter")
					}
					value = b
				}
				if err := validate(value, p.Schema, 0); err != nil {
					return nil, err
				}
			}
		}
		query.Set(k, v)
	}
	for _, p := range op.Parameters {
		if p.In == "query" && p.Required && query.Get(p.Name) == "" && p.Name != "project" && p.Name != "environment" {
			return nil, fmt.Errorf("missing query parameter %s", p.Name)
		}
	}
	var body any
	if len(in.Body) > 0 {
		if err := Decode(in.Body, &body); err != nil {
			return nil, err
		}
		if op.Body == nil {
			return nil, errors.New("operation does not accept a JSON body")
		}
		if err := validate(body, op.Body, 0); err != nil {
			return nil, err
		}
	} else if op.bodyRequired {
		return nil, errors.New("request body is required")
	}
	if strings.HasPrefix(op.Path, "/builds") && op.Mutating {
		if err := rejectAutoDeploy(body); err != nil {
			return nil, err
		}
	}
	if installation && !credentials && hasSensitiveFields(body, op.Policy.SensitiveFields) {
		return nil, errors.New("credential fields require separate credential opt-in and agent:credentials")
	}
	if !installation {
		if err := checkScope(body, scope); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"project", "environment"} {
		if installation {
			continue
		}
		wanted := scope.Project
		if key == "environment" {
			wanted = scope.Environment
		}
		if val := query.Get(key); val != "" && val != wanted {
			return nil, errors.New("query is outside the configured scope")
		}
		if allowedQuery[key] {
			query.Set(key, wanted)
		}
	}
	if obj, ok := body.(map[string]any); ok && !installation {
		schema := resolve(op.Body)
		props, _ := schema["properties"].(map[string]any)
		if _, ok := props["project"]; ok {
			obj["project"] = scope.Project
		}
		if _, ok := props["environment"]; ok {
			obj["environment"] = scope.Environment
		}
	}
	if !installation {
		if err := references(ctx, request, scope, op.Policy.References, body); err != nil {
			return nil, err
		}
		for _, name := range []string{"application_id"} {
			if app := query.Get(name); app != "" {
				if err := resourceScope(ctx, request, scope, "applications", app); err != nil {
					return nil, err
				}
			}
		}
		if err := preflight(ctx, request, scope, *op, in.Path, query, body); err != nil {
			return nil, err
		}
	}
	if op.Path == "/templates" {
		query.Del("project")
		query.Del("environment")
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out any
	if err := request(ctx, op.Method, path, body, in.IdempotencyKey, &out); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxBytes {
		return nil, errors.New("response exceeds 1 MiB")
	}
	return out, nil
}
func resolve(s map[string]any) map[string]any {
	if ref, ok := s["$ref"].(string); ok {
		return doc.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
	}
	return s
}
func checkScope(v any, s Scope) error {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "project" && val != s.Project || k == "environment" && val != s.Environment {
				return errors.New("body is outside the configured scope")
			}
			if err := checkScope(val, s); err != nil {
				return err
			}
		}
	case []any:
		for _, val := range x {
			if err := checkScope(val, s); err != nil {
				return err
			}
		}
	}
	return nil
}
func preflight(ctx context.Context, r RequestFunc, s Scope, o Operation, p map[string]string, q url.Values, body any) error {
	if o.Policy.FixedScope != nil && (s.Project != o.Policy.FixedScope["project"] || s.Environment != o.Policy.FixedScope["environment"]) {
		return errors.New("operation belongs to a different fixed project/environment")
	}
	if o.ID == "reviewManagedPlatform" || o.ID == "acceptManagedPlatform" {
		if m, ok := body.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" {
				return resourceScope(ctx, r, s, "managed-platforms", id)
			}
		}
	}
	if o.Policy.Resource != "" {
		return resourceScope(ctx, r, s, o.Policy.Resource, p[o.Policy.ResourceParameter])
	}
	parts := strings.Split(strings.Trim(o.Path, "/"), "/")
	resource := ""
	id := ""
	if len(parts) > 1 && parts[1] == "{id}" {
		switch parts[0] {
		case "applications", "databases", "external-databases", "managed-platforms", "builds", "deployments", "managed-platform-operations", "managed-platform-recovery-operations":
			resource = parts[0]
			id = p["id"]
		}
	}
	if resource == "" { // Collections and named scoped resources need explicit scope on the canonical request.
		if o.Path == "/applications" || o.Path == "/databases" || o.Path == "/external-databases" || o.Path == "/managed-platforms" || o.Path == "/builds" || o.Path == "/virtual-networks" || o.Path == "/dns-providers" || o.Path == "/registries" || o.Path == "/secrets" || o.Path == "/alarms" || o.Path == "/alarm-settings" || o.Path == "/requests" || o.Path == "/storage/retained" || o.Path == "/placement/nodes" || o.Path == "/database-placement/nodes" || o.Path == "/actions/capabilities" || o.Path == "/cloud/capabilities" || strings.HasPrefix(o.Path, "/virtual-networks/") || strings.HasPrefix(o.Path, "/registries/") || strings.HasPrefix(o.Path, "/dns-providers/") || strings.HasPrefix(o.Path, "/secrets/") {
			q.Set("project", s.Project)
			q.Set("environment", s.Environment)
		}
		if m, ok := body.(map[string]any); ok {
			if app, ok := m["application_id"].(string); ok && app != "" {
				return resourceScope(ctx, r, s, "applications", app)
			}
		}
		return nil
	}
	if resource == "builds" && o.Mutating {
		var build map[string]any
		if err := r(ctx, "GET", "/builds/"+url.PathEscape(id), nil, "", &build); err != nil {
			return err
		}
		if build["project"] != s.Project || build["environment"] != s.Environment {
			return errors.New("resource is outside the configured scope")
		}
		if enabled, _ := build["auto_deploy"].(bool); enabled {
			return errors.New("automatic deployment builds require their dedicated reviewed interface")
		}
		return nil
	}
	return resourceScope(ctx, r, s, resource, id)
}
func resourceScope(ctx context.Context, r RequestFunc, s Scope, resource, id string) error {
	if !segment.MatchString(id) {
		return errors.New("invalid resource ID")
	}
	var out map[string]any
	if err := r(ctx, "GET", "/"+resource+"/"+url.PathEscape(id), nil, "", &out); err != nil {
		return err
	}
	if database, ok := out["database_id"].(string); ok {
		switch resource {
		case "database-operations", "database-public-endpoint-operations":
			return resourceScope(ctx, r, s, "databases", database)
		case "external-database-operations":
			return resourceScope(ctx, r, s, "external-databases", database)
		}
	}
	if platform, ok := out["platform_id"].(string); ok && resource == "managed-platform-operations" {
		return resourceScope(ctx, r, s, "managed-platforms", platform)
	}
	if app, ok := out["application_id"].(string); ok && resource == "deployments" {
		return resourceScope(ctx, r, s, "applications", app)
	}
	if out["project"] != s.Project || out["environment"] != s.Environment {
		return errors.New("resource is outside the configured scope")
	}
	return nil
}

func references(ctx context.Context, r RequestFunc, s Scope, refs map[string]string, v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if resource := refs[k]; resource != "" {
				id, ok := val.(string)
				if !ok {
					return errors.New("invalid resource reference")
				}
				if id != "" {
					if err := resourceScope(ctx, r, s, resource, id); err != nil {
						return err
					}
				}
			}
			if err := references(ctx, r, s, refs, val); err != nil {
				return err
			}
		}
	case []any:
		for _, val := range x {
			if err := references(ctx, r, s, refs, val); err != nil {
				return err
			}
		}
	}
	return nil
}

// Discovery returns bounded summaries. Supply operation to inspect one contract schema.
func Discovery(cursor, family, operation string, limit int) (map[string]any, error) {
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 || len(cursor) > 256 || len(family) > 128 {
		return nil, errors.New("invalid discovery bounds")
	}
	available := 0
	for _, o := range catalog {
		if o.Available {
			available++
		}
	}
	out := map[string]any{"contract_sha256": contractSHA256, "version": Version, "total": len(catalog), "available": available, "excluded": len(catalog) - available}
	if operation != "" {
		for _, o := range Catalog() {
			if o.ID == operation {
				out["operation"] = o
				definitions := map[string]any{}
				collectDefinitions(o.Body, definitions, 0)
				for _, p := range o.Parameters {
					collectDefinitions(p.Schema, definitions, 0)
				}
				out["components"] = map[string]any{"schemas": definitions}
				encoded, err := json.Marshal(out)
				if err != nil || len(encoded) > MaxBytes {
					return nil, errors.New("selected operation schema exceeds 1 MiB")
				}
				return out, nil
			}
		}
		return nil, errors.New("unknown operation")
	}
	page := []Operation{}
	more := false
	for _, o := range Catalog() {
		if o.ID <= cursor {
			continue
		}
		if family != "" && !strings.HasPrefix(o.Path, "/"+family+"/") && o.Path != "/"+family {
			continue
		}
		if len(page) == limit {
			more = true
			break
		}
		o.Body = nil
		page = append(page, o)
	}
	out["operations"] = page
	if more {
		out["next_cursor"] = page[len(page)-1].ID
	}
	return out, nil
}

func collectDefinitions(v any, out map[string]any, depth int) {
	if depth > 32 || len(out) > 256 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			key := strings.TrimPrefix(ref, "#/components/schemas/")
			if _, exists := out[key]; !exists {
				if schema, exists := doc.Components.Schemas[key]; exists {
					out[key] = schema
					collectDefinitions(schema, out, depth+1)
				}
			}
		}
		for _, val := range x {
			collectDefinitions(val, out, depth+1)
		}
	case []any:
		for _, val := range x {
			collectDefinitions(val, out, depth+1)
		}
	}
}

func rejectAutoDeploy(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for key, val := range x {
			if key == "auto_deploy" && val == true {
				return errors.New("automatic deployment requires its dedicated reviewed interface")
			}
			if err := rejectAutoDeploy(val); err != nil {
				return err
			}
		}
	case []any:
		for _, val := range x {
			if err := rejectAutoDeploy(val); err != nil {
				return err
			}
		}
	}
	return nil
}
