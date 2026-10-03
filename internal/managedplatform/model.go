// Package managedplatform describes coordinated stacks separately from database engines.
// A plan is desired configuration, never proof that a service is available.
package managedplatform

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const MaxComponents = 32

const (
	MaxManagedPlatformSnapshotBytes          = 4 << 20
	MaxManagedPlatformEncryptedSnapshotBytes = MaxManagedPlatformSnapshotBytes + 12 + 16
)

type SecretReference struct {
	Name     string `json:"name" toml:"name"`
	Revision int64  `json:"revision" toml:"revision"`
}

func (s SecretReference) Validate() error {
	if s.Revision < 1 || len(s.Name) > 63 || len(validation.IsDNS1123Label(s.Name)) != 0 {
		return fmt.Errorf("secret references require a valid name and a positive immutable revision")
	}
	return nil
}

func ValidateHTTPSOrigin(value, field string) error {
	u, err := url.Parse(value)
	if err != nil || len(value) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || strings.Contains(value, "#") || u.RawPath != "" || u.Path != "" && u.Path != "/" || u.Opaque != "" {
		return fmt.Errorf("%s requires an exact HTTPS origin without credentials", field)
	}
	if u.Port() != "" && u.Port() != "443" {
		return fmt.Errorf("%s must use port 443", field)
	}
	host := u.Hostname()
	if host == "" || net.ParseIP(host) == nil && len(validation.IsDNS1123Subdomain(host)) != 0 {
		return fmt.Errorf("%s requires a valid hostname", field)
	}
	return nil
}

type Resources struct {
	CPU    string `json:"cpu" toml:"cpu"`
	Memory string `json:"memory" toml:"memory"`
}

type Placement = database.Placement

type Spec struct {
	SchemaVersion int                        `json:"schema_version" toml:"schema_version"`
	Name          string                     `json:"name" toml:"name"`
	Kind          string                     `json:"kind" toml:"kind"`
	Version       string                     `json:"version" toml:"version"`
	TLSMode       string                     `json:"tls_mode,omitempty" toml:"tls_mode"`
	Resources     map[string]Resources       `json:"resources" toml:"resources"`
	Storage       map[string]int64           `json:"storage" toml:"storage"`
	Secrets       map[string]SecretReference `json:"secrets" toml:"secrets"`
	Placement     Placement                  `json:"placement,omitempty" toml:"placement"`
	Supabase      *SupabaseConfig            `json:"supabase,omitempty" toml:"supabase"`
	Neon          *NeonConfig                `json:"neon,omitempty" toml:"neon"`
}

func (s Spec) Validate() error {
	if s.TLSMode != "" && s.TLSMode != "operator" && s.TLSMode != "managed" {
		return fmt.Errorf("tls_mode must be managed or operator")
	}
	if s.SchemaVersion != 1 || len(s.Name) > 40 || len(validation.IsDNS1123Label(s.Name)) != 0 {
		return fmt.Errorf("managed platforms require schema_version 1 and a valid name of at most 40 characters")
	}
	switch s.Kind {
	case "supabase":
		if s.Neon != nil {
			return fmt.Errorf("Neon configuration belongs only to Neon platforms")
		}
		return s.ValidateSupabase()
	case "neon":
		if s.Supabase != nil {
			return fmt.Errorf("Supabase configuration belongs only to Supabase platforms")
		}
		return s.ValidateNeon()
	default:
		return fmt.Errorf("platform kind must be neon or supabase")
	}
}

func exactKeys[V any](values map[string]V, required []string, field string) error {
	if len(required) == 0 || len(required) > MaxComponents || len(values) != len(required) {
		return fmt.Errorf("%s must contain the complete supported inventory", field)
	}
	seen := map[string]bool{}
	for _, key := range required {
		if seen[key] || len(validation.IsDNS1123Label(key)) != 0 {
			return fmt.Errorf("invalid supported %s inventory", field)
		}
		seen[key] = true
		if _, ok := values[key]; !ok {
			return fmt.Errorf("%s requires %s", field, key)
		}
	}
	return nil
}

func (s Spec) ValidateResources(required []string) error {
	if err := exactKeys(s.Resources, required, "resources"); err != nil {
		return err
	}
	for _, key := range required {
		r := s.Resources[key]
		cpu, err := resource.ParseQuantity(r.CPU)
		if err != nil || cpu.Cmp(resource.MustParse("100m")) < 0 || cpu.Cmp(resource.MustParse("16")) > 0 {
			return fmt.Errorf("resources.%s.cpu must be between 100m and 16 cores", key)
		}
		memory, err := resource.ParseQuantity(r.Memory)
		if err != nil || memory.Cmp(resource.MustParse("128Mi")) < 0 || memory.Cmp(resource.MustParse("64Gi")) > 0 {
			return fmt.Errorf("resources.%s.memory must be between 128Mi and 64Gi", key)
		}
	}
	return nil
}

func (s Spec) ValidateStorage(required []string) error {
	if err := exactKeys(s.Storage, required, "storage"); err != nil {
		return err
	}
	for _, key := range required {
		if s.Storage[key] < 1 || s.Storage[key] > 1024 {
			return fmt.Errorf("storage.%s must be between 1 and 1024 GiB", key)
		}
	}
	return nil
}

func (s Spec) ValidateSecrets(required []string) error {
	if s.TLSMode == "managed" {
		required = s.UserSecretKeys(required)
	}
	if err := exactKeys(s.Secrets, required, "secrets"); err != nil {
		return err
	}
	for _, key := range required {
		if s.TLSMode == "managed" && strings.HasPrefix(s.Secrets[key].Name, "platform-tls-") {
			return fmt.Errorf("secrets.%s uses a controller-reserved name", key)
		}
		if err := s.Secrets[key].Validate(); err != nil {
			return fmt.Errorf("secrets.%s: %w", key, err)
		}
	}
	return nil
}

var imagePart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var imageDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidateImages checks reference shape. Qualification must verify registry
// content, architectures and source provenance separately.
func ValidateImages(images map[string]string, required []string) error {
	if err := exactKeys(images, required, "images"); err != nil {
		return err
	}
	for _, key := range required {
		image := images[key]
		name, digest, ok := strings.Cut(image, "@sha256:")
		if !ok || len(image) > 512 || strings.Count(image, "@") != 1 || !imageDigest.MatchString(digest) {
			return fmt.Errorf("images.%s requires an explicit registry and SHA-256 digest", key)
		}
		if last := strings.LastIndex(name, ":"); last > strings.LastIndex(name, "/") {
			if !imageTag.MatchString(name[last+1:]) {
				return fmt.Errorf("images.%s has an invalid tag", key)
			}
			name = name[:last]
		}
		parts := strings.Split(name, "/")
		if len(parts) < 2 || !strings.Contains(parts[0], ".") || len(validation.IsDNS1123Subdomain(parts[0])) != 0 {
			return fmt.Errorf("images.%s requires an explicit registry hostname", key)
		}
		for _, part := range parts[1:] {
			if !imagePart.MatchString(part) {
				return fmt.Errorf("images.%s has an invalid repository", key)
			}
		}
	}
	return nil
}

type Capability struct {
	Available        bool   `json:"available"`
	ClusterQualified bool   `json:"cluster_qualified"`
	PublicQualified  bool   `json:"public_qualified"`
	Reason           string `json:"reason"`
}

type Component struct {
	Name        string    `json:"name"`
	Image       string    `json:"image"`
	Resources   Resources `json:"resources"`
	Replicas    int       `json:"replicas"`
	Ports       []int32   `json:"ports"`
	SecretKeys  []string  `json:"secret_keys"`
	StorageKeys []string  `json:"storage_keys"`
}

type Plan struct {
	Namespace              string      `json:"namespace"`
	Components             []Component `json:"components"`
	PublicService          string      `json:"public_service"`
	StorageClass           string      `json:"storage_class"`
	SchedulingPool         string      `json:"scheduling_pool,omitempty"`
	SchedulingRuntimeClass string      `json:"scheduling_runtime_class,omitempty"`
	Capability             Capability  `json:"capability"`
}
