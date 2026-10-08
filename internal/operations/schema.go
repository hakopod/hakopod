package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaResource = "https://hakopod.invalid/embedded-operation-schemas"
const maxSchemaInputs = 2048
const maxInstanceNodes = 10000

// Only embedded schemas can compile. Request data cannot add a schema or load a URL.
var inputSchemas map[string]*jsonschema.Schema
var numberLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

type rejectSchemaLoader struct{}

func (rejectSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external schema resources are unavailable")
}

func compileCatalogSchemas() error {
	inputs := []map[string]any{}
	for _, op := range catalog {
		if op.Body != nil {
			inputs = append(inputs, op.Body)
		}
		for _, parameter := range op.Parameters {
			if parameter.Schema != nil {
				inputs = append(inputs, parameter.Schema)
			}
		}
	}
	var err error
	inputSchemas, err = compileInputSchemas(doc.Components.Schemas, inputs)
	return err
}

func compileInputSchemas(components map[string]map[string]any, inputs []map[string]any) (map[string]*jsonschema.Schema, error) {
	if len(inputs) > maxSchemaInputs {
		return nil, errors.New("operation schema count exceeds 2048")
	}
	definitions := map[string]any{}
	keys := map[string]string{}
	for _, schema := range inputs {
		raw, err := json.Marshal(schema)
		if err != nil {
			return nil, errors.New("operation schema is invalid")
		}
		key := string(raw)
		if _, found := keys[key]; found {
			continue
		}
		name := strconv.Itoa(len(keys))
		keys[key] = name
		definitions[name] = schema
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectSchemaLoader{})
	// Draft 2020-12 treats format and content keywords as annotations by default.
	// Canonical handlers retain their domain-specific validation.
	componentSchemas := map[string]any{}
	for name, schema := range components {
		componentSchemas[name] = schema
	}
	resource := map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"components": map[string]any{"schemas": componentSchemas},
		"$defs":      definitions,
	}
	if err := compiler.AddResource(schemaResource, resource); err != nil {
		return nil, errors.New("embedded operation schemas are invalid")
	}
	compiled := map[string]*jsonschema.Schema{}
	for key, name := range keys {
		schema, err := compiler.Compile(schemaResource + "#/$defs/" + name)
		if err != nil {
			// The compiler reads committed contract data, never request data.
			return nil, fmt.Errorf("compile embedded operation schema %s: %w", name, err)
		}
		compiled[key] = schema
	}
	return compiled, nil
}

func validate(v any, schema map[string]any, depth int) error {
	remaining := maxInstanceNodes
	if err := boundSchemaInstance(v, depth, &remaining); err != nil {
		return err
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return errors.New("operation schema is unavailable")
	}
	compiled := inputSchemas[string(raw)]
	if compiled == nil {
		return errors.New("operation schema is unavailable")
	}
	if compiled.Validate(v) != nil {
		// Library errors can contain submitted values and dynamic property names.
		return errors.New("request does not match the operation schema. Read the operation schema before retrying")
	}
	return nil
}

// Bounds apply to every value, including objects that a schema leaves unconstrained.
func boundSchemaInstance(v any, depth int, remaining *int) error {
	*remaining = *remaining - 1
	if depth > 32 || *remaining < 0 {
		return errors.New("JSON exceeds 32 nested levels or 10000 values")
	}
	switch value := v.(type) {
	case map[string]any:
		for key, child := range value {
			if len(key) > MaxBytes {
				return errors.New("JSON property name exceeds 1 MiB")
			}
			if err := boundSchemaInstance(child, depth+1, remaining); err != nil {
				return err
			}
		}
	case []any:
		if len(value) > 1000 {
			return errors.New("JSON array exceeds 1000 items")
		}
		for _, child := range value {
			if err := boundSchemaInstance(child, depth+1, remaining); err != nil {
				return err
			}
		}
	case string:
		if len(value) > MaxBytes {
			return errors.New("JSON string exceeds 1 MiB")
		}
	case json.Number:
		literal := string(value)
		if len(literal) > 128 || !numberLiteral.MatchString(literal) {
			return errors.New("JSON number must contain at most 128 characters")
		}
		if index := strings.IndexAny(literal, "eE"); index >= 0 {
			exponent, err := strconv.Atoi(literal[index+1:])
			if err != nil || exponent < -1000 || exponent > 1000 {
				return errors.New("JSON number exponent must be from -1000 to 1000")
			}
		}
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("JSON number must be finite")
		}
	case bool, nil:
	default:
		return errors.New("request contains an unsupported JSON value")
	}
	return nil
}
