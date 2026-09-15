// Package spec defines the one versioned application format used by every client.
package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const MaxBytes = 256 << 10

type Application struct {
	Recovery      *RecoveryPolicy        `json:"recovery,omitempty" toml:"recovery"`
	SchemaVersion int                    `json:"schema_version" toml:"schema_version"`
	Name          string                 `json:"name" toml:"name"`
	Services      map[string]Service     `json:"services" toml:"services"`
	Networks      map[string]Network     `json:"networks,omitempty" toml:"networks"`
	Volumes       map[string]NamedVolume `json:"volumes,omitempty" toml:"volumes"`
	Domains       map[string]string      `json:"domains,omitempty" toml:"domains"`
}

type Service struct {
	Suspended               bool                    `json:"suspended,omitempty" toml:"suspended,omitempty"`
	Job                     *Job                    `json:"job,omitempty" toml:"job"`
	Files                   map[string]File         `json:"files,omitempty" toml:"files"`
	Bindings                map[string]Binding      `json:"bindings,omitempty" toml:"bindings"`
	HTTP                    map[string]HTTPEndpoint `json:"http,omitempty" toml:"http"`
	Architecture            string                  `json:"architecture,omitempty" toml:"architecture"`
	Volume                  *Volume                 `json:"volume,omitempty" toml:"volume"`
	GPU                     *GPU                    `json:"gpu,omitempty" toml:"gpu"`
	RunAsUser               int64                   `json:"run_as_user,omitempty" toml:"run_as_user"`
	RunAsGroup              int64                   `json:"run_as_group,omitempty" toml:"run_as_group"`
	FSGroup                 int64                   `json:"fs_group,omitempty" toml:"fs_group"`
	ReadOnlyRootFilesystem  bool                    `json:"read_only_root_filesystem,omitempty" toml:"read_only_root_filesystem"`
	WorkingDir              string                  `json:"working_dir,omitempty" toml:"working_dir"`
	TerminationGraceSeconds int64                   `json:"termination_grace_seconds,omitempty" toml:"termination_grace_seconds"`
	CertificateMounts       []CertificateMount      `json:"certificate_mounts,omitempty" toml:"certificate_mounts"`
	Mounts                  []Mount                 `json:"mounts,omitempty" toml:"mounts"`
	TemporaryMounts         []TemporaryMount        `json:"temporary_mounts,omitempty" toml:"temporary_mounts"`
	Image                   string                  `json:"image" toml:"image"`
	Port                    int32                   `json:"port,omitempty" toml:"port"`
	PublicTCP               []PublicTCPListener     `json:"public_tcp,omitempty" toml:"public_tcp"`
	Ports                   []Port                  `json:"ports,omitempty" toml:"ports"`
	NetworkAccess           *NetworkAccess          `json:"network_access,omitempty" toml:"network_access"`
	Public                  bool                    `json:"public" toml:"public"`
	Size                    string                  `json:"size" toml:"size"`
	Replicas                int32                   `json:"replicas" toml:"replicas"`
	UpdateStrategy          string                  `json:"update_strategy,omitempty" toml:"update_strategy"`
	Readiness               *Readiness              `json:"readiness,omitempty" toml:"readiness"`
	Healthcheck             string                  `json:"healthcheck,omitempty" toml:"healthcheck"`
	Env                     map[string]string       `json:"env,omitempty" toml:"env"`
	Command                 []string                `json:"command,omitempty" toml:"command"`
	Args                    []string                `json:"args,omitempty" toml:"args"`
	DependsOn               []string                `json:"depends_on,omitempty" toml:"depends_on"`
	Networks                []string                `json:"networks" toml:"networks"`
	Secrets                 map[string]SecretRef    `json:"secrets,omitempty" toml:"secrets"`
	Autoscaling             *Autoscaling            `json:"autoscaling,omitempty" toml:"autoscaling"`
	RestartNonce            string                  `json:"restart_nonce,omitempty" toml:"restart_nonce"`
	RegistryCredential      string                  `json:"registry_credential,omitempty" toml:"registry_credential"`
	AWSIdentity             string                  `json:"aws_identity,omitempty" toml:"aws_identity"`
	TLS                     *TLSConfig              `json:"tls,omitempty" toml:"tls"`
}

type Network struct {
	Internal       bool   `json:"internal" toml:"internal"`
	VirtualNetwork string `json:"virtual_network,omitempty" toml:"virtual_network"`
	Segment        string `json:"segment,omitempty" toml:"segment"`
}

type Autoscaling struct {
	MinReplicas int32 `json:"min_replicas" toml:"min_replicas"`
	MaxReplicas int32 `json:"max_replicas" toml:"max_replicas"`
	TargetCPU   int32 `json:"target_cpu" toml:"target_cpu"`
}

type Change struct {
	Service   string `json:"service"`
	Field     string `json:"field"`
	Before    any    `json:"before"`
	After     any    `json:"after"`
	Sensitive bool   `json:"sensitive"`
}

// Profile is intentionally a small, centrally defined resource budget.
type Profile struct {
	CPURequest, CPULimit, MemoryRequest, MemoryLimit string
}

var Profiles = map[string]Profile{
	"small":   {"100m", "500m", "128Mi", "256Mi"},
	"medium":  {"250m", "1", "256Mi", "512Mi"},
	"large":   {"500m", "2", "512Mi", "1Gi"},
	"compute": {"1", "4", "2Gi", "4Gi"},
	"gpu":     {"2", "8", "8Gi", "16Gi"},
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func Parse(data []byte) (Application, error) {
	if len(data) > MaxBytes {
		return Application{}, fmt.Errorf("application TOML exceeds %d bytes", MaxBytes)
	}
	var app Application
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&app); err != nil {
		// Decoder diagnostics may contain source values. Return field locations,
		// never its contextual String or an unfiltered parser message.
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			fields := make([]string, 0, len(strict.Errors))
			for i, item := range strict.Errors {
				if i >= 16 {
					break
				}
				line, column := item.Position()
				fields = append(fields, fmt.Sprintf("%s (line %d, column %d)", strings.Join(item.Key(), "."), line, column))
			}
			return Application{}, fmt.Errorf("unknown TOML fields: %s", strings.Join(fields, ", "))
		}
		var decode *toml.DecodeError
		if errors.As(err, &decode) {
			line, column := decode.Position()
			return Application{}, fmt.Errorf("invalid TOML syntax or field type at line %d, column %d (%s)", line, column, strings.Join(decode.Key(), "."))
		}
		return Application{}, errors.New("invalid TOML syntax or field type")
	}
	// The typed TOML decoder leaves an explicit empty table as a nil map.
	// Preserve its presence so omission cannot accidentally mean "remove all".
	if app.Services == nil {
		var document map[string]any
		if err := toml.Unmarshal(data, &document); err != nil {
			return Application{}, errors.New("invalid TOML service table")
		}
		if services, ok := document["services"].(map[string]any); ok && len(services) == 0 {
			app.Services = map[string]Service{}
		}
	}
	return Normalize(app)
}

// Normalize clones its input so planning or digest resolution cannot mutate an
// immutable stored revision through a shared map or slice.
func Normalize(input Application) (Application, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return Application{}, fmt.Errorf("encode application: %w", err)
	}
	if len(data) > MaxBytes {
		return Application{}, fmt.Errorf("application specification exceeds %d bytes", MaxBytes)
	}
	var app Application
	if err := json.Unmarshal(data, &app); err != nil {
		return Application{}, err
	}
	if app.Recovery != nil && app.Recovery.OnFailure != "safe" && app.Recovery.OnFailure != "disabled" {
		return Application{}, fmt.Errorf("recovery.on_failure: choose safe or disabled")
	}
	if app.SchemaVersion == 0 {
		app.SchemaVersion = 1
	}
	if app.SchemaVersion != 1 {
		return Application{}, fmt.Errorf("schema_version: unsupported version %d (supported: 1)", app.SchemaVersion)
	}
	if !namePattern.MatchString(app.Name) {
		return Application{}, errors.New("name: use 1–40 lowercase letters, digits or hyphens, beginning with a letter and ending with a letter or digit")
	}
	if app.Services == nil || len(app.Services) > 20 {
		return Application{}, errors.New("services: provide an explicit services table with at most 20 services")
	}
	if err := ValidateDomains(app); err != nil {
		return Application{}, err
	}
	if len(app.Networks) > 16 {
		return Application{}, errors.New("networks: at most 16 networks are supported")
	}
	if app.Networks == nil {
		app.Networks = make(map[string]Network)
	}
	// The implicit default exists even when advanced networks are declared.
	if _, ok := app.Networks["default"]; !ok {
		app.Networks["default"] = Network{}
	}
	if len(app.Networks) > 16 {
		return Application{}, errors.New("networks: at most 16 networks including default are supported")
	}
	for name, network := range app.Networks {
		if !namePattern.MatchString(name) {
			return Application{}, fmt.Errorf("networks.%s: invalid network name", name)
		}
		if (network.VirtualNetwork == "") != (network.Segment == "") || network.VirtualNetwork != "" && (!namePattern.MatchString(network.VirtualNetwork) || !namePattern.MatchString(network.Segment)) {
			return Application{}, fmt.Errorf("networks.%s: virtual_network and segment must both name a configured shared network", name)
		}
	}
	for _, name := range Names(app) {
		svc := app.Services[name]
		field := "services." + name
		if err := validateCertificateMounts(svc); err != nil {
			return Application{}, fmt.Errorf("%s: %w", field, err)
		}
		if err := validateAWSIdentity(svc); err != nil {
			return Application{}, fmt.Errorf("%s: %w", field, err)
		}
		if err := validateRuntimeService(svc); err != nil {
			return Application{}, fmt.Errorf("%s: %w", field, err)
		}
		if !namePattern.MatchString(name) {
			return Application{}, fmt.Errorf("%s: invalid service name", field)
		}
		if err := validateImage(svc.Image); err != nil {
			return Application{}, fmt.Errorf("%s.image: %w", field, err)
		}
		if svc.Port < 0 || svc.Port > 65535 {
			return Application{}, fmt.Errorf("%s.port: must be 1–65535, or omitted for a worker", field)
		}
		if svc.Public && svc.Port == 0 {
			return Application{}, fmt.Errorf("%s.public: a public HTTP service requires port", field)
		}
		if svc.Size == "" {
			svc.Size = "small"
		}
		if _, ok := Profiles[svc.Size]; !ok {
			return Application{}, fmt.Errorf("%s.size: choose small, medium, large, compute or gpu", field)
		}
		if svc.Suspended && svc.Job != nil && svc.Job.Schedule == nil {
			return Application{}, fmt.Errorf("%s: deployment jobs cannot be stopped or resumed", field)
		}
		if svc.Replicas == 0 {
			svc.Replicas = 1
		}
		if svc.Replicas < 1 || svc.Replicas > 20 {
			return Application{}, fmt.Errorf("%s.replicas: must be between 1 and 20", field)
		}
		if svc.Healthcheck != "" {
			path, err := url.ParseRequestURI(svc.Healthcheck)
			if svc.Port == 0 || len(svc.Healthcheck) > 2048 || !strings.HasPrefix(svc.Healthcheck, "/") || strings.HasPrefix(svc.Healthcheck, "//") || err != nil || path.Host != "" || path.Fragment != "" {
				return Application{}, fmt.Errorf("%s.healthcheck: use an HTTP path on a service with a port, such as /readyz", field)
			}
		}
		if svc.UpdateStrategy != "" && svc.UpdateStrategy != "rolling" && svc.UpdateStrategy != "recreate" {
			return Application{}, fmt.Errorf("%s.update_strategy: choose rolling or recreate", field)
		}
		if len(svc.Env) > 128 {
			return Application{}, fmt.Errorf("%s.env: at most 128 variables are supported", field)
		}
		for key, value := range svc.Env {
			if sensitiveEnv(key, value) {
				return Application{}, fmt.Errorf("%s.env.%s: secret values cannot be stored in TOML or deployment history; use an application-scoped secret reference", field, key)
			}
			if !envPattern.MatchString(key) || len(key) > 128 || strings.IndexByte(value, 0) >= 0 || len(value) > 4096 {
				return Application{}, fmt.Errorf("%s.env.%s: invalid name or value (maximum 4096 bytes, no NUL)", field, key)
			}
		}
		if len(svc.Secrets) > 32 {
			return Application{}, fmt.Errorf("%s.secrets: at most 32 references", field)
		}
		for key, reference := range svc.Secrets {
			if !envPattern.MatchString(key) || len(key) > 128 || !reference.Valid() {
				return Application{}, fmt.Errorf("%s.secrets: invalid environment name or secret reference", field)
			}
			if _, exists := svc.Env[key]; exists {
				return Application{}, fmt.Errorf("%s: an environment variable cannot also be a secret reference", field)
			}
		}
		if err := ValidateCommand(svc.Command, svc.Args); err != nil {
			return Application{}, fmt.Errorf("%s: %s", field, err)
		}
		if svc.Networks == nil {
			svc.Networks = []string{"default"}
		} else if len(svc.Networks) == 0 {
			return Application{}, fmt.Errorf("%s.networks: an explicit empty list is unsupported; omit networks for default membership", field)
		}
		if err := validateMembers(svc.Networks, field+".networks", func(n string) bool { _, ok := app.Networks[n]; return ok }); err != nil {
			return Application{}, err
		}
		if err := validateMembers(svc.DependsOn, field+".depends_on", func(n string) bool {
			dependency, ok := app.Services[n]
			return ok && n != name && (dependency.Job == nil || dependency.Job.Schedule == nil)
		}); err != nil {
			return Application{}, err
		}
		sort.Strings(svc.Networks)
		sort.Strings(svc.DependsOn)
		if a := svc.Autoscaling; a != nil {
			if a.MinReplicas == 0 {
				a.MinReplicas = 1
			}
			if a.TargetCPU == 0 {
				a.TargetCPU = 70
			}
			if a.MinReplicas < 1 || a.MaxReplicas < a.MinReplicas || a.MaxReplicas > 20 || a.TargetCPU < 10 || a.TargetCPU > 95 {
				return Application{}, fmt.Errorf("%s.autoscaling: require 1 <= min_replicas <= max_replicas <= 20 and target_cpu between 10 and 95", field)
			}
			svc.Replicas = a.MinReplicas
		}
		if err := validateWorkload(svc); err != nil {
			return Application{}, fmt.Errorf("%s: %w", field, err)
		}
		if err := normalizeJobAndFiles(&svc); err != nil {
			return Application{}, fmt.Errorf("%s: %w", field, err)
		}
		app.Services[name] = svc
	}
	if err := validateNamedStorage(app); err != nil {
		return Application{}, err
	}
	if err := validateBindings(app); err != nil {
		return Application{}, err
	}
	if err := validateNetworkAccess(app); err != nil {
		return Application{}, err
	}
	if err := validateHTTPEndpoints(app); err != nil {
		return Application{}, err
	}
	for _, name := range Names(app) {
		svc := app.Services[name]
		if err := normalizeReadiness(&svc); err != nil {
			return Application{}, fmt.Errorf("services.%s: %w", name, err)
		}
		app.Services[name] = svc
	}
	if err := validatePublicTCP(app); err != nil {
		return Application{}, err
	}
	if _, err := Order(app); err != nil {
		return Application{}, err
	}
	return app, nil
}

func sensitiveEnv(key, value string) bool {
	upper := strings.ToUpper(key)
	for _, suffix := range []string{"PASSWORD", "PASSWD", "TOKEN", "SECRET", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY", "ACCESS_KEY_ID", "SECRET_KEY"} {
		if upper == suffix || strings.HasSuffix(upper, "_"+suffix) {
			return true
		}
	}
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		return true
	}
	return false
}

func validateMembers(values []string, field string, exists func(string) bool) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !exists(value) {
			return fmt.Errorf("%s: invalid or undeclared reference %q", field, value)
		}
		if seen[value] {
			return fmt.Errorf("%s: duplicate reference %q", field, value)
		}
		seen[value] = true
	}
	return nil
}

func validateImage(value string) error {
	if value == "" || len(value) > 512 || strings.ContainsAny(value, " \t\n\r\\?#") || strings.Contains(value, "://") || strings.HasPrefix(value, "/") || strings.Contains(value, "..") || strings.Count(value, "@") > 1 {
		return errors.New("use an OCI repository with a tag or sha256 digest, without a URL scheme or credentials")
	}
	if parts := strings.SplitN(value, "@", 2); len(parts) == 2 && (parts[0] == "" || !digestPattern.MatchString(parts[1])) {
		return errors.New("image digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	return nil
}

func Names(app Application) []string {
	names := make([]string, 0, len(app.Services))
	for name := range app.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Order gives a stable dependency-first order and rejects readiness cycles.
func Order(app Application) ([]string, error) {
	state := make(map[string]uint8, len(app.Services))
	order := make([]string, 0, len(app.Services))
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("services.%s.depends_on: readiness dependency cycle detected", name)
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, dependency := range app.Services[name].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	for _, name := range Names(app) {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Diff never returns environment values. Configuration history is not a safe
// place for secrets, but redacting all env values also protects mistaken input.
func Diff(before *Application, after Application) []Change {
	changes := make([]Change, 0)
	var old Application
	if before != nil {
		old = *before
	}
	add := func(service, field string, a, b any, sensitive bool) {
		if reflect.DeepEqual(a, b) {
			return
		}
		if sensitive {
			a, b = redact(a), redact(b)
		}
		changes = append(changes, Change{service, field, a, b, sensitive})
	}
	add("", "recovery", old.Recovery, after.Recovery, false)
	add("", "name", old.Name, after.Name, false)
	add("", "networks", old.Networks, after.Networks, false)
	add("", "volumes", old.Volumes, after.Volumes, false)
	add("", "domains", old.Domains, after.Domains, false)
	names := make(map[string]bool)
	for name := range old.Services {
		names[name] = true
	}
	for name := range after.Services {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		a, aok := old.Services[name]
		b, bok := after.Services[name]
		if !aok {
			add(name, "service", nil, "added", false)
		}
		if !bok {
			add(name, "service", "present", nil, false)
			continue
		}
		add(name, "image", a.Image, b.Image, false)
		add(name, "job", a.Job, b.Job, false)
		add(name, "bindings", a.Bindings, b.Bindings, false)
		add(name, "http", a.HTTP, b.HTTP, false)
		// File bodies belong in the reviewed source, never in event/diff summaries.
		if !reflect.DeepEqual(a.Files, b.Files) {
			changes = append(changes, Change{Service: name, Field: "files", Before: "[redacted]", After: "[redacted]", Sensitive: true})
		}
		add(name, "port", a.Port, b.Port, false)
		add(name, "ports", a.Ports, b.Ports, false)
		add(name, "public_tcp", a.PublicTCP, b.PublicTCP, false)
		add(name, "network_access", a.NetworkAccess, b.NetworkAccess, false)
		add(name, "public", a.Public, b.Public, false)
		add(name, "size", a.Size, b.Size, false)
		add(name, "replicas", a.Replicas, b.Replicas, false)
		add(name, "healthcheck", a.Healthcheck, b.Healthcheck, false)
		add(name, "readiness", a.Readiness, b.Readiness, false)
		add(name, "update_strategy", a.UpdateStrategy, b.UpdateStrategy, false)
		add(name, "env", a.Env, b.Env, true)
		add(name, "command", a.Command, b.Command, false)
		add(name, "args", a.Args, b.Args, false)
		add(name, "depends_on", a.DependsOn, b.DependsOn, false)
		add(name, "networks", a.Networks, b.Networks, false)
		add(name, "secrets", a.Secrets, b.Secrets, true)
		add(name, "autoscaling", a.Autoscaling, b.Autoscaling, false)
		add(name, "restart_nonce", a.RestartNonce, b.RestartNonce, false)
		add(name, "registry_credential", a.RegistryCredential, b.RegistryCredential, false)
		add(name, "tls", a.TLS, b.TLS, false)
		add(name, "aws_identity", a.AWSIdentity, b.AWSIdentity, false)
		add(name, "certificate_mounts", a.CertificateMounts, b.CertificateMounts, false)
		add(name, "volume", a.Volume, b.Volume, false)
		add(name, "mounts", a.Mounts, b.Mounts, false)
		add(name, "temporary_mounts", a.TemporaryMounts, b.TemporaryMounts, false)
		add(name, "gpu", a.GPU, b.GPU, false)
		add(name, "run_as_user", a.RunAsUser, b.RunAsUser, false)
		add(name, "run_as_group", a.RunAsGroup, b.RunAsGroup, false)
		add(name, "fs_group", a.FSGroup, b.FSGroup, false)
		add(name, "read_only_root_filesystem", a.ReadOnlyRootFilesystem, b.ReadOnlyRootFilesystem, false)
		add(name, "working_dir", a.WorkingDir, b.WorkingDir, false)
		add(name, "termination_grace_seconds", a.TerminationGraceSeconds, b.TerminationGraceSeconds, false)
		add(name, "architecture", a.Architecture, b.Architecture, false)
	}
	return changes
}

func redact(value any) any {
	switch x := value.(type) {
	case map[string]string:
		result := make(map[string]string, len(x))
		for key := range x {
			result[key] = "[redacted]"
		}
		return result
	default:
		// Permission-sensitive exposure and network choices remain reviewable;
		// the flag does not mean these values are secret.
		return value
	}
}

func Warnings(app Application) []string {
	warnings := make([]string, 0)
	if len(app.Domains) > 0 {
		warnings = append(warnings, "Custom domains are saved with this application. New domains remain inactive until you verify DNS ownership and activate them from Networking > Domains.")
	}
	for _, name := range Names(app) {
		svc := app.Services[name]
		if svc.Job != nil && svc.Job.Schedule != nil {
			warnings = append(warnings, name+": scheduled job uses "+svc.Job.Schedule.Timezone+" and skips overlapping runs. Pause stops future scheduling; an active run finishes independently.")
		} else if svc.Job != nil {
			warnings = append(warnings, name+": deployment job must finish before dependent services start. Retries and new revisions can repeat side effects; make migrations idempotent. Rollback does not undo database changes.")
		}
		if len(svc.Files) > 0 {
			warnings = append(warnings, name+": file content is stored in configuration history; use secret references for sensitive files. File updates take effect through a deployment.")
		}
		if svc.Volume != nil || len(svc.Mounts) > 0 {
			warnings = append(warnings, name+": persistent service uses one replica and Recreate updates, with brief downtime; rollback restores configuration, not database contents. Back up data separately.")
		}
		if svc.GPU != nil {
			warnings = append(warnings, name+": requires an installed NVIDIA device plugin and matching GPU capacity; model downloads and inference have their own memory and storage costs.")
		}
		internal, external := false, false
		for _, network := range svc.Networks {
			if app.Networks[network].Internal {
				internal = true
			} else {
				external = true
			}
		}
		if internal && external {
			warnings = append(warnings, name+": membership in an egress-enabled network permits external egress even with an internal network")
		}
		if svc.Public {
			warnings = append(warnings, name+": public HTTP exposure; TLS requires an operator-configured certificate issuer")
		}
	}
	return warnings
}

// ValidateCommand bounds exec-form process overrides. No implicit shell runs.
func ValidateCommand(command, args []string) error {
	if len(command) > 64 || len(args) > 128 {
		return fmt.Errorf("command/args exceed the supported element count")
	}
	for _, words := range [][]string{command, args} {
		for _, word := range words {
			if strings.IndexByte(word, 0) >= 0 || len(word) > 4096 {
				return fmt.Errorf("command/args must contain no NUL and at most 4096 bytes per element")
			}
		}
	}
	if len(command) > 0 && strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("command executable cannot be empty")
	}
	return nil
}
