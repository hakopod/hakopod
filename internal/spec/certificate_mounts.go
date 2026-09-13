package spec

import (
	"fmt"
	"strings"
)

// CertificateMount pins an upload or follows the service's ingress certificate.
// Private keys never enter application revisions.
type CertificateMount struct {
	Certificate string `json:"certificate,omitempty" toml:"certificate,omitempty"`
	Source      string `json:"source,omitempty" toml:"source,omitempty"`
	Hostname    string `json:"hostname" toml:"hostname"`
	MountPath   string `json:"mount_path" toml:"mount_path"`
}

func validateCertificateMounts(service Service) error {
	if len(service.CertificateMounts) > 4 {
		return fmt.Errorf("certificate_mounts: at most four certificate mounts per service")
	}
	seen := make(map[string]bool, len(service.CertificateMounts))
	for _, mount := range service.CertificateMounts {
		reference := mount.Certificate
		if mount.Source != "" {
			if mount.Source != "ingress" || mount.Certificate != "" || !service.Public {
				return fmt.Errorf("certificate_mounts: source must be ingress on a public HTTP service, without a certificate reference")
			}
			reference = "ingress:" + mount.Hostname
		} else if !runtimeName.MatchString(mount.Certificate) || strings.HasPrefix(mount.Certificate, "hp-auto-cert-") {
			return fmt.Errorf("certificate_mounts: use a managed uploaded certificate reference")
		}
		if seen[reference] {
			return fmt.Errorf("certificate_mounts: each certificate reference may be mounted once per service")
		}
		seen[reference] = true
		if !ValidHostname(mount.Hostname) || !allowedMount(mount.MountPath) {
			return fmt.Errorf("certificate_mounts: use a managed certificate reference, lowercase DNS hostname and clean absolute data directory")
		}
	}
	return nil
}

func HasAutomaticCertificates(app Application) bool {
	for _, svc := range app.Services {
		for _, mount := range svc.CertificateMounts {
			if mount.Source == "ingress" {
				return true
			}
		}
	}
	return false
}
