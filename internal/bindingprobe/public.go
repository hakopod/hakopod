package bindingprobe

// PublicStages replaces every returned message with fixed platform text.
// Workload stdout is not trusted: a modified application must not use this
// endpoint to return its environment, driver errors or arbitrary output.
func PublicStages(input []Stage) ([]Stage, bool) {
	if len(input) < 1 || len(input) > 7 {
		return nil, false
	}
	stages := make([]Stage, 0, len(input))
	passed := []string{"configuration", "dns", "network", "certificate", "authentication", "query"}
	next := 0
	for i, stage := range input {
		canonical, ok := publicStageCodes[stage.Code]
		if !ok || stage.Name != canonical.Name || stage.Status != canonical.Status {
			return nil, false
		}
		canonical.Code = stage.Code
		if canonical.Status == "passed" || canonical.Code == "tls_not_configured" {
			if next >= len(passed) || canonical.Name != passed[next] {
				return nil, false
			}
			next++
		} else {
			if i != len(input)-1 {
				return nil, false
			}
			if canonical.Status == "unsupported" {
				if canonical.Name != "capability" || next != 1 {
					return nil, false
				}
			} else if next >= len(passed) || canonical.Name != passed[next] {
				return nil, false
			}
		}
		stages = append(stages, canonical)
	}
	last := stages[len(stages)-1]
	if last.Status == "passed" && (next != len(passed) || last.Code != "read_query_succeeded") {
		return nil, false
	}
	if last.Status == "unsupported" && (len(stages) != 2 || stages[0].Code != "variable_loaded") {
		return nil, false
	}
	return stages, true
}

var publicStageCodes = map[string]Stage{
	"invalid_request":            {Name: "configuration", Status: "failed", Message: "The probe request is invalid."},
	"variable_not_loaded":        {Name: "configuration", Status: "failed", Message: "The running process did not load this variable."},
	"variable_too_large":         {Name: "configuration", Status: "failed", Message: "The loaded variable exceeds 16 KiB."},
	"connection_value_invalid":   {Name: "configuration", Status: "failed", Message: "The loaded variable is not a supported connection value."},
	"fingerprint_nonce_invalid":  {Name: "configuration", Status: "failed", Message: "The comparison nonce is invalid."},
	"variable_loaded":            {Name: "configuration", Status: "passed", Message: "The running process loaded a valid connection value."},
	"protocol_probe_unsupported": {Name: "capability", Status: "unsupported", Message: "This helper version cannot verify this protocol."},
	"dns_lookup_failed":          {Name: "dns", Status: "failed", Message: "DNS could not resolve a bounded address set required by the connection."},
	"host_resolved":              {Name: "dns", Status: "passed", Message: "DNS resolved the connection host."},
	"network_unreachable":        {Name: "network", Status: "failed", Message: "The application runtime could not open a TCP connection."},
	"tcp_connected":              {Name: "network", Status: "passed", Message: "The application runtime opened a TCP connection."},
	"ca_unavailable":             {Name: "certificate", Status: "failed", Message: "The configured CA file is unavailable or invalid."},
	"tls_handshake_failed":       {Name: "certificate", Status: "failed", Message: "TLS could not establish a verified connection."},
	"tls_verification_failed":    {Name: "certificate", Status: "failed", Message: "TLS could not verify the server hostname and certificate chain."},
	"tls_verified":               {Name: "certificate", Status: "passed", Message: "TLS verified the server hostname and certificate chain."},
	"tls_not_configured":         {Name: "certificate", Status: "skipped", Message: "This binding is configured without TLS. The test used an unencrypted connection."},
	"authentication_rejected":    {Name: "authentication", Status: "failed", Message: "The server rejected the loaded credentials."},
	"authentication_failed":      {Name: "authentication", Status: "failed", Message: "The server did not complete authentication. Check the database and try again."},
	"database_access_rejected":   {Name: "authentication", Status: "failed", Message: "The requested database does not exist or the loaded account cannot access it."},
	"authenticated":              {Name: "authentication", Status: "passed", Message: "The server accepted the loaded credentials."},
	"read_query_rejected":        {Name: "query", Status: "failed", Message: "The server rejected the minimal read-only query."},
	"connection_or_query_failed": {Name: "query", Status: "failed", Message: "The verified connection or minimal read-only query failed. Server details were removed."},
	"read_query_succeeded":       {Name: "query", Status: "passed", Message: "A minimal read-only query succeeded. This does not verify access to application tables or all required grants."},
}
