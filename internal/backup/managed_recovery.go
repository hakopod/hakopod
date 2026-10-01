package backup

import "fmt"

// Only supported, authenticated logical archive formats may reach a fresh
// managed target. A version change is an explicit staged copy, never in-place.
func ValidateManagedRecovery(a Artifact, engine, version string) error {
	if a.Source.Engine != engine || a.VerifiedAt == nil || a.CapturedAt == nil || a.DeletionPending {
		return fmt.Errorf("%w: choose a verified matching database archive with a captured recovery point", ErrInput)
	}
	switch engine {
	case "vitess":
		if a.Format != "age-v1+vitess-logical-v1" || a.SourceVersion != "23" || version != "23" {
			return fmt.Errorf("%w: Vitess recovery requires a verified version 23 logical shard archive", ErrInput)
		}
	case "oracle":
		if a.Format != "age-v1+oracle-datapump-v1" || a.SourceVersion != "23.26" || version != "23.26" {
			return fmt.Errorf("%w: Oracle Free recovery requires a verified 23.26 Data Pump archive", ErrInput)
		}
	case "clickhouse":
		if a.Format != "age-v1+clickhouse-shards-v1" || a.SourceVersion != "26.3" || version != "26.3" {
			return fmt.Errorf("%w: ClickHouse recovery requires a verified 26.3 shard archive", ErrInput)
		}
	case "mongodb":
		if a.Format != "age-v1+mongodb-bson-v1" || a.SourceVersion != "8.0" || version != "8.0" {
			return fmt.Errorf("%w: MongoDB recovery requires a verified MongoDB 8.0 BSON archive", ErrInput)
		}
	case "mysql":
		if a.Format != "age-v1+mysql-sql" || a.SourceVersion != "8.4" || version != "8.4" {
			return fmt.Errorf("%w: MySQL recovery requires a verified MySQL 8.4 logical archive", ErrInput)
		}
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
