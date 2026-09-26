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
	Kind          string `json:"kind"`
	ApplicationID string `json:"application_id,omitempty"`
	Service       string `json:"service,omitempty"`
	Engine        string `json:"engine"`
	Database      string `json:"database,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)
var identifierID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s Source) Validate() error {
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
	Source
	ApplicationName    string `json:"application_name,omitempty"`
	Revision           int64  `json:"revision"`
	Pod                string `json:"pod,omitempty"`
	PodUID             string `json:"pod_uid,omitempty"`
	RuntimeFingerprint string `json:"runtime_fingerprint,omitempty"`
	Available          bool   `json:"available"`
	Message            string `json:"message,omitempty"`
}

type Artifact struct {
	ID              string     `json:"id"`
	JobID           string     `json:"job_id"`
	DestinationID   string     `json:"destination_id"`
	Source          Source     `json:"source"`
	ObjectKey       string     `json:"object_key"`
	SHA256          string     `json:"sha256"`
	Bytes           int64      `json:"bytes"`
	Format          string     `json:"format"`
	Scope           string     `json:"scope"`
	ScheduleID      string     `json:"schedule_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	DeletionPending bool       `json:"deletion_pending"`
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
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Lease           string     `json:"-"`
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
	ID           string    `json:"id"`
	ArtifactID   string    `json:"artifact_id"`
	Target       Target    `json:"target"`
	Confirmation string    `json:"confirmation"`
	Scope        string    `json:"scope"`
	Warnings     []string  `json:"warnings"`
	ExpiresAt    time.Time `json:"expires_at"`
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
	StartRestore(ctx context.Context, t Target, d Destination, c Credentials, prefix string) (ref string, err error)
	PollRestore(ctx context.Context, t Target, ref string) (EngineStatus, error)
}

type Repository interface {
	BackupDestination(context.Context, string) (Destination, error)
	ClaimBackupJob(context.Context, string) (Job, error)
	HeartbeatBackupJob(context.Context, string, string) (bool, error)
	FinishBackupJob(context.Context, Job, string, string, *Artifact) error
	BackupArtifact(context.Context, string) (Artifact, error)
	QueueDueBackups(context.Context) error
	ExpiredBackupArtifacts(context.Context, int) ([]Artifact, error)
	MarkBackupArtifactDeleted(context.Context, string) error
	ClaimBackupArtifactDeletion(context.Context, string) (bool, error)
}

func Scope(source Source) string {
	if source.Kind == "management" {
		return "Logical PostgreSQL dump of the Hakopod management database, including accounts, encrypted credential records, specifications, jobs and audit history. Excludes the authentication encryption key, installer configuration, Kubernetes state and secrets, application databases, persistent volumes, images and external object data. Recovery requires separately preserved encryption key/configuration and deliberate offline reconnection; restoring this dump does not activate another control plane."
	}
	if source.Engine == "clickhouse" {
		return "ClickHouse backup of one database, written to object storage by the ClickHouse server itself using its BACKUP command. It covers the tables ClickHouse includes in that database backup and their data as the server saw them at that moment. It excludes server users, grants, settings and other databases. Hakopod does not read, decrypt or verify these files: it records what the server reported and where the files were written, so completeness and consistency are the server's guarantees, not hakopod's. Restoring replays the files back through the server into the target database."
	}
	if source.Engine == "mysql" {
		return "Logical MySQL dump of one database using single-transaction/quick, including tables, triggers, routines and events. Transactional InnoDB data is consistent; concurrent DDL and non-transactional tables require an operator maintenance window. Excludes server users, grants, global settings, binlogs and point-in-time recovery."
	}
	return "PostgreSQL custom-format logical dump of one database. Includes schema and data; excludes global roles, tablespaces, server configuration, WAL and point-in-time recovery. Restores without original ownership or ACLs into a new database owned by the target connection user."
}
