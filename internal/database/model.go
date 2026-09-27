// Package database describes managed databases independently of application replicas.
package database

import (
	"fmt"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

const MaxDatabases = 64
const MaxMembers = 48
const ObservationMaxAge = 30 * time.Second
const ReviewLifetime = 10 * time.Minute
const BackupMaxAge = time.Hour

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)

// Spec is a versioned desired configuration. Replicas belong to the database
// controller: they are never derived from an application's service count.
type Spec struct {
	SchemaVersion int    `json:"schema_version" toml:"schema_version"`
	Name          string `json:"name" toml:"name"`
	Engine        string `json:"engine" toml:"engine"`
	Version       string `json:"version" toml:"version"`
	Mode          string `json:"mode" toml:"mode"`
	Replicas      int    `json:"replicas" toml:"replicas"`
	Shards        int    `json:"shards" toml:"shards"`
	CPU           string `json:"cpu" toml:"cpu"`
	Memory        string `json:"memory" toml:"memory"`
	StorageGiB    int64  `json:"storage_gib" toml:"storage_gib"`
}

func (s Spec) Validate() error {
	if s.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("name must contain 1–40 lowercase letters, digits or hyphens and begin with a letter")
	}
	if s.Mode != "standalone" && s.Mode != "cluster" {
		return fmt.Errorf("mode must be standalone or cluster")
	}
	switch s.Engine {
	case "postgresql":
		if s.Version != "17" && s.Version != "18" {
			return fmt.Errorf("PostgreSQL version must be 17 or 18")
		}
		if s.Shards != 1 {
			return fmt.Errorf("PostgreSQL uses one primary, not shards")
		}
		if s.Mode == "cluster" && (s.Replicas < 1 || s.Replicas > 4) {
			return fmt.Errorf("PostgreSQL clusters require 1–4 replicas")
		}
	case "redis":
		if s.Version != "8" {
			return fmt.Errorf("Redis version must be 8")
		}
		if s.Mode == "cluster" && (s.Shards < 3 || s.Shards > 16 || s.Replicas < 1 || s.Replicas > 2) {
			return fmt.Errorf("Redis clusters require 3–16 shards and 1–2 replicas per shard")
		}
	default:
		return fmt.Errorf("engine must be postgresql or redis")
	}
	if s.Mode == "standalone" && (s.Replicas != 0 || s.Shards != 1) {
		return fmt.Errorf("standalone databases require one shard and no replicas")
	}
	if s.Members() > MaxMembers {
		return fmt.Errorf("a database may have at most %d members", MaxMembers)
	}
	cpu, err := resource.ParseQuantity(s.CPU)
	if err != nil || cpu.Cmp(resource.MustParse("100m")) < 0 || cpu.Cmp(resource.MustParse("16")) > 0 {
		return fmt.Errorf("cpu must be between 100m and 16 cores per member")
	}
	memory, err := resource.ParseQuantity(s.Memory)
	if err != nil || memory.Cmp(resource.MustParse("128Mi")) < 0 || memory.Cmp(resource.MustParse("64Gi")) > 0 {
		return fmt.Errorf("memory must be between 128Mi and 64Gi per member")
	}
	if s.StorageGiB < 1 || s.StorageGiB > 1024 {
		return fmt.Errorf("storage_gib must be between 1 and 1024 per member")
	}
	return nil
}
func (s Spec) Members() int { return s.Shards * (1 + s.Replicas) }

type Member struct {
	Name  string `json:"name"`
	UID   string `json:"uid"`
	Role  string `json:"role"`
	Shard string `json:"shard,omitempty"`
	Ready bool   `json:"ready"`
	Node  string `json:"node,omitempty"`
}
type Endpoint struct {
	Purpose string `json:"purpose"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}
type Observation struct {
	ObservedAt          time.Time  `json:"observed_at"`
	Revision            int64      `json:"revision"`
	Status              string     `json:"status"`
	Message             string     `json:"message"`
	Members             []Member   `json:"members"`
	Endpoints           []Endpoint `json:"endpoints"`
	Primary             string     `json:"primary,omitempty"`
	SlotsAssigned       int        `json:"slots_assigned,omitempty"`
	SlotsHealthy        bool       `json:"slots_healthy"`
	TopologyFingerprint string     `json:"topology_fingerprint,omitempty"`
}

func (o Observation) Fresh(now time.Time, revision int64) bool {
	return !o.ObservedAt.IsZero() && !o.ObservedAt.After(now) && now.Sub(o.ObservedAt) <= ObservationMaxAge && o.Revision == revision
}

type Resource struct {
	Recovery             *Recovery   `json:"recovery,omitempty"`
	ID                   string      `json:"id"`
	Project              string      `json:"project"`
	Environment          string      `json:"environment"`
	Revision             int64       `json:"revision"`
	Spec                 Spec        `json:"spec"`
	Status               string      `json:"status"`
	Observation          Observation `json:"observation"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
	DeletedAt            *time.Time  `json:"deleted_at,omitempty"`
	EncryptedCredentials []byte      `json:"-"`
}

type Operation struct {
	Review     *ResizePlan `json:"review,omitempty"`
	ID         string      `json:"id"`
	DatabaseID string      `json:"database_id"`
	Revision   int64       `json:"revision"`
	Kind       string      `json:"kind"`
	Status     string      `json:"status"`
	Phase      string      `json:"phase"`
	Message    string      `json:"message"`
	Spec       Spec        `json:"spec"`
	CreatedAt  time.Time   `json:"created_at"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	IdentityID string      `json:"-"`
	KeyID      string      `json:"-"`
	Lease      string      `json:"-"`
}

// BackupEvidence refers to bytes checked against their recorded digest. Upload
// completion alone must never be represented as verification.
type BackupEvidence struct {
	ArtifactID string    `json:"artifact_id"`
	DatabaseID string    `json:"database_id"`
	Revision   int64     `json:"revision"`
	CapturedAt time.Time `json:"captured_at"`
	VerifiedAt time.Time `json:"verified_at"`
	SHA256     string    `json:"sha256"`
}

type ResizePlan struct {
	Current             Spec            `json:"current"`
	Proposed            Spec            `json:"proposed"`
	ExpectedRevision    int64           `json:"expected_revision"`
	TopologyFingerprint string          `json:"topology_fingerprint"`
	Backup              *BackupEvidence `json:"backup,omitempty"`
	BlockedReasons      []string        `json:"blocked_reasons"`
	Warnings            []string        `json:"warnings"`
	ExpiresAt           time.Time       `json:"expires_at"`
}

func PlanResize(db Resource, next Spec, evidence *BackupEvidence, now time.Time) (ResizePlan, error) {
	p := ResizePlan{Current: db.Spec, Proposed: next, ExpectedRevision: db.Revision, TopologyFingerprint: db.Observation.TopologyFingerprint, Backup: evidence, BlockedReasons: []string{}, Warnings: []string{}, ExpiresAt: now.Add(ReviewLifetime)}
	if err := next.Validate(); err != nil {
		return p, err
	}
	if db.Spec.Name != next.Name || db.Spec.Engine != next.Engine || db.Spec.Version != next.Version || db.Spec.Mode != next.Mode {
		return p, fmt.Errorf("name, engine, version and mode are immutable; create a separate database for migration or upgrade")
	}
	if next.StorageGiB < db.Spec.StorageGiB {
		return p, fmt.Errorf("database volumes cannot shrink; restore into a separate database")
	}
	if db.Status != "ready" || db.Observation.Status != "ready" || !db.Observation.Fresh(now, db.Revision) {
		p.BlockedReasons = append(p.BlockedReasons, "A recent healthy observation is required.")
	}
	if db.Spec.Engine == "redis" && db.Spec.Shards != next.Shards {
		if !db.Observation.SlotsHealthy || db.Observation.SlotsAssigned != 16384 || db.Observation.TopologyFingerprint == "" {
			p.BlockedReasons = append(p.BlockedReasons, "Every Redis slot must have exactly one healthy owner, with no migration in progress.")
		}
		if evidence == nil || evidence.DatabaseID != db.ID || evidence.Revision != db.Revision || evidence.CapturedAt.IsZero() || evidence.CapturedAt.After(now) || now.Sub(evidence.CapturedAt) > BackupMaxAge || evidence.VerifiedAt.Before(evidence.CapturedAt) || evidence.VerifiedAt.After(now) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(evidence.SHA256) {
			p.BlockedReasons = append(p.BlockedReasons, "A verified backup of this database revision captured within the last hour is required.")
		}
		p.Warnings = append(p.Warnings, "Redis Cluster needs a cluster-aware client. Resharding moves live data and can temporarily increase latency.", "Redis backups preserve values and expiry consistently per shard, not in one transaction across shards.")
	}
	return p, nil
}
