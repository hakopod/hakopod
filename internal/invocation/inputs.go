package invocation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hakopod/hakopod/internal/spec"
)

// NormalizeInputs accepts only the keys in a reviewed template and returns
// deterministic JSON for idempotency. Numbers retain their original precision.
func NormalizeInputs(cfg *spec.JobInvocation, inputs map[string]json.RawMessage) ([]byte, error) {
	if cfg == nil || len(inputs) == 0 || len(inputs) > 16 {
		return nil, fmt.Errorf("invocation inputs are required")
	}
	allowed := map[string]bool{}
	for _, key := range cfg.InputKeys {
		allowed[key] = true
	}
	canonical := map[string]any{}
	for key, raw := range inputs {
		if !allowed[key] {
			return nil, fmt.Errorf("invocation inputs contain an undeclared key")
		}
		if len(raw) > MaxInputBytes {
			return nil, fmt.Errorf("invocation input exceeds its byte limit")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invocation input must be valid JSON")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("invocation input must contain one JSON value")
		}
		canonical[key] = value
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("invocation input must be valid JSON")
	}
	limit := cfg.MaxInputBytes
	if limit == 0 {
		limit = 64 << 10
	}
	if limit > MaxInputBytes || len(encoded) > limit {
		return nil, fmt.Errorf("invocation input exceeds its byte limit")
	}
	return encoded, nil
}
func AllowsIdentity(cfg *spec.JobInvocation, id string) bool {
	if cfg == nil || id == "" {
		return false
	}
	for _, allowed := range cfg.AllowedIdentities {
		if allowed == id {
			return true
		}
	}
	return false
}
