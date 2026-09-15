package spec

import (
	"fmt"
	"github.com/robfig/cron/v3"
	"strings"
	"time"
	_ "time/tzdata"
)

// Job runs to completion once per release revision. DependsOn waits for jobs
// to complete and for long-running services to become ready.
type JobSchedule struct {
	Cron         string `json:"cron" toml:"cron"`
	Timezone     string `json:"timezone" toml:"timezone"`
	HistoryLimit int32  `json:"history_limit" toml:"history_limit"`
}
type Job struct {
	Schedule       *JobSchedule `json:"schedule,omitempty" toml:"schedule"`
	TimeoutSeconds int64        `json:"timeout_seconds" toml:"timeout_seconds"`
	Retries        int32        `json:"retries" toml:"retries"`
}

// File mounts exactly one read-only file, not an operator host directory.
// Content is public configuration; sensitive bodies use a scoped SecretRef.
type File struct {
	MountPath string     `json:"mount_path" toml:"mount_path"`
	Content   *string    `json:"content,omitempty" toml:"content"`
	Secret    *SecretRef `json:"secret,omitempty" toml:"secret"`
	Mode      int32      `json:"mode,omitempty" toml:"mode"`
}

func FileSecretKey(name string) string { return "__file_" + name }

// SecretReferences includes file sources without making them container env vars.
func SecretReferences(s Service) map[string]SecretRef {
	refs := make(map[string]SecretRef, len(s.Secrets)+len(s.Files))
	for key, ref := range s.Secrets {
		refs[key] = ref
	}
	for name, file := range s.Files {
		if file.Secret != nil {
			refs[FileSecretKey(name)] = *file.Secret
		}
	}
	for key, binding := range s.Bindings {
		if binding.Password != nil {
			refs[BindingSecretKey(key)] = *binding.Password
		}
	}
	return refs
}

func normalizeJobAndFiles(s *Service) error {
	if j := s.Job; j != nil {
		if schedule := j.Schedule; schedule != nil {
			if len(schedule.Cron) > 100 || len(strings.Fields(schedule.Cron)) != 5 {
				return fmt.Errorf("job.schedule.cron: use a five-field cron expression")
			}
			if _, err := cron.ParseStandard(schedule.Cron); err != nil {
				return fmt.Errorf("job.schedule.cron: invalid cron expression")
			}
			if schedule.Timezone == "" {
				schedule.Timezone = "UTC"
			}
			if len(schedule.Timezone) > 100 {
				return fmt.Errorf("job.schedule.timezone: invalid timezone")
			}
			if _, err := time.LoadLocation(schedule.Timezone); err != nil {
				return fmt.Errorf("job.schedule.timezone: use an IANA timezone")
			}
			if schedule.HistoryLimit == 0 {
				schedule.HistoryLimit = 1
			}
			if schedule.HistoryLimit < 1 || schedule.HistoryLimit > 2 {
				return fmt.Errorf("job.schedule.history_limit: use 1 or 2 retained successes and failures")
			}
		}
		if j.TimeoutSeconds == 0 {
			j.TimeoutSeconds = 300
		}
		if j.TimeoutSeconds < 10 || j.TimeoutSeconds > 900 || j.Retries < 0 || j.Retries > 3 {
			return fmt.Errorf("job: timeout_seconds must be 10–900 and retries 0–3")
		}
		if s.Replicas != 1 || s.Autoscaling != nil || s.Port != 0 || len(s.Ports) > 0 || s.Public || len(s.PublicTCP) > 0 || s.TLS != nil || s.Readiness != nil || s.Healthcheck != "" || len(s.CertificateMounts) > 0 || s.UpdateStrategy != "" {
			return fmt.Errorf("job: require one replica without listeners, readiness checks, certificates, autoscaling or update strategy")
		}
	}
	if len(s.Files)+len(s.Mounts)+len(s.TemporaryMounts)+len(s.CertificateMounts) > 16 {
		return fmt.Errorf("at most 16 file, named, temporary and certificate mounts")
	}
	paths := []string{}
	if s.Volume != nil {
		paths = append(paths, s.Volume.MountPath)
	}
	for _, m := range s.Mounts {
		paths = append(paths, m.MountPath)
	}
	for _, m := range s.TemporaryMounts {
		paths = append(paths, m.MountPath)
	}
	for _, m := range s.CertificateMounts {
		paths = append(paths, m.MountPath)
	}
	total := 0
	for name, f := range s.Files {
		if !namePattern.MatchString(name) || !cleanDirectory(f.MountPath) || (f.Content == nil) == (f.Secret == nil) {
			return fmt.Errorf("files: use a valid name, absolute file path and exactly one of content or secret")
		}
		for _, reserved := range []string{"/proc", "/sys", "/dev", "/var/run/secrets", "/run/secrets", "/etc"} {
			if f.MountPath == reserved || reserved != "/etc" && strings.HasPrefix(f.MountPath, reserved+"/") || strings.HasPrefix(reserved, f.MountPath+"/") {
				return fmt.Errorf("files.%s: cannot replace a reserved system path", name)
			}
		}
		if f.Secret != nil && !f.Secret.Valid() {
			return fmt.Errorf("files.%s.secret: invalid scoped secret reference", name)
		}
		if _, exists := s.Secrets[FileSecretKey(name)]; exists {
			return fmt.Errorf("files.%s: reserved secret key collision", name)
		}
		if f.Content != nil {
			total += len(*f.Content)
			if len(*f.Content) > 64<<10 || strings.ContainsRune(*f.Content, 0) {
				return fmt.Errorf("files.%s.content: at most 64 KiB without NUL", name)
			}
		}
		if f.Mode == 0 {
			f.Mode = 0444
		}
		if f.Mode != 0444 && f.Mode != 0440 {
			return fmt.Errorf("files.%s.mode: choose 292 (0444) or 288 (0440); files are read-only", name)
		}
		for _, other := range paths {
			if f.MountPath == other || strings.HasPrefix(f.MountPath, other+"/") || strings.HasPrefix(other, f.MountPath+"/") {
				return fmt.Errorf("file and volume mount paths must not overlap")
			}
		}
		paths = append(paths, f.MountPath)
		s.Files[name] = f
	}
	if total > 128<<10 {
		return fmt.Errorf("files: combined content exceeds 128 KiB")
	}
	return nil
}
