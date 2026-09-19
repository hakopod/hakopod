package spec

import "fmt"

// Serverless runs a public HTTP container at zero or one replica. Limits apply
// at the activation gateway; it never retries an application request.
type Serverless struct {
	MinReplicas           int32 `json:"min_replicas" toml:"min_replicas"`
	IdleSeconds           int   `json:"idle_seconds" toml:"idle_seconds"`
	StartupTimeoutSeconds int   `json:"startup_timeout_seconds" toml:"startup_timeout_seconds"`
	RequestTimeoutSeconds int   `json:"request_timeout_seconds" toml:"request_timeout_seconds"`
	MaxConcurrency        int   `json:"max_concurrency" toml:"max_concurrency"`
}

func normalizeServerless(s *Service) error {
	if s.Serverless == nil {
		return nil
	}
	v := *s.Serverless
	s.Serverless = &v
	if v.IdleSeconds == 0 {
		v.IdleSeconds = 300
	}
	if v.StartupTimeoutSeconds == 0 {
		v.StartupTimeoutSeconds = 60
	}
	if v.RequestTimeoutSeconds == 0 {
		v.RequestTimeoutSeconds = 60
	}
	if v.MaxConcurrency == 0 {
		v.MaxConcurrency = 16
	}
	if v.MinReplicas < 0 || v.MinReplicas > 1 || v.IdleSeconds < 30 || v.IdleSeconds > 86400 || v.StartupTimeoutSeconds < 5 || v.StartupTimeoutSeconds > 300 || v.RequestTimeoutSeconds < 1 || v.RequestTimeoutSeconds > 300 || v.MaxConcurrency < 1 || v.MaxConcurrency > 64 {
		return fmt.Errorf("serverless: min_replicas must be 0 or 1, idle_seconds 30–86400, startup_timeout_seconds 5–300, request_timeout_seconds 1–300 and max_concurrency 1–64")
	}
	if !s.Public || s.Port < 1 || s.Replicas != 1 || s.Job != nil || s.Autoscaling != nil || s.Volume != nil || len(s.Mounts) > 0 || s.GPU != nil || len(s.PublicTCP) > 0 || len(s.HTTP) > 0 || len(s.Ports) > 0 || len(s.CertificateMounts) > 0 {
		return fmt.Errorf("serverless requires one public HTTP port and one saved replica, without jobs, autoscaling, persistent mounts, GPU, extra ports or backend certificates")
	}
	return nil
}
