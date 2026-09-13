package spec

import "fmt"

// CertificateMount references an immutable service-owned certificate. Private
// keys are uploaded separately and never enter an application revision.
type CertificateMount struct {
	Certificate string `json:"certificate" toml:"certificate"`
	Hostname    string `json:"hostname" toml:"hostname"`
	MountPath   string `json:"mount_path" toml:"mount_path"`
}

func validateCertificateMounts(service Service) error {
	if len(service.CertificateMounts) > 4 {
		return fmt.Errorf("certificate_mounts: at most four certificate mounts per service")
	}
	seen := make(map[string]bool, len(service.CertificateMounts))
	for _, mount := range service.CertificateMounts {
		if seen[mount.Certificate] {
			return fmt.Errorf("certificate_mounts: each certificate reference may be mounted once per service")
		}
		seen[mount.Certificate] = true
		if !runtimeName.MatchString(mount.Certificate) || !ValidHostname(mount.Hostname) || !allowedMount(mount.MountPath) {
			return fmt.Errorf("certificate_mounts: use a managed certificate reference, lowercase DNS hostname and clean absolute data directory")
		}
	}
	return nil
}
