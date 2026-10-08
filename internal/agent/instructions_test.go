package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStdioInitializationInstructionsMatchConnection(t *testing.T) {
	request := func(_ context.Context, _, _ string, _ any, _ string, out any) error {
		return json.Unmarshal([]byte(`{"email":"fixture@example.invalid","credential_type":"cli","permissions":["nodes:terminal","admin","agent:admin","agent:credentials"]}`), out)
	}
	for _, tc := range []struct {
		name     string
		scope    Scope
		options  Options
		boundary string
	}{
		{"project", Scope{Project: "demo", Environment: "development"}, Options{AllowDeploy: true}, "configured project"},
		{"installation", Scope{}, Options{Installation: true, AllowAdmin: true, AllowCredentials: true}, "installation tools"},
		{"host", Scope{}, Options{HostOnly: true, AllowHostTerminal: true}, "current host grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := ServeWithStream(context.Background(), request, tc.scope, tc.options, nil, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"), &output, "test"); err != nil {
				t.Fatal(err)
			}
			var response struct {
				Result struct {
					Instructions string `json:"instructions"`
				} `json:"result"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			expected := NewWithOptions(request, tc.scope, tc.options, 1).Instructions()
			if response.Result.Instructions != expected || !strings.Contains(expected, tc.boundary) {
				t.Fatal(response.Result.Instructions)
			}
			if strings.Contains(expected, "secrets and access keys") != tc.options.AllowCredentials {
				t.Fatal("credential disclosure differs")
			}
			if strings.Contains(expected, "canonical deployment plan") != tc.options.AllowDeploy {
				t.Fatal("deployment review differs")
			}
		})
	}
}
