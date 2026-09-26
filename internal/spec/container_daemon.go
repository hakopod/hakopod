package spec

import (
	"fmt"
	"strings"
)

// ContainerDaemonDirectory is reserved for the client certificate, key and
// certificate authority that prove this service to its granted daemon.
const ContainerDaemonDirectory = "/var/run/secrets/hakopod/container-daemon"

// validateContainerDaemon checks the shape of the binding name and the surface
// the cluster layer owns. It deliberately does not check that a binding exists:
// a specification is portable, while grants belong to one installation.
func validateContainerDaemon(s Service) error {
	if s.ContainerDaemon == "" {
		return nil
	}
	if !runtimeName.MatchString(s.ContainerDaemon) {
		return fmt.Errorf("container_daemon: use an operator-approved binding name of at most 63 lowercase letters, digits or hyphens")
	}
	// Keep one daemon endpoint. An application that sets DOCKER_HOST would aim
	// the injected client certificate at a daemon it was never granted.
	for key := range s.Env {
		if containerDaemonReservedEnv(key) {
			return fmt.Errorf("env.%s: managed by container_daemon", key)
		}
	}
	for key := range s.Secrets {
		if containerDaemonReservedEnv(key) {
			return fmt.Errorf("secrets.%s: managed by container_daemon", key)
		}
	}
	for key := range s.Bindings {
		if containerDaemonReservedEnv(key) {
			return fmt.Errorf("bindings.%s: managed by container_daemon", key)
		}
	}
	paths := []string{}
	if s.Volume != nil {
		paths = append(paths, s.Volume.MountPath)
	}
	for _, m := range s.Mounts {
		paths = append(paths, m.MountPath)
	}
	for _, m := range s.CertificateMounts {
		paths = append(paths, m.MountPath)
	}
	for _, m := range s.TemporaryMounts {
		paths = append(paths, m.MountPath)
	}
	for _, p := range paths {
		if p == ContainerDaemonDirectory || strings.HasPrefix(p, ContainerDaemonDirectory+"/") || strings.HasPrefix(ContainerDaemonDirectory, p+"/") {
			return fmt.Errorf("container_daemon: a mount overlaps the reserved trust directory")
		}
	}
	return nil
}

func containerDaemonReservedEnv(key string) bool {
	switch strings.ToUpper(key) {
	case "DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_CONFIG", "DOCKER_API_VERSION", "BUILDKIT_HOST":
		return true
	}
	return false
}
