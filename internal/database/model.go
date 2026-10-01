// Package database describes managed databases independently of application replicas.
package database

import (
	"fmt"
	"reflect"
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
	SchemaVersion int           `json:"schema_version" toml:"schema_version"`
	Name          string        `json:"name" toml:"name"`
	Engine        string        `json:"engine" toml:"engine"`
	Version       string        `json:"version" toml:"version"`
	Mode          string        `json:"mode" toml:"mode"`
	Replicas      int           `json:"replicas" toml:"replicas"`
	Shards        int           `json:"shards" toml:"shards"`
	CPU           string        `json:"cpu" toml:"cpu"`
	Memory        string        `json:"memory" toml:"memory"`
	StorageGiB    int64         `json:"storage_gib" toml:"storage_gib"`
	Placement     Placement     `json:"placement,omitempty" toml:"placement"`
	TLS           *TLSConfig    `json:"tls,omitempty" toml:"tls"`
	Pooling       *Pooling      `json:"pooling,omitempty" toml:"pooling"`
	Oracle        *OracleConfig `json:"oracle,omitempty" toml:"oracle"`
	Vitess        *VitessConfig `json:"vitess,omitempty" toml:"vitess"`
}

// Placement restricts scheduling within one Kubernetes cluster. Spread is a
// hard separation rule, not a claim about replication or outage tolerance.
type Placement struct {
	Spread    string   `json:"spread,omitempty" toml:"spread"`
	NodeNames []string `json:"node_names,omitempty" toml:"node_names"`
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
	if s.TLS != nil && s.TLS.Mode != "required" {
		return fmt.Errorf("tls.mode must be required")
	}
	if s.Engine != "oracle" && s.Oracle != nil {
		return fmt.Errorf("oracle configuration belongs only to Oracle databases")
	}
	if s.Engine != "vitess" && s.Vitess != nil {
		return fmt.Errorf("vitess configuration belongs only to Vitess databases")
	}
	if err := s.Placement.Validate(s.Mode, s.PlacementDomains()); err != nil {
		return err
	}
	switch s.Engine {
	case "postgresql":
		if s.Version != "17" && s.Version != "18" {
			return fmt.Errorf("PostgreSQL version must be 17 or 18")
		}
		if s.Shards != 1 {
			return fmt.Errorf("PostgreSQL uses one primary, not shards")
		}
		if s.Mode == "cluster" && (s.Replicas < 1 || s.Replicas > 6) {
			return fmt.Errorf("PostgreSQL clusters require 1–6 replicas")
		}
	case "redis":
		if s.Version != "8" {
			return fmt.Errorf("Redis version must be 8")
		}
		if s.Mode == "cluster" && (s.Shards < 3 || s.Shards > 16 || s.Replicas < 1 || s.Replicas > 2) {
			return fmt.Errorf("Redis clusters require 3–16 shards and 1–2 replicas per shard")
		}
	case "mysql":
		if s.Version != "8.4" || s.Shards != 1 || !s.TLSRequired() {
			return fmt.Errorf("MySQL requires version 8.4, one shard and required TLS")
		}
		if s.Mode == "cluster" && s.Replicas != 2 && s.Replicas != 4 && s.Replicas != 6 {
			return fmt.Errorf("MySQL clusters require 3, 5 or 7 voting members (2, 4 or 6 replicas)")
		}
	case "mongodb":
		if s.Version != "8.0" || s.Shards != 1 || !s.TLSRequired() {
			return fmt.Errorf("MongoDB requires version 8.0, one replica set and required TLS")
		}
		if s.Mode == "cluster" && s.Replicas != 2 && s.Replicas != 4 && s.Replicas != 6 {
			return fmt.Errorf("MongoDB clusters require 3, 5 or 7 voting members (2, 4 or 6 replicas)")
		}
	case "clickhouse":
		if s.Version != "26.3" || !s.TLSRequired() {
			return fmt.Errorf("ClickHouse requires version 26.3 and required TLS")
		}
		if s.Mode == "cluster" && (s.Shards < 1 || s.Shards > 8 || s.Replicas < 1 || s.Replicas > 5) {
			return fmt.Errorf("ClickHouse clusters require 1–8 shards and 1–5 replicas per shard, plus three Keeper members")
		}
	case "vitess":
		if err := s.ValidateVitess(); err != nil {
			return err
		}
	case "oracle":
		if s.Oracle == nil || s.Shards != 1 || !s.TLSRequired() {
			return fmt.Errorf("Oracle requires an explicit edition, one database and required TLS")
		}
		if err := s.Oracle.Validate(s.Version, s.Mode); err != nil {
			return err
		}
		if s.Mode == "cluster" && (s.Replicas < 1 || s.Replicas > 5) {
			return fmt.Errorf("Oracle Data Guard requires 1–5 physical standbys")
		}
	default:
		return fmt.Errorf("engine must be postgresql, redis, mysql, mongodb, clickhouse, vitess or oracle")
	}
	if s.Mode == "standalone" && (s.Replicas != 0 || s.Shards != 1) {
		return fmt.Errorf("standalone databases require one shard and no replicas")
	}
	if s.Members() > MaxMembers {
		return fmt.Errorf("a database may have at most %d members", MaxMembers)
	}
	if err := s.ValidatePooling(); err != nil {
		return err
	}
	cpu, err := resource.ParseQuantity(s.CPU)
	if err != nil || cpu.Cmp(resource.MustParse("100m")) < 0 || cpu.Cmp(resource.MustParse("16")) > 0 {
		return fmt.Errorf("cpu must be between 100m and 16 cores per member")
	}
	memory, err := resource.ParseQuantity(s.Memory)
	if err != nil || memory.Cmp(resource.MustParse("128Mi")) < 0 || memory.Cmp(resource.MustParse("64Gi")) > 0 {
		return fmt.Errorf("memory must be between 128Mi and 64Gi per member")
	}
	if s.Engine == "mysql" && (cpu.Cmp(resource.MustParse("500m")) < 0 || memory.Cmp(resource.MustParse("1Gi")) < 0) {
		return fmt.Errorf("MySQL requires at least 500m CPU and 1Gi memory per database member, plus controller and router resources")
	}
	if s.Engine == "mongodb" && (cpu.Cmp(resource.MustParse("500m")) < 0 || memory.Cmp(resource.MustParse("1Gi")) < 0) {
		return fmt.Errorf("MongoDB requires at least 500m CPU and 1Gi memory per member, plus agent resources")
	}
	if s.Engine == "clickhouse" && (cpu.Cmp(resource.MustParse("500m")) < 0 || memory.Cmp(resource.MustParse("2Gi")) < 0) {
		return fmt.Errorf("ClickHouse requires at least 500m CPU and 2Gi memory per data member, plus Keeper resources in cluster mode")
	}
	if s.Engine == "oracle" && (cpu.Cmp(resource.MustParse("1")) < 0 || memory.Cmp(resource.MustParse("4Gi")) < 0 || s.StorageGiB < 10) {
		return fmt.Errorf("Oracle requires at least 1 CPU, 4Gi memory and 10Gi storage per member, plus backup storage")
	}
	if s.StorageGiB < 1 || s.StorageGiB > 1024 {
		return fmt.Errorf("storage_gib must be between 1 and 1024 per member")
	}
	return nil
}
func (s Spec) Members() int { return s.Shards * (1 + s.Replicas) }

func (s Spec) Equal(other Spec) bool {
	s.Placement, other.Placement = s.Placement.canonical(), other.Placement.canonical()
	return reflect.DeepEqual(s, other)
}

// Metrics contains only complete current resource samples. Missing values are
// unavailable, never zero. Collection does not affect database readiness.
type Metrics struct {
	Available     bool       `json:"available"`
	Reason        string     `json:"reason,omitempty"`
	CPU           *float64   `json:"cpu_millicores,omitempty"`
	Memory        *int64     `json:"memory_bytes,omitempty"`
	SampledAt     *time.Time `json:"sampled_at,omitempty"`
	WindowSeconds float64    `json:"window_seconds,omitempty"`
	PodsSampled   int        `json:"pods_sampled"`
	PodsExpected  int        `json:"pods_expected"`
}

type Member struct {
	Phase     string     `json:"phase,omitempty"`
	Restarts  int32      `json:"restarts"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Image     string     `json:"image,omitempty"`
	Metrics   *Metrics   `json:"metrics,omitempty"`
	Name      string     `json:"name"`
	UID       string     `json:"uid"`
	Role      string     `json:"role"`
	Shard     string     `json:"shard,omitempty"`
	Ready     bool       `json:"ready"`
	Node      string     `json:"node,omitempty"`
	Zone      string     `json:"zone,omitempty"`
	Region    string     `json:"region,omitempty"`
	Provider  string     `json:"provider,omitempty"`
}
type Endpoint struct {
	Purpose string `json:"purpose"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}
type Observation struct {
	Coordination        *CoordinationObservation `json:"coordination,omitempty"`
	Routing             *RoutingObservation      `json:"routing,omitempty"`
	Pooling             *PoolingObservation      `json:"pooling,omitempty"`
	EngineMetrics       *EngineMetrics           `json:"engine_metrics,omitempty"`
	TLS                 *TLSObservation          `json:"tls,omitempty"`
	Placement           *PlacementObservation    `json:"placement,omitempty"`
	Metrics             *Metrics                 `json:"metrics,omitempty"`
	ObservedAt          time.Time                `json:"observed_at"`
	Revision            int64                    `json:"revision"`
	Status              string                   `json:"status"`
	Message             string                   `json:"message"`
	Members             []Member                 `json:"members"`
	Endpoints           []Endpoint               `json:"endpoints"`
	Primary             string                   `json:"primary,omitempty"`
	SlotsAssigned       int                      `json:"slots_assigned,omitempty"`
	SlotsHealthy        bool                     `json:"slots_healthy"`
	TopologyFingerprint string                   `json:"topology_fingerprint,omitempty"`
}

type PlacementObservation struct {
	Verified  bool   `json:"verified"`
	Message   string `json:"message"`
	Nodes     int    `json:"nodes"`
	Zones     int    `json:"zones"`
	Regions   int    `json:"regions"`
	Providers int    `json:"providers"`
}

func (o Observation) Fresh(now time.Time, revision int64) bool {
	return !o.ObservedAt.IsZero() && !o.ObservedAt.After(now) && now.Sub(o.ObservedAt) <= ObservationMaxAge && o.Revision == revision
}

type Resource struct {
	Recovery    *Recovery   `json:"recovery,omitempty"`
	ID          string      `json:"id"`
	Project     string      `json:"project"`
	Environment string      `json:"environment"`
	Revision    int64       `json:"revision"`
	Spec        Spec        `json:"spec"`
	Status      string      `json:"status"`
	Observation Observation `json:"observation"`
	// PublicEndpointNames and PublicEndpointAccess are trusted reconciliation
	// inputs loaded from durable endpoint records, never API request fields.
	PublicEndpointNames   []string                         `json:"-"`
	PublicEndpointMembers []PublicEndpointMemberAllocation `json:"-"`
	PublicEndpointAccess  bool                             `json:"-"`
	CreatedAt             time.Time                        `json:"created_at"`
	UpdatedAt             time.Time                        `json:"updated_at"`
	DeletedAt             *time.Time                       `json:"deleted_at,omitempty"`
	EncryptedCredentials  []byte                           `json:"-"`
}

type Operation struct {
	Review     *ResizePlan             `json:"review,omitempty"`
	Switchover *OracleSwitchoverReview `json:"switchover,omitempty"`
	ID         string                  `json:"id"`
	DatabaseID string                  `json:"database_id"`
	Revision   int64                   `json:"revision"`
	Kind       string                  `json:"kind"`
	Status     string                  `json:"status"`
	Phase      string                  `json:"phase"`
	Message    string                  `json:"message"`
	Spec       Spec                    `json:"spec"`
	CreatedAt  time.Time               `json:"created_at"`
	StartedAt  *time.Time              `json:"started_at,omitempty"`
	FinishedAt *time.Time              `json:"finished_at,omitempty"`
	IdentityID string                  `json:"-"`
	KeyID      string                  `json:"-"`
	Lease      string                  `json:"-"`
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
	if !db.Spec.Placement.Equal(next.Placement) {
		return p, fmt.Errorf("placement is immutable; restore into a separate database to move persistent data")
	}
	if !reflect.DeepEqual(db.Spec.TLS, next.TLS) {
		return p, fmt.Errorf("TLS policy is immutable; restore into a separate secure database and review application connections before cutover")
	}
	if !reflect.DeepEqual(db.Spec.Pooling, next.Pooling) {
		return p, fmt.Errorf("pooling configuration is immutable; create a separate database to change its connection policy")
	}
	if !reflect.DeepEqual(db.Spec.Oracle, next.Oracle) {
		return p, fmt.Errorf("Oracle edition, image and entitlement are immutable; use a separate database for migration")
	}
	if db.Spec.Engine == "oracle" && !db.Spec.Equal(next) {
		return p, fmt.Errorf("Oracle capacity is fixed at creation; restore into a separate database to change resources")
	}
	if db.Spec.Engine == "mysql" && (db.Spec.CPU != next.CPU || db.Spec.Memory != next.Memory || db.Spec.StorageGiB != next.StorageGiB) {
		return p, fmt.Errorf("MySQL member resources are immutable with this controller; restore into a separate database to change capacity")
	}
	if db.Spec.Engine == "mongodb" && (db.Spec.CPU != next.CPU || db.Spec.Memory != next.Memory || db.Spec.StorageGiB != next.StorageGiB) {
		return p, fmt.Errorf("MongoDB member resources are fixed at creation; restore into a separate database to change capacity")
	}
	if db.Spec.Engine == "clickhouse" && !db.Spec.Equal(next) {
		return p, fmt.Errorf("ClickHouse capacity is fixed at creation; restore into a separate database to change its shard layout or resources")
	}
	if db.Spec.Engine == "vitess" && !db.Spec.Equal(next) {
		return p, fmt.Errorf("Vitess capacity and routing schema are fixed at creation; restore into a separate database before a reviewed migration")
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
