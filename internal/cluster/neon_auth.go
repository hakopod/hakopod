package cluster

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

// ValidateNeonComputeTemplate checks the same bindings used by reconciliation
// before an operation can be accepted. It does not contact the runtime.
func ValidateNeonComputeTemplate(template []byte) error {
	if len(template) == 0 || len(template) > 65536 {
		return fmt.Errorf("Neon compute template must contain at most 64 KiB")
	}
	for _, name := range []string{"compute-0", "compute-1"} {
		if _, err := bindNeonComputeConfig(template, strings.Repeat("1", 32), strings.Repeat("2", 32), []string{"preflight-0.invalid:5454", "preflight-1.invalid:5454", "preflight-2.invalid:5454"}, name); err != nil {
			return err
		}
	}
	var config struct {
		Control map[string]json.RawMessage `json:"compute_ctl_config"`
	}
	if json.Unmarshal(template, &config) != nil || config.Control == nil {
		return fmt.Errorf("Neon compute template requires a compute_ctl_config object")
	}
	return nil
}

// PrepareNeonAuthenticationSnapshots runs before the accepted snapshot is
// encrypted. The same public trust is retained through updates and restores.
func PrepareNeonAuthenticationSnapshots(request *NeonRuntimeRequest, key []byte, operationKind string) error {
	if request == nil || request.Render.Spec.Kind != "neon" || request.SecretSnapshots == nil {
		return fmt.Errorf("Neon authentication snapshot is unavailable")
	}
	if operationKind != "delete" {
		computeRef := request.Render.Spec.Secrets["compute-auth"]
		if err := ValidateNeonComputeTemplate(request.SecretSnapshots[secretSnapshotNameForCluster(computeRef)]["config.json"]); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(request.Render.Spec.Secrets))
	for _, ref := range request.Render.Spec.Secrets {
		name := secretSnapshotNameForCluster(ref)
		if seen[name] {
			return fmt.Errorf("Neon components require separate secret references")
		}
		seen[name] = true
	}
	values, err := managedplatform.NeonAuthentication(key, request.Render.PlatformID)
	if err != nil {
		return err
	}
	prepared := make(map[string]map[string][]byte, len(values)+1)
	for logical, auth := range values {
		ref, ok := request.Render.Spec.Secrets[logical]
		if !ok || ref.Name == "" || ref.Revision < 1 {
			return fmt.Errorf("Neon authentication secret reference is unavailable")
		}
		name := secretSnapshotNameForCluster(ref)
		data := make(map[string][]byte, len(auth)+3)
		for _, field := range []string{"tls.crt", "tls.key", "ca.crt"} {
			if value, ok := request.SecretSnapshots[name][field]; ok {
				data[field] = append([]byte(nil), value...)
			}
		}
		for name, value := range auth {
			data[name] = value
		}
		prepared[name] = data
	}
	// Deletion needs storage authority, but never configures a compute. A stale
	// template must not prevent removing an otherwise owned platform.
	if operationKind != "delete" {
		ref, ok := request.Render.Spec.Secrets["compute-auth"]
		if !ok || ref.Name == "" || ref.Revision < 1 {
			return fmt.Errorf("Neon compute authentication reference is unavailable")
		}
		name := secretSnapshotNameForCluster(ref)
		data := copySecretData(request.SecretSnapshots[name])
		data["config.json"], err = managedplatform.BindNeonTenantAuthentication(data["config.json"], key, request.Render.PlatformID, neonDeterministicID(request.Render.PlatformID, "tenant"))
		if err != nil {
			return err
		}
		prepared[name] = data
	}
	for name, data := range prepared {
		request.SecretSnapshots[name] = data
	}
	return nil
}
