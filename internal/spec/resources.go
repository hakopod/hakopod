package spec

import (
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Resources overrides individual size-profile values, per replica or job attempt.
// Empty fields inherit the selected profile; zero never means unlimited.
type Resources struct {
	CPURequest    string `json:"cpu_request,omitempty" toml:"cpu_request,omitempty"`
	CPULimit      string `json:"cpu_limit,omitempty" toml:"cpu_limit,omitempty"`
	MemoryRequest string `json:"memory_request,omitempty" toml:"memory_request,omitempty"`
	MemoryLimit   string `json:"memory_limit,omitempty" toml:"memory_limit,omitempty"`
}

var cpuQuantity = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]{1,3})?|[0-9]+m)$`)
var memoryQuantity = regexp.MustCompile(`^[0-9]+(?:Ki|Mi|Gi|Ti|k|M|G|T)?$`)

// EffectiveResources must be used wherever resource budgets are consumed.
// Call Normalize before using untrusted service input.
func EffectiveResources(s Service) Profile {
	p, ok := Profiles[s.Size]
	if !ok {
		p = Profiles["small"]
	}
	if r := s.Resources; r != nil {
		if r.CPURequest != "" {
			p.CPURequest = r.CPURequest
		}
		if r.CPULimit != "" {
			p.CPULimit = r.CPULimit
		}
		if r.MemoryRequest != "" {
			p.MemoryRequest = r.MemoryRequest
		}
		if r.MemoryLimit != "" {
			p.MemoryLimit = r.MemoryLimit
		}
	}
	return p
}

func normalizeResources(s *Service) error {
	if s.Resources == nil {
		return nil
	}
	r := s.Resources
	for _, f := range []struct {
		name  string
		value *string
		cpu   bool
	}{
		{"cpu_request", &r.CPURequest, true}, {"cpu_limit", &r.CPULimit, true},
		{"memory_request", &r.MemoryRequest, false}, {"memory_limit", &r.MemoryLimit, false},
	} {
		if *f.value == "" {
			continue
		}
		pattern, lower, upper, help := memoryQuantity, "1Mi", "256Gi", "use whole bytes or Ki/Mi/Gi (1Mi–256Gi)"
		if f.cpu {
			pattern, lower, upper, help = cpuQuantity, "1m", "64", "use CPU cores or millicores (1m–64 cores), such as 250m or 0.5"
		}
		if len(*f.value) > 32 || !pattern.MatchString(*f.value) {
			return fmt.Errorf("resources.%s: %s", f.name, help)
		}
		q, err := resource.ParseQuantity(*f.value)
		if err != nil || q.Cmp(resource.MustParse(lower)) < 0 || q.Cmp(resource.MustParse(upper)) > 0 {
			return fmt.Errorf("resources.%s: %s", f.name, help)
		}
		*f.value = q.String()
	}
	if *r == (Resources{}) {
		s.Resources = nil
		return nil
	}
	p := EffectiveResources(*s)
	for _, f := range []struct{ name, request, limit string }{
		{"cpu_request", p.CPURequest, p.CPULimit}, {"memory_request", p.MemoryRequest, p.MemoryLimit},
	} {
		q := resource.MustParse(f.request)
		if q.Cmp(resource.MustParse(f.limit)) > 0 {
			return fmt.Errorf("resources.%s: request must not exceed its limit; omitted values inherit the size profile", f.name)
		}
	}
	return nil
}

// ValidateResourceCeiling enforces all four effective values, including values
// inherited from a profile. The caller owns the plan and scope authorization.
func ValidateResourceCeiling(s Service, ceiling Profile) error {
	if s.Resources != nil {
		copy := *s.Resources
		s.Resources = &copy
	}
	if err := normalizeResources(&s); err != nil {
		return err
	}
	p := EffectiveResources(s)
	for _, f := range []struct{ name, value, maximum string }{
		{"cpu_request", p.CPURequest, ceiling.CPURequest}, {"cpu_limit", p.CPULimit, ceiling.CPULimit},
		{"memory_request", p.MemoryRequest, ceiling.MemoryRequest}, {"memory_limit", p.MemoryLimit, ceiling.MemoryLimit},
	} {
		q := resource.MustParse(f.value)
		if q.Cmp(resource.MustParse(f.maximum)) > 0 {
			return fmt.Errorf("resources.%s: this compute allows at most %s per service replica", f.name, f.maximum)
		}
	}
	return nil
}
