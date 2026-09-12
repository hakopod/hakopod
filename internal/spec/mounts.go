package spec

import (
	"fmt"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

type NamedVolume struct {
	SizeGiB      int64  `json:"size_gib" toml:"size_gib"`
	StorageClass string `json:"storage_class,omitempty" toml:"storage_class"`
	AccessMode   string `json:"access_mode" toml:"access_mode"`
}

type Mount struct {
	Volume    string `json:"volume" toml:"volume"`
	MountPath string `json:"mount_path" toml:"mount_path"`
	ReadOnly  bool   `json:"read_only,omitempty" toml:"read_only"`
	SubPath   string `json:"sub_path,omitempty" toml:"sub_path"`
}

type TemporaryMount struct {
	MountPath string `json:"mount_path" toml:"mount_path"`
	SizeMiB   int64  `json:"size_mib" toml:"size_mib"`
	Memory    bool   `json:"memory,omitempty" toml:"memory"`
}

func cleanDirectory(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && len(p) <= 200 && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}

func allowedMount(p string) bool {
	if !cleanDirectory(p) {
		return false
	}
	for _, reserved := range []string{"/proc", "/sys", "/dev", "/etc", "/var/run/secrets"} {
		if p == reserved || strings.HasPrefix(p, reserved+"/") || strings.HasPrefix(reserved, p+"/") {
			return false
		}
	}
	return true
}

func validateMountControls(s Service) error {
	for _, id := range []int64{s.RunAsGroup, s.FSGroup} {
		if id < 0 || id > 2147483647 {
			return fmt.Errorf("run_as_group and fs_group must be positive IDs or omitted")
		}
	}
	if s.WorkingDir != "" && s.WorkingDir != "/" && !cleanDirectory(s.WorkingDir) {
		return fmt.Errorf("working_dir must be a clean absolute directory")
	}
	if s.TerminationGraceSeconds < 0 || s.TerminationGraceSeconds > 300 {
		return fmt.Errorf("termination_grace_seconds must be 1–300 or omitted for 30 seconds")
	}
	if len(s.Mounts)+len(s.TemporaryMounts) > 16 {
		return fmt.Errorf("at most 16 named and temporary mounts per service")
	}
	paths := []string{}
	if s.Volume != nil {
		paths = append(paths, s.Volume.MountPath)
	}
	for _, m := range s.Mounts {
		if !namePattern.MatchString(m.Volume) || !allowedMount(m.MountPath) {
			return fmt.Errorf("mounts: use a named volume and a clean absolute data directory")
		}
		if m.SubPath != "" && (path.IsAbs(m.SubPath) || path.Clean(m.SubPath) != m.SubPath || m.SubPath == "." || m.SubPath == ".." || strings.HasPrefix(m.SubPath, "../") || len(m.SubPath) > 200 || strings.ContainsAny(m.SubPath, "\x00\r\n")) {
			return fmt.Errorf("mounts.sub_path must be a clean relative directory without traversal")
		}
		paths = append(paths, m.MountPath)
	}
	var temporarySize int64
	for _, m := range s.TemporaryMounts {
		if !allowedMount(m.MountPath) || m.SizeMiB < 1 || m.SizeMiB > 128 {
			return fmt.Errorf("temporary_mounts: use a data directory and size_mib between 1 and 128")
		}
		temporarySize += m.SizeMiB
		paths = append(paths, m.MountPath)
	}
	if temporarySize > 128 {
		return fmt.Errorf("temporary_mounts: combined capacity must not exceed 128 MiB")
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return fmt.Errorf("mount paths must not overlap")
			}
		}
	}
	return nil
}

func validateNamedStorage(app Application) error {
	if len(app.Volumes) > 16 {
		return fmt.Errorf("volumes: at most 16 named volumes")
	}
	var totalSize int64
	totalCount := len(app.Volumes)
	claims := map[string]bool{}
	for name, v := range app.Volumes {
		if !namePattern.MatchString(name) || v.SizeGiB < 1 || v.SizeGiB > 200 {
			return fmt.Errorf("volumes: use valid names and sizes between 1 and 200 GiB")
		}
		if v.StorageClass != "" && len(validation.IsDNS1123Subdomain(v.StorageClass)) > 0 {
			return fmt.Errorf("volumes.%s: invalid storage class", name)
		}
		if v.AccessMode == "" {
			v.AccessMode = "ReadWriteOnce"
		}
		if v.AccessMode != "ReadWriteOnce" && v.AccessMode != "ReadWriteMany" {
			return fmt.Errorf("volumes.%s.access_mode: choose ReadWriteOnce or ReadWriteMany", name)
		}
		if v.AccessMode == "ReadWriteMany" && v.StorageClass == "" {
			return fmt.Errorf("volumes.%s: shared storage requires an explicit ReadWriteMany-capable storage_class", name)
		}
		app.Volumes[name] = v
		claims["hakopod-volume-"+name] = true
		totalSize += v.SizeGiB
	}
	users := map[string]map[string]bool{}
	groups := map[string]int64{}
	for _, name := range Names(app) {
		s := app.Services[name]
		if s.Volume != nil {
			if claims[name+"-data"] {
				return fmt.Errorf("services.%s.volume: legacy volume name conflicts with a named volume; rename the named volume", name)
			}
			totalSize += s.Volume.SizeGiB
			totalCount++
		}
		if len(s.Mounts) > 0 && (s.Replicas != 1 || s.Autoscaling != nil) {
			return fmt.Errorf("services.%s: persistent services require one replica without autoscaling", name)
		}
		group := int64(10001)
		if s.RunAsUser > 0 {
			group = s.RunAsUser
		}
		if s.FSGroup > 0 {
			group = s.FSGroup
		}
		for _, mount := range s.Mounts {
			v, ok := app.Volumes[mount.Volume]
			if !ok {
				return fmt.Errorf("services.%s.mounts: unknown volume %s", name, mount.Volume)
			}
			if users[mount.Volume] == nil {
				users[mount.Volume] = map[string]bool{}
			}
			users[mount.Volume][name] = true
			if len(users[mount.Volume]) > 1 && v.AccessMode != "ReadWriteMany" {
				return fmt.Errorf("volumes.%s: mounts across services require ReadWriteMany storage", mount.Volume)
			}
			if old, set := groups[mount.Volume]; set && old != group {
				return fmt.Errorf("volumes.%s: services sharing a volume must use the same fs_group", mount.Volume)
			}
			groups[mount.Volume] = group
		}
	}
	if totalCount > 20 || totalSize > 200 {
		return fmt.Errorf("volumes: application storage is limited to 20 volumes and 200 GiB in total")
	}
	return nil
}
