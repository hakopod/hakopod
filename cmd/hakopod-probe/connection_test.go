package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/bindingprobe"
)

func TestConnectionInputAndOutputAreBoundedAndStructured(t *testing.T) {
	for _, input := range []string{"not-json", `{"schema_version":1,"protocol":"redis","variable":"REDIS_URL","ca_file":"/ca","unknown":true}`, strings.Repeat("x", bindingprobe.MaxInput+1)} {
		var output bytes.Buffer
		if err := runConnection(strings.NewReader(input), &output); err != nil {
			t.Fatal(err)
		}
		if output.Len() > 2048 {
			t.Fatalf("unbounded output: %d", output.Len())
		}
		var result bindingprobe.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if len(result.Stages) != 1 || result.Stages[0].Code != "invalid_request" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
}
