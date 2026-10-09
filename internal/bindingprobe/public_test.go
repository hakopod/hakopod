package bindingprobe

import (
	"strings"
	"testing"
)

func TestPublicStagesNeverExposeWorkloadMessages(t *testing.T) {
	input := []Stage{{Name: "configuration", Status: "passed", Code: "variable_loaded", Message: "postgres://secret-value"}, {Name: "dns", Status: "passed", Code: "host_resolved"}, {Name: "network", Status: "passed", Code: "tcp_connected"}, {Name: "certificate", Status: "failed", Code: "tls_verification_failed", Message: "password=do-not-return"}}
	result, ok := PublicStages(input)
	if !ok || len(result) != 4 || strings.Contains(result[0].Message, "secret") || strings.Contains(result[3].Message, "password") {
		t.Fatal("workload text was not replaced")
	}
	for _, input := range [][]Stage{
		{{Name: "query", Status: "passed", Code: "read_query_succeeded"}},
		{{Name: "configuration", Status: "passed", Code: "variable_loaded"}},
		{{Name: "configuration", Status: "failed", Code: "tls_verification_failed"}},
		{{Name: "configuration", Status: "passed", Code: "unrecognized-credential-value"}},
		{{Name: "configuration", Status: "failed", Code: "invalid_request"}, {Name: "dns", Status: "passed", Code: "host_resolved"}},
		{{Name: "configuration", Status: "passed", Code: "variable_loaded"}, {Name: "authentication", Status: "failed", Code: "authentication_rejected"}},
		{{Name: "configuration", Status: "passed", Code: "variable_loaded"}, {Name: "query", Status: "failed", Code: "read_query_rejected"}},
	} {
		if _, ok := PublicStages(input); ok {
			t.Fatal("incomplete, forged or unknown probe result accepted")
		}
	}
}
