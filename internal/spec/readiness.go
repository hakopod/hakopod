package spec

import (
	"fmt"
	"path"
	"strings"
)

const ReadinessHelperDirectory = "/var/run/secrets/hakopod-probe"

// Readiness checks a declared listener. When healthcheck is also set, both
// checks must pass before the pod receives traffic.
type Readiness struct {
	Protocol         string `json:"protocol" toml:"protocol"`
	Port             int32  `json:"port" toml:"port"`
	TLSServerName    string `json:"tls_server_name,omitempty" toml:"tls_server_name"`
	TLSCAFile        string `json:"tls_ca_file,omitempty" toml:"tls_ca_file"`
	PeriodSeconds    int32  `json:"period_seconds,omitempty" toml:"period_seconds"`
	TimeoutSeconds   int32  `json:"timeout_seconds,omitempty" toml:"timeout_seconds"`
	FailureThreshold int32  `json:"failure_threshold,omitempty" toml:"failure_threshold"`
}

func NeedsReadinessHelper(s Service) bool {
	return s.Readiness != nil && (s.Readiness.Protocol != "tcp" || s.Healthcheck != "")
}

func normalizeReadiness(s *Service) error {
	r := s.Readiness
	if r == nil {
		return nil
	}
	if r.Protocol != "tcp" && r.Protocol != "smtp" && r.Protocol != "smtp_starttls" {
		return fmt.Errorf("readiness.protocol: choose tcp, smtp or smtp_starttls")
	}
	found := false
	for _, port := range ServicePorts(*s) {
		found = found || port.TargetPort == r.Port && port.Protocol == "TCP"
	}
	if r.Port < 1 || !found {
		return fmt.Errorf("readiness.port: use a declared TCP target port")
	}
	if r.Protocol == "smtp_starttls" {
		if !ValidHostname(r.TLSServerName) {
			return fmt.Errorf("readiness.tls_server_name: a lowercase DNS hostname is required for certificate verification")
		}
		if r.TLSCAFile != "" && (!path.IsAbs(r.TLSCAFile) || path.Clean(r.TLSCAFile) != r.TLSCAFile || len(r.TLSCAFile) > 256 || strings.ContainsAny(r.TLSCAFile, "\x00\r\n")) {
			return fmt.Errorf("readiness.tls_ca_file: use a clean absolute path to a PEM trust bundle in the container")
		}
	} else if r.TLSServerName != "" || r.TLSCAFile != "" {
		return fmt.Errorf("readiness: TLS settings require smtp_starttls")
	}
	if r.PeriodSeconds == 0 {
		r.PeriodSeconds = 5
	}
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = 3
	}
	if r.FailureThreshold == 0 {
		r.FailureThreshold = 3
	}
	if r.PeriodSeconds < 3 || r.PeriodSeconds > 60 || r.TimeoutSeconds < 1 || r.TimeoutSeconds > 10 || r.TimeoutSeconds >= r.PeriodSeconds || r.FailureThreshold < 1 || r.FailureThreshold > 10 {
		return fmt.Errorf("readiness: period_seconds must be 3–60, timeout_seconds 1–10 and less than the period, and failure_threshold 1–10")
	}
	return nil
}
