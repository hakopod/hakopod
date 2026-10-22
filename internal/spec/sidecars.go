package spec

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

// MaxSidecars bounds the extra containers one service pod can carry.
const MaxSidecars = 4

// Sidecar is an extra container that runs next to the service container for
// the whole life of the pod. It starts first and stops last. A sidecar gets the
// same restricted security settings as the service container; anything more
// comes only from an operator-approved node profile.
type Sidecar struct {
	Name    string               `json:"name" toml:"name"`
	Image   string               `json:"image" toml:"image"`
	Command []string             `json:"command,omitempty" toml:"command"`
	Args    []string             `json:"args,omitempty" toml:"args"`
	Env     map[string]string    `json:"env,omitempty" toml:"env"`
	Secrets map[string]SecretRef `json:"secrets,omitempty" toml:"secrets"`
	// CPU and Memory are required. Each is both the request and the limit, so a
	// sidecar never runs unbounded and its cost is the same in every plan.
	CPU    string         `json:"cpu" toml:"cpu"`
	Memory string         `json:"memory" toml:"memory"`
	Mounts []SidecarMount `json:"mounts,omitempty" toml:"mounts"`
}

// SidecarMount shares one of the service's own mounts with a sidecar. Name
// exactly one source: a named volume the service mounts, or the mount_path of
// one of its temporary mounts. The sidecar picks its own mount_path.
type SidecarMount struct {
	Volume    string `json:"volume,omitempty" toml:"volume"`
	Temporary string `json:"temporary,omitempty" toml:"temporary"`
	MountPath string `json:"mount_path" toml:"mount_path"`
	ReadOnly  bool   `json:"read_only,omitempty" toml:"read_only"`
}

// Names the platform already uses for helper containers in a service pod.
var reservedSidecarNames = map[string]bool{"app": true, "install-readiness-probe": true, "install-guard": true, "docker": true, "prepare": true}

func SidecarSecretKey(sidecar, variable string) string {
	return "__sidecar_" + sidecar + "__" + variable
}

func sidecarSecretReferences(s Service, into map[string]SecretRef) {
	for _, sc := range s.Sidecars {
		for key, ref := range sc.Secrets {
			into[SidecarSecretKey(sc.Name, key)] = ref
		}
	}
}

func normalizeSidecars(name string, s *Service) error {
	if len(s.Sidecars) == 0 {
		return nil
	}
	if s.Actions != nil || s.Serverless != nil || s.Session != nil || s.Job != nil {
		return fmt.Errorf("sidecars are available only for regular services, not Managed Actions, serverless, session or job services")
	}
	if len(s.Sidecars) > MaxSidecars {
		return fmt.Errorf("sidecars: at most %d per service", MaxSidecars)
	}
	seen := map[string]bool{}
	for i := range s.Sidecars {
		sc := &s.Sidecars[i]
		field := fmt.Sprintf("sidecars[%d]", i)
		if len(validation.IsDNS1123Label(sc.Name)) != 0 {
			return fmt.Errorf("%s.name: use at most 63 lowercase letters, digits or hyphens", field)
		}
		if sc.Name == name || reservedSidecarNames[sc.Name] || strings.HasPrefix(sc.Name, "hakopod-") {
			return fmt.Errorf("%s.name: %q is reserved or matches the service name", field, sc.Name)
		}
		if seen[sc.Name] {
			return fmt.Errorf("%s.name: duplicate sidecar %q", field, sc.Name)
		}
		seen[sc.Name] = true
		field = "sidecars." + sc.Name
		if err := validateImage(sc.Image); err != nil {
			return fmt.Errorf("%s.image: %w", field, err)
		}
		if !strings.Contains(sc.Image, "@sha256:") {
			return fmt.Errorf("%s.image: pin the image by digest, for example repository@sha256:<64 hex characters>", field)
		}
		if err := ValidateCommand(sc.Command, sc.Args); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		if err := validateEnvironment(field+".env", sc.Env); err != nil {
			return err
		}
		if len(sc.Secrets) > 16 {
			return fmt.Errorf("%s.secrets: at most 16 references", field)
		}
		for key, ref := range sc.Secrets {
			if !envPattern.MatchString(key) || len(key) > 128 || !ref.Valid() {
				return fmt.Errorf("%s.secrets: invalid environment name or secret reference", field)
			}
			if _, exists := sc.Env[key]; exists {
				return fmt.Errorf("%s: an environment variable cannot also be a secret reference", field)
			}
		}
		if sc.CPU == "" || sc.Memory == "" {
			return fmt.Errorf("%s: cpu and memory are required", field)
		}
		// Reuse the service's own quantity parsing and bounds.
		check := Service{Size: "small", Resources: &Resources{CPURequest: sc.CPU, CPULimit: sc.CPU, MemoryRequest: sc.Memory, MemoryLimit: sc.Memory}}
		if err := normalizeResources(&check); err != nil {
			return fmt.Errorf("%s.%w", field, err)
		}
		sc.CPU, sc.Memory = check.Resources.CPURequest, check.Resources.MemoryRequest
		if err := validateSidecarMounts(field, *s, sc.Mounts); err != nil {
			return err
		}
	}
	return nil
}

func validateSidecarMounts(field string, s Service, mounts []SidecarMount) error {
	if len(mounts) > 8 {
		return fmt.Errorf("%s.mounts: at most 8", field)
	}
	volumes, temporary := map[string]bool{}, map[string]bool{}
	for _, m := range s.Mounts {
		volumes[m.Volume] = true
	}
	for _, m := range s.TemporaryMounts {
		temporary[m.MountPath] = true
	}
	paths := []string{}
	for _, m := range mounts {
		if (m.Volume == "") == (m.Temporary == "") {
			return fmt.Errorf("%s.mounts: name exactly one of volume or temporary", field)
		}
		if m.Volume != "" && !volumes[m.Volume] || m.Temporary != "" && !temporary[m.Temporary] {
			return fmt.Errorf("%s.mounts: share only a volume the service mounts or the mount_path of one of its temporary_mounts", field)
		}
		if !allowedMount(m.MountPath) {
			return fmt.Errorf("%s.mounts: use a clean absolute data directory", field)
		}
		for _, other := range paths {
			if other == m.MountPath || strings.HasPrefix(other, m.MountPath+"/") || strings.HasPrefix(m.MountPath, other+"/") {
				return fmt.Errorf("%s.mounts: mount paths must not overlap", field)
			}
		}
		paths = append(paths, m.MountPath)
	}
	return nil
}

// redactSidecars keeps diffs reviewable without printing environment values.
func redactSidecars(in []Sidecar) []Sidecar {
	if in == nil {
		return nil
	}
	out := make([]Sidecar, len(in))
	for i, sc := range in {
		sc.Env = redact(sc.Env).(map[string]string)
		out[i] = sc
	}
	return out
}

func SidecarNames(s Service) []string {
	names := make([]string, 0, len(s.Sidecars))
	for _, sc := range s.Sidecars {
		names = append(names, sc.Name)
	}
	sort.Strings(names)
	return names
}

// PodResources is the service's own budget plus every sidecar, per replica. Use
// it wherever capacity, quota or reservations are counted, so a sidecar costs
// exactly what the service container costs.
func PodResources(s Service) Profile {
	p := EffectiveResources(s)
	if len(s.Sidecars) == 0 {
		return p
	}
	sum := func(base string, add func(Sidecar) string) string {
		q := resource.MustParse(base)
		for _, sc := range s.Sidecars {
			q.Add(resource.MustParse(add(sc)))
		}
		return q.String()
	}
	cpu := func(sc Sidecar) string { return sc.CPU }
	memory := func(sc Sidecar) string { return sc.Memory }
	p.CPURequest, p.CPULimit = sum(p.CPURequest, cpu), sum(p.CPULimit, cpu)
	p.MemoryRequest, p.MemoryLimit = sum(p.MemoryRequest, memory), sum(p.MemoryLimit, memory)
	return p
}
