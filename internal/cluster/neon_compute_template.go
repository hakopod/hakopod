package cluster

import (
	"encoding/json"
	"fmt"
	"strings"
)

type neonTemplateOption struct {
	Name    *string `json:"name"`
	Value   *string `json:"value"`
	VarType *string `json:"vartype"`
}

// These fields mirror the required ComputeSpec, Cluster and ComputeCtlConfig
// fields in the pinned provider. Rust's derived Default does not make a field
// optional during deserialization; only Option and serde(default) do that.
func validateNeonComputeTemplateFields(template []byte) error {
	if !neonTemplateHasExactRequiredFields(template) {
		return fmt.Errorf("Neon compute template is missing a required provider field")
	}
	var config struct {
		Spec *struct {
			FormatVersion  *float32 `json:"format_version"`
			SuspendTimeout *int64   `json:"suspend_timeout_seconds"`
			Cluster        *struct {
				ClusterID      *string `json:"cluster_id"`
				Name           *string `json:"name"`
				State          *string `json:"state"`
				PostgresqlConf *string `json:"postgresql_conf"`
				Roles          []struct {
					Name              *string              `json:"name"`
					EncryptedPassword *string              `json:"encrypted_password"`
					Options           []neonTemplateOption `json:"options"`
				} `json:"roles"`
				Databases []struct {
					Name    *string              `json:"name"`
					Owner   *string              `json:"owner"`
					Options []neonTemplateOption `json:"options"`
				} `json:"databases"`
				Settings []neonTemplateOption `json:"settings"`
			} `json:"cluster"`
		} `json:"spec"`
		Control *struct {
			JWKS *struct {
				Keys []map[string]json.RawMessage `json:"keys"`
			} `json:"jwks"`
			TLS *struct {
				KeyPath  *string `json:"key_path"`
				CertPath *string `json:"cert_path"`
			} `json:"tls"`
		} `json:"compute_ctl_config"`
	}
	if json.Unmarshal(template, &config) != nil {
		return fmt.Errorf("Neon compute template fields have invalid types or numeric bounds")
	}
	if config.Spec == nil || config.Spec.FormatVersion == nil || config.Spec.SuspendTimeout == nil || config.Spec.Cluster == nil {
		return fmt.Errorf("Neon compute spec requires format_version, suspend_timeout_seconds and cluster")
	}
	cluster := config.Spec.Cluster
	if cluster.Roles == nil || cluster.Databases == nil || len(cluster.Roles) > 128 || len(cluster.Databases) > 128 {
		return fmt.Errorf("Neon compute cluster requires bounded roles and databases lists")
	}
	for _, role := range cluster.Roles {
		if !neonTemplateString(role.Name, 256) || !neonTemplateOptions(role.Options) {
			return fmt.Errorf("Neon compute role requires a name and valid options")
		}
	}
	for _, database := range cluster.Databases {
		if !neonTemplateString(database.Name, 256) || !neonTemplateString(database.Owner, 256) || !neonTemplateOptions(database.Options) {
			return fmt.Errorf("Neon compute database requires a name, owner and valid options")
		}
	}
	if !neonTemplateOptions(cluster.Settings) {
		return fmt.Errorf("Neon compute settings must contain bounded name, value and vartype fields")
	}
	if config.Control == nil || config.Control.JWKS == nil || config.Control.JWKS.Keys == nil || len(config.Control.JWKS.Keys) > 16 {
		return fmt.Errorf("Neon compute_ctl_config requires a bounded jwks.keys list")
	}
	for _, key := range config.Control.JWKS.Keys {
		if !neonTemplateJWK(key) {
			return fmt.Errorf("Neon compute JWKS contains an invalid key")
		}
	}
	if tls := config.Control.TLS; tls != nil && (!neonTemplateString(tls.KeyPath, 4096) || !neonTemplateString(tls.CertPath, 4096)) {
		return fmt.Errorf("Neon compute TLS requires key_path and cert_path")
	}
	return nil
}

// encoding/json matches struct tags without regard to case, while the provider
// requires the exact field names. Check those names before typed decoding.
func neonTemplateHasExactRequiredFields(raw []byte) bool {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	object := func(value any) map[string]any { result, _ := value.(map[string]any); return result }
	required := func(value map[string]any, keys ...string) bool {
		for _, key := range keys {
			if value[key] == nil {
				return false
			}
		}
		return true
	}
	spec, control := object(root["spec"]), object(root["compute_ctl_config"])
	cluster, jwks := object(spec["cluster"]), object(control["jwks"])
	if !required(spec, "format_version", "suspend_timeout_seconds", "cluster") || !required(cluster, "roles", "databases") || !required(jwks, "keys") {
		return false
	}
	options := func(value any) bool {
		if value == nil {
			return true
		}
		list, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if !required(object(item), "name", "vartype") {
				return false
			}
		}
		return true
	}
	for _, entry := range []struct {
		name   string
		fields []string
	}{{"roles", []string{"name"}}, {"databases", []string{"name", "owner"}}} {
		list, ok := cluster[entry.name].([]any)
		if !ok {
			return false
		}
		for _, value := range list {
			item := object(value)
			if !required(item, entry.fields...) || !options(item["options"]) {
				return false
			}
		}
	}
	if !options(cluster["settings"]) {
		return false
	}
	if tls := control["tls"]; tls != nil && !required(object(tls), "key_path", "cert_path") {
		return false
	}
	return true
}

func neonTemplateString(value *string, maximum int) bool {
	return value != nil && *value != "" && len(*value) <= maximum && !strings.ContainsRune(*value, '\x00')
}

func neonTemplateOptions(options []neonTemplateOption) bool {
	if len(options) > 256 {
		return false
	}
	for _, option := range options {
		if !neonTemplateString(option.Name, 256) || !neonTemplateString(option.VarType, 64) || (option.Value != nil && (len(*option.Value) > 8192 || strings.ContainsRune(*option.Value, '\x00'))) {
			return false
		}
	}
	return true
}

func neonTemplateJWK(key map[string]json.RawMessage) bool {
	stringField := func(name string, required bool) bool {
		raw, exists := key[name]
		if !exists || string(raw) == "null" {
			return !required
		}
		var value *string
		return json.Unmarshal(raw, &value) == nil && neonTemplateString(value, 8192)
	}
	var kind string
	if json.Unmarshal(key["kty"], &kind) != nil {
		return false
	}
	var required []string
	switch kind {
	case "RSA":
		required = []string{"n", "e"}
	case "EC":
		required = []string{"crv", "x", "y"}
	case "OKP":
		required = []string{"crv", "x"}
	case "oct":
		required = []string{"k"}
	default:
		return false
	}
	for _, name := range required {
		if !stringField(name, true) {
			return false
		}
	}
	for _, name := range []string{"use", "alg", "kid", "x5u", "x5t", "x5t#S256"} {
		if !stringField(name, false) {
			return false
		}
	}
	for _, name := range []string{"key_ops", "x5c"} {
		if raw, exists := key[name]; exists {
			var values []string
			if json.Unmarshal(raw, &values) != nil || len(values) > 16 {
				return false
			}
		}
	}
	return true
}
