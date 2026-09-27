package backup

import "fmt"

// Only supported, authenticated logical archive formats may reach a fresh
// managed target. A version change is an explicit staged copy, never in-place.
func ValidateManagedRecovery(a Artifact, engine, version string) error {
	if a.Source.Engine != engine || a.VerifiedAt == nil || a.CapturedAt == nil || a.DeletionPending {
		return fmt.Errorf("%w: choose a verified matching database archive with a captured recovery point", ErrInput)
	}
	switch engine {
	case "postgresql":
		if a.Format != "age-v1+postgresql-custom" || (a.SourceVersion != "17" && a.SourceVersion != "18") || (version != a.SourceVersion && !(a.SourceVersion == "17" && version == "18")) {
			return fmt.Errorf("%w: PostgreSQL recovery supports the same major version or a staged 17-to-18 logical copy", ErrInput)
		}
	case "redis":
		if a.Format != "age-v1+redis-shards-v1" || a.SourceVersion != "8" || version != "8" {
			return fmt.Errorf("%w: Redis recovery requires a verified Redis 8 shard archive", ErrInput)
		}
	default:
		return fmt.Errorf("%w: unsupported managed recovery engine", ErrInput)
	}
	return nil
}
