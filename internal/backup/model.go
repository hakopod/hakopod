// Package backup implements bounded, encrypted logical database backups.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrInput = errors.New("invalid backup input")
var ErrConflict = errors.New("backup revision or state conflict")
var ErrNotFound = errors.New("backup resource not found")

// Authority preserves the accepted scope across worker and scheduler restarts.
// It never grants authority: workers must reauthorize it against current membership.
type Authority struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

type Source struct {
	ExternalName      string `json:"external_name,omitempty"`
	ManagedDatabaseID string `json:"managed_database_id,omitempty"`
	Kind              string `json:"kind"`
	ApplicationID     string `json:"application_id,omitempty"`
	Service           string `json:"service,omitempty"`
	Engine            string `json:"engine"`
	Database          string `json:"database,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)
var identifierID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s Source) Validate() error {
	if s.Kind == "docker_import" {
		if !identifier.MatchString(s.ExternalName) || s.ManagedDatabaseID != "" || s.ApplicationID != "" || s.Service != "" || s.Database != "" || (s.Engine != "postgresql" && s.Engine != "redis") {
			return fmt.Errorf("%w: invalid Docker archive source", ErrInput)
		}
		return nil
	}
	if s.ExternalName != "" {
		return fmt.Errorf("%w: external_name only belongs to imported archives", ErrInput)
	}
	if s.Kind == "managed_database" {
		if !identifierID.MatchString(s.ManagedDatabaseID) || s.ApplicationID != "" || s.Service != "" || s.Database != "" || (s.Engine != "postgresql" && s.Engine != "redis" && s.Engine != "mysql" && s.Engine != "mongodb" && s.Engine != "clickhouse" && s.Engine != "oracle" && s.Engine != "vitess" && s.Engine != "duckdb") {
			return fmt.Errorf("%w: choose a supported managed database", ErrInput)
		}
		return nil
	}
	if s.ManagedDatabaseID != "" {
		return fmt.Errorf("%w: managed database identity does not match source kind", ErrInput)
	}
	if s.Kind == "management" {
		if s.Engine != "postgresql" || s.ApplicationID != "" || s.Service != "" || s.Database != "" {
			return fmt.Errorf("%w: management source only accepts engine postgresql", ErrInput)
		}
		return nil
	}
	if s.Kind != "database" || !identifierID.MatchString(s.ApplicationID) || !identifier.MatchString(s.Service) || !identifier.MatchString(s.Database) || (s.Engine != "postgresql" && s.Engine != "mysql" && s.Engine != "clickhouse") {
		return fmt.Errorf("%w: select a PostgreSQL, MySQL or ClickHouse application service and database", ErrInput)
	}
	return nil
}

type Destination struct {
	Project              string    `json:"project,omitempty"`
	Environment          string    `json:"environment,omitempty"`
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Endpoint             string    `json:"endpoint"`
	Region               string    `json:"region"`
	Bucket               string    `json:"bucket"`
	Prefix               string    `json:"prefix"`
	PathStyle            bool      `json:"path_style"`
	AllowHTTP            bool      `json:"allow_http"`
	Revision             int64     `json:"revision"`
	CredentialRef        string    `json:"credential_ref"`
	EncryptionRecipient  string    `json:"encryption_recipient"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	EncryptedCredentials []byte    `json:"-"`
}

type Credentials struct {
	AccessKeyID        string `json:"access_key_id"`
	SecretAccessKey    string `json:"secret_access_key"`
	SessionToken       string `json:"session_token,omitempty"`
	EncryptionIdentity string `json:"encryption_identity"`
}

type DestinationInput struct {
	Name               string `json:"name"`
	Endpoint           string `json:"endpoint"`
	Region             string `json:"region"`
	Bucket             string `json:"bucket"`
	Prefix             string `json:"prefix"`
	PathStyle          bool   `json:"path_style"`
	AllowHTTP          bool   `json:"allow_http"`
	ExpectedRevision   int64  `json:"expected_revision"`
	AccessKeyID        string `json:"access_key_id,omitempty"`
	SecretAccessKey    string `json:"secret_access_key,omitempty"`
	SessionToken       string `json:"session_token,omitempty"`
	EncryptionIdentity string `json:"encryption_identity,omitempty"`
}

func (d DestinationInput) Validate() error {
	if len(strings.TrimSpace(d.Name)) < 1 || len(d.Name) > 80 || len(d.Region) < 1 || len(d.Region) > 63 || strings.ContainsAny(d.Region, " /\\\r\n") {
		return fmt.Errorf("%w: destination name and region are required", ErrInput)
	}
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && d.AllowHTTP)) {
		return fmt.Errorf("%w: use an HTTPS S3 endpoint; HTTP requires allow_http for trusted local storage", ErrInput)
	}
	if len(d.Endpoint) > 512 || len(d.Bucket) < 3 || len(d.Bucket) > 63 || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`).MatchString(d.Bucket) || strings.Contains(d.Bucket, "..") {
		return fmt.Errorf("%w: invalid S3 bucket", ErrInput)
	}
	if len(d.Prefix) > 160 || strings.HasPrefix(d.Prefix, "/") || strings.Contains(d.Prefix, "..") || !regexp.MustCompile(`^[A-Za-z0-9/_-]*$`).MatchString(d.Prefix) {
		return fmt.Errorf("%w: prefix must be a relative alphanumeric path", ErrInput)
	}
	if len(d.AccessKeyID) > 256 || len(d.SecretAccessKey) > 512 || len(d.SessionToken) > 8192 || len(d.EncryptionIdentity) > 256 {
		return fmt.Errorf("%w: credentials exceed bounds", ErrInput)
	}
	return nil
}

type Target struct {
	SourceVersion       string `json:"source_version,omitempty"`
	ManagedDatabaseName string `json:"managed_database_name,omitempty"`
	Source
	ApplicationName    string   `json:"application_name,omitempty"`
	Revision           int64    `json:"revision"`
	Pod                string   `json:"pod,omitempty"`
	PodUID             string   `json:"pod_uid,omitempty"`
	RuntimeFingerprint string   `json:"runtime_fingerprint,omitempty"`
	Dependencies       []string `json:"dependencies,omitempty"`
	Available          bool     `json:"available"`
	Message            string   `json:"message,omitempty"`
}

type Artifact struct {
	SourceVersion         string                `json:"source_version,omitempty"`
	SourceRevision        int64                 `json:"source_revision,omitempty"`
	CapturedAt            *time.Time            `json:"captured_at,omitempty"`
	VerifiedAt            *time.Time            `json:"verified_at,omitempty"`
	ID                    string                `json:"id"`
	JobID                 string                `json:"job_id"`
	DestinationID         string                `json:"destination_id"`
	Source                Source                `json:"source"`
	ObjectKey             string                `json:"object_key"`
	SHA256                string                `json:"sha256"`
	Bytes                 int64                 `json:"bytes"`
	Format                string                `json:"format"`
	Scope                 string                `json:"scope"`
	ScheduleID            string                `json:"schedule_id,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	DeletedAt             *time.Time            `json:"deleted_at,omitempty"`
	DeletionPending       bool                  `json:"deletion_pending"`
	CompatibilityEvidence CompatibilityEvidence `json:"compatibility_evidence"`
}

type CompatibilityEvidence struct {
	ApplicationRevision   int64                `json:"application_revision,omitempty"`
	RuntimeFingerprint    string               `json:"runtime_fingerprint,omitempty"`
	EncryptionRecipient   string               `json:"encryption_recipient,omitempty"`
	Dependencies          []string             `json:"dependencies,omitempty"`
	RelatedRecoveryPoints map[string]time.Time `json:"related_recovery_points,omitempty"`
}

type Job struct {
	Authority       *Authority `json:"-"`
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	DestinationID   string     `json:"destination_id"`
	Source          Source     `json:"source"`
	Target          *Target    `json:"target,omitempty"`
	ArtifactID      string     `json:"artifact_id,omitempty"`
	ScheduleID      string     `json:"schedule_id,omitempty"`
	IdentityID      string     `json:"-"`
	KeyID           string     `json:"-"`
	Error           string     `json:"error"`
	Bytes           int64      `json:"bytes"`
	CancelRequested bool       `json:"cancel_requested"`
	// EngineRef is the engine's own name for work it is performing itself. A job
	// carrying one is polled rather than streamed, and stays claimable while it
	// waits. Never exposed: it is an internal handle, not operator-facing state.
	EngineRef  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Lease      string     `json:"-"`
}

type workerJobContextKey struct{}

type WorkerJob struct{ ID, Lease, Kind string }

func WithWorkerJob(ctx context.Context, job Job) context.Context {
	return context.WithValue(ctx, workerJobContextKey{}, WorkerJob{ID: job.ID, Lease: job.Lease, Kind: job.Kind})
}

func WorkerJobFromContext(ctx context.Context) (WorkerJob, bool) {
	job, ok := ctx.Value(workerJobContextKey{}).(WorkerJob)
	return job, ok && job.ID != "" && job.Lease != "" && (job.Kind == "backup" || job.Kind == "restore")
}

type Schedule struct {
	Authority      *Authority `json:"-"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	DestinationID  string     `json:"destination_id"`
	Source         Source     `json:"source"`
	IntervalHours  int        `json:"interval_hours"`
	RetentionCount int        `json:"retention_count"`
	Enabled        bool       `json:"enabled"`
	Revision       int64      `json:"revision"`
	NextRunAt      time.Time  `json:"next_run_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	IdentityID     string     `json:"-"`
}

func (s Schedule) Validate() error {
	if len(strings.TrimSpace(s.Name)) < 1 || len(s.Name) > 80 || !identifierID.MatchString(s.DestinationID) || s.IntervalHours < 1 || s.IntervalHours > 8760 || s.RetentionCount < 1 || s.RetentionCount > 100 {
		return fmt.Errorf("%w: schedule needs a destination, name, 1–8760 hour interval and 1–100 retained backups", ErrInput)
	}
	return s.Source.Validate()
}

type RestorePlan struct {
	ID            string              `json:"id"`
	ArtifactID    string              `json:"artifact_id"`
	Target        Target              `json:"target"`
	Confirmation  string              `json:"confirmation"`
	Scope         string              `json:"scope"`
	Warnings      []string            `json:"warnings"`
	Compatibility CompatibilityReport `json:"compatibility"`
	ExpiresAt     time.Time           `json:"expires_at"`
}

type CompatibilityCheck struct {
	Code     string `json:"code"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Evidence string `json:"evidence,omitempty"`
}
type CompatibilityReport struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Checks      []CompatibilityCheck `json:"checks"`
	Blocked     bool                 `json:"blocked"`
}

func RestoreCompatibility(a Artifact, target Target, encrypted bool, now time.Time) CompatibilityReport {
	checks := []CompatibilityCheck{}
	add := func(code, status, message, evidence string) {
		checks = append(checks, CompatibilityCheck{Code: code, Status: status, Message: message, Evidence: evidence})
	}
	if a.CapturedAt == nil {
		add("backup_age", "unknown", "The backup has no captured recovery point.", "")
	} else if a.CapturedAt.After(now) {
		add("backup_age", "blocker", "The backup recovery point is in the future.", a.CapturedAt.UTC().Format(time.RFC3339))
	} else {
		add("backup_age", "checked", "The backup recovery point is recorded.", a.CapturedAt.UTC().Format(time.RFC3339))
	}
	if a.Source.Engine == target.Engine && a.SourceVersion != "" {
		add("database_version", "checked", "The database engine and recorded source version are available for engine validation.", a.Source.Engine+" "+a.SourceVersion)
	} else if a.Source.Engine == target.Engine {
		add("database_version", "unknown", "The database engine matches, but this archive has no recorded source version.", a.Source.Engine)
	} else {
		add("database_version", "blocker", "The database engine or source version does not match the selected target.", "")
	}
	if encrypted {
		add("encryption_identity", "unknown", "The destination encryption recipient reference matches. The private recovery identity is checked before restore changes the target.", "")
	} else {
		add("encryption_key", "blocker", "The required destination encryption key reference is unavailable.", "")
	}
	if a.Source.Kind == "database" && a.CompatibilityEvidence.ApplicationRevision > 0 && a.CompatibilityEvidence.RuntimeFingerprint != "" {
		add("application_version", "unknown", "The source application revision and runtime fingerprint were captured, but no target application version was selected for comparison.", fmt.Sprintf("revision %d, %s", a.CompatibilityEvidence.ApplicationRevision, a.CompatibilityEvidence.RuntimeFingerprint))
		if len(a.CompatibilityEvidence.Dependencies) > 0 {
			add("dependencies", "checked", "Declared dependency recovery points were captured.", fmt.Sprintf("%d dependencies", len(a.CompatibilityEvidence.Dependencies)))
		} else {
			add("dependencies", "unknown", "No related dependency recovery points were captured.", "")
		}
	} else if a.Source.Kind == "database" {
		add("application_version", "unknown", "The archive format predates an immutable application runtime inventory.", "")
		add("dependencies", "unknown", "The archive does not contain a complete dependency recovery set.", "")
	} else {
		add("application_version", "unknown", "This database archive has no associated application version.", "")
		add("dependencies", "unknown", "No related application recovery points were declared.", "")
	}
	points := a.CompatibilityEvidence.RelatedRecoveryPoints
	if len(points) < 2 {
		add("related_recovery_points", "unknown", "No multi-service recovery set was captured.", "")
	} else {
		var earliest, latest time.Time
		for _, point := range points {
			if earliest.IsZero() || point.Before(earliest) {
				earliest = point
			}
			if point.After(latest) {
				latest = point
			}
		}
		if latest.Sub(earliest) > 5*time.Minute {
			add("related_recovery_points", "blocker", "Related recovery points are inconsistent by more than five minutes.", fmt.Sprintf("%s to %s", earliest.UTC().Format(time.RFC3339), latest.UTC().Format(time.RFC3339)))
		} else {
			add("related_recovery_points", "checked", "Related recovery points are within the five-minute consistency window.", fmt.Sprintf("%d recovery points", len(points)))
		}
	}
	blocked := false
	for _, check := range checks {
		if check.Status == "blocker" {
			blocked = true
		}
	}
	return CompatibilityReport{GeneratedAt: now.UTC(), Checks: checks, Blocked: blocked}
}

// Runtime resolves owned services and executes official database tools. A
// restore implementation must refuse an existing target database.
type Runtime interface {
	Targets(context.Context) ([]Target, error)
	Resolve(context.Context, Source) (Target, error)
	Dump(context.Context, Target, io.Writer) error
	Restore(context.Context, Target, io.Reader) error
}

// EngineStatus reports the progress of a backup the database engine runs by
// itself. Bytes and Files are the engine's own counters, not measurements
// hakopod made.
type EngineStatus struct {
	Done   bool
	Failed bool
	Bytes  int64
	Files  int64
	// Message is sanitized prose written for an operator. It must never carry
	// raw engine output: it is stored on backup_jobs.error, served by the API
	// and rendered in the dashboard, and a database exception routinely echoes
	// the statement that caused it, which here contains the object-storage
	// credentials the engine was given.
	Message string
}

// EngineBackup covers engines that write their own backup to object storage, so
// the bytes never pass through this process. The engine writes a tree under
// prefix; ref identifies the running operation for polling. Both start calls
// hand the engine object-store credentials, so nothing derived from them may
// reach a status message or a log.
type EngineBackup interface {
	StartBackup(ctx context.Context, t Target, d Destination, c Credentials, prefix string) (ref string, err error)
	PollBackup(ctx context.Context, t Target, ref string) (EngineStatus, error)
	// sourceDatabase is the database recorded in the artifact being restored, not
	// whatever the target service declares today. Re-deriving it would restore the
	// wrong database when an operator recovers into a service declaring another.
	StartRestore(ctx context.Context, t Target, d Destination, c Credentials, prefix string, sourceDatabase string) (ref string, err error)
	PollRestore(ctx context.Context, t Target, ref string) (EngineStatus, error)
}

type Repository interface {
	BackupDestination(context.Context, string) (Destination, error)
	ClaimBackupJob(context.Context, string) (Job, error)
	HeartbeatBackupJob(context.Context, string, string) (bool, error)
	SetBackupJobEngineRef(ctx context.Context, id, lease, ref string) (bool, error)
	ReleaseBackupJobToEngine(ctx context.Context, id, lease string) (bool, error)
	BackupJobEngineRef(ctx context.Context, id string) (string, error)
	FinishBackupJob(context.Context, Job, string, string, *Artifact) error
	BackupArtifact(context.Context, string) (Artifact, error)
	QueueDueBackups(context.Context) error
	ExpiredBackupArtifacts(context.Context, int) ([]Artifact, error)
	MarkBackupArtifactDeleted(context.Context, string) error
	ClaimBackupArtifactDeletion(context.Context, string) (bool, error)
}

func Scope(source Source) string {
	if source.Engine == "duckdb" {
		return "Encrypted cold backup of one standalone DuckDB (MyDuck) data directory. Capture briefly stops the database, so connections are interrupted, and resumes it after the archive is complete or fails. Restore verifies the complete encrypted archive before replacing files in a separate empty target of the same version. A failed restore remains stopped and isolated until cleanup and recovery are reviewed. Excludes users, server configuration and point-in-time recovery."
	}
	if source.Engine == "oracle" {
		return "Oracle Free Data Pump capture of the APP schema in FREEPDB1 at one SCN. Includes supported schema objects and data; excludes database users, grants, CDB configuration, wallets, archived redo, RMAN and point-in-time recovery. Concurrent DDL can invalidate capture. Restore runs with the APP schema account in a separate empty Free target of the same version after complete archive verification and session revocation. Application ingress stays closed until recovery is inspected."
	}
	if source.Engine == "mongodb" {
		return "MongoDB BSON snapshot of app at one read timestamp. Preserves supported collection options, indexes, views, time-series data and BSON types; schema or topology changes invalidate capture. Excludes other databases, users, replica configuration, oplog and point-in-time recovery. Restore replaces members of a separate empty target to close existing sessions, then uses a loopback-only recovery account scoped to app while client access remains isolated."
	}
	if source.Engine == "redis" {
		return "Redis RDB snapshots preserve values and absolute expiry. Each primary shard is captured consistently; different shards have different recovery points. Excludes server configuration, cluster membership, users and access-control lists. Redis Cluster requires a cluster-aware client."
	}
	if source.Kind == "management" {
		return "Logical PostgreSQL dump of the Hakopod management database, including accounts, encrypted credential records, specifications, jobs and audit history. Excludes the authentication encryption key, installer configuration, Kubernetes state and secrets, application databases, persistent volumes, images and external object data. Recovery requires separately preserved encryption key/configuration and deliberate offline reconnection; restoring this dump does not activate another control plane."
	}
	if source.Engine == "clickhouse" {
		if source.Kind == "managed_database" {
			return "Encrypted native ClickHouse backups of app from one replica per shard. Tables and shards can have different capture times; this is not a transactionally consistent cross-table snapshot or point-in-time recovery. Excludes users, grants, server configuration and Keeper state. Restore requires a separate empty database with the same shard layout, isolated Keeper identity and enough staging space. Client access remains closed until recovery is inspected."
		}
		return "ClickHouse backup of one database, written to object storage by the ClickHouse server itself using its BACKUP command. It covers the tables ClickHouse includes in that database backup and their data as the server saw them at that moment. It excludes server users, grants, settings and other databases. Hakopod does not read, decrypt or verify these files: it records what the server reported and where the files were written, so completeness and consistency are the server's guarantees, not hakopod's. Restoring replays the files back through the server into the target database."
	}
	if source.Engine == "mysql" {
		if source.Kind == "managed_database" {
			return "Logical MySQL dump of app, including tables, views, triggers, routines and events. A global read lock blocks writes and DDL during capture to preserve a consistent snapshot. Excludes server users, grants, global settings, binlogs and point-in-time recovery. Restore uses the application account in a separate empty database."
		}
		return "Logical MySQL dump of one database using single-transaction/quick, including tables, triggers, routines and events. Transactional InnoDB data is consistent; concurrent DDL and non-transactional tables require an operator maintenance window. Excludes server users, grants, global settings, binlogs and point-in-time recovery."
	}
	return "PostgreSQL custom-format logical dump of one database. Includes schema and data; excludes global roles, tablespaces, server configuration, WAL and point-in-time recovery. Restores without original ownership or ACLs into a new database owned by the target connection user."
}

func RestoreConfirmation(t Target) string {
	if t.Kind == "managed_database" {
		return t.ManagedDatabaseName
	}
	return t.Database
}
