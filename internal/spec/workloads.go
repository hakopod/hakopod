package spec

import (
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"path"
	"strings"
)

type Volume struct {
	MountPath    string `json:"mount_path" toml:"mount_path"`
	SizeGiB      int64  `json:"size_gib" toml:"size_gib"`
	StorageClass string `json:"storage_class,omitempty" toml:"storage_class"`
}
type GPU struct {
	Count int64 `json:"count" toml:"count"`
}

func validateWorkload(s Service) error {
	if err := validateMountControls(s); err != nil {
		return err
	}
	if s.Architecture != "" && s.Architecture != "amd64" && s.Architecture != "arm64" {
		return fmt.Errorf("architecture must be amd64 or arm64")
	}
	if s.RunAsUser < 0 || s.RunAsUser > 2147483647 {
		return fmt.Errorf("run_as_user must be a positive unprivileged UID")
	}
	if v := s.Volume; v != nil {
		if s.Replicas != 1 || s.Autoscaling != nil {
			return fmt.Errorf("persistent services require exactly one replica without autoscaling")
		}
		if v.SizeGiB < 1 || v.SizeGiB > 200 {
			return fmt.Errorf("volume.size_gib must be 1–200")
		}
		if !strings.HasPrefix(v.MountPath, "/") || v.MountPath == "/" || len(v.MountPath) > 200 || path.Clean(v.MountPath) != v.MountPath || strings.ContainsAny(v.MountPath, "\x00\r\n") {
			return fmt.Errorf("volume.mount_path must be a clean absolute data directory")
		}
		for _, reserved := range []string{"/proc", "/sys", "/dev", "/etc", "/run", "/var/run"} {
			if v.MountPath == reserved || strings.HasPrefix(v.MountPath, reserved+"/") {
				return fmt.Errorf("volume cannot replace a system directory")
			}
		}
		if v.StorageClass != "" && len(validation.IsDNS1123Subdomain(v.StorageClass)) > 0 {
			return fmt.Errorf("invalid storage class")
		}
	}
	if g := s.GPU; g != nil {
		if g.Count < 1 || g.Count > 8 || s.Autoscaling != nil {
			return fmt.Errorf("gpu.count must be 1–8; GPU autoscaling is not supported")
		}
	}
	return nil
}
