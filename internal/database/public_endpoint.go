package database

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

const MaxPublicEndpoints = 4
const MaxPublicEndpointNames = MaxPublicEndpoints * MaxMembers
const PublicEndpointReviewLifetime = 10 * time.Minute

// Change only after the exact release candidate passes native MySQL endpoint
// qualification. Configuration and customer requests cannot override this gate.
const mysqlPublicEndpointsQualified = false

// Native and HTTPS publication share one qualification boundary. Neither may
// be enabled until both transports pass the same candidate acceptance suite.
const clickhousePublicEndpointsQualified = false

// Change only after the exact release candidate passes native Oracle Free
// endpoint qualification. Enterprise and Data Guard have a separate licensed
// acceptance boundary and are intentionally excluded from this gate.
const oracleFreePublicEndpointsQualified = false

const mongodbPublicEndpointsQualified = false
const redisPublicEndpointsQualified = false
const vitessPublicEndpointsQualified = false

type PublicEndpointRoute struct {
	Purpose         string `json:"purpose"`
	Protocol        string `json:"protocol"`
	Routing         string `json:"routing"`
	ReadOnly        bool   `json:"read_only"`
	Pooled          bool   `json:"pooled"`
	BackendService  string `json:"-"`
	BackendPort     int    `json:"-"`
	BackendPortName string `json:"-"`
	BackendUser     string `json:"-"`
	BackendDatabase string `json:"-"`
}

func (r PublicEndpointRoute) Equal(other PublicEndpointRoute) bool {
	return r.Purpose == other.Purpose && r.Protocol == other.Protocol && r.Routing == other.Routing && r.ReadOnly == other.ReadOnly && r.Pooled == other.Pooled
}

// The review binds the private backend mapping without offering service names
// or ports as customer-controlled fields.
func (r PublicEndpointRoute) Fingerprint() string {
	values := []any{r.Purpose, r.Protocol, r.Routing, r.ReadOnly, r.Pooled, r.BackendService, r.BackendPort, r.BackendPortName}
	// Keep existing PostgreSQL, MySQL and ClickHouse fingerprints stable while
	// binding routes that also select a server-owned user and logical database.
	if r.BackendUser != "" || r.BackendDatabase != "" {
		values = append(values, r.BackendUser, r.BackendDatabase)
	}
	encoded, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

type PublicEndpointCapabilities struct {
	Engine            string                `json:"engine"`
	Available         bool                  `json:"available"`
	UnavailableReason string                `json:"unavailable_reason"`
	Routes            []PublicEndpointRoute `json:"routes"`
}

func PublicEndpointAvailability(s Spec) error {
	if s.Engine != "postgresql" && s.Engine != "mysql" && s.Engine != "clickhouse" && s.Engine != "oracle" && s.Engine != "mongodb" && s.Engine != "redis" && s.Engine != "vitess" {
		return fmt.Errorf("public endpoints are unavailable for this database engine")
	}
	if !s.TLSRequired() {
		return fmt.Errorf("public endpoints require database TLS")
	}
	if s.Engine == "mongodb" && !mongodbPublicEndpointsQualified {
		return fmt.Errorf("MongoDB public endpoints are unavailable until native member discovery, TLS, failover and revocation qualification is complete")
	}
	if s.Engine == "redis" && !redisPublicEndpointsQualified {
		return fmt.Errorf("Redis public endpoints are unavailable until native private and public member discovery, TLS, failover and revocation qualification is complete")
	}
	if s.Engine == "vitess" && !vitessPublicEndpointsQualified {
		return fmt.Errorf("Vitess public endpoints are unavailable until native vtgate TLS, target routing, failover and revocation qualification is complete")
	}
	if s.Engine == "mysql" && !mysqlPublicEndpointsQualified {
		return fmt.Errorf("MySQL public endpoints are unavailable until native Router TLS, routing and revocation qualification is complete")
	}
	if s.Engine == "clickhouse" && !clickhousePublicEndpointsQualified {
		return fmt.Errorf("ClickHouse public endpoints are unavailable until native TLS, HTTPS, identity migration and revocation qualification is complete")
	}
	if s.Engine == "oracle" {
		if s.Oracle == nil || s.Oracle.Edition != "free" || s.Mode != "standalone" {
			return fmt.Errorf("Oracle Enterprise and Data Guard public endpoints are unavailable until licensed native acceptance is complete")
		}
		if !oracleFreePublicEndpointsQualified {
			return fmt.Errorf("Oracle Free public endpoints are unavailable until native TCPS, identity transition and revocation qualification is complete")
		}
	}
	return nil
}

// Routes describe supported database shapes. Availability is a separate hard
// gate; a descriptor does not grant permission to publish a listener.
func PublicEndpointRoutes(s Spec) []PublicEndpointRoute {
	routes := []PublicEndpointRoute{}
	add := func(purpose, protocol, routing, service, portName string, port int, readOnly, pooled bool) {
		routes = append(routes, PublicEndpointRoute{Purpose: purpose, Protocol: protocol, Routing: routing, ReadOnly: readOnly, Pooled: pooled, BackendService: service, BackendPort: port, BackendPortName: portName})
	}
	switch s.Engine {
	case "postgresql":
		add("read_write", "postgresql", "direct", "database-rw", "postgresql", 5432, false, false)
		if s.Replicas > 0 {
			add("read_only", "postgresql", "direct", "database-ro", "postgresql", 5432, true, false)
		}
		if s.Pooling != nil {
			add("pooled_read_write", "postgresql", "pgbouncer", "database-pool-rw", "postgresql", 5432, false, true)
			if s.Pooling.ReadOnly && s.Replicas > 0 {
				add("pooled_read_only", "postgresql", "pgbouncer", "database-pool-ro", "postgresql", 5432, true, true)
			}
		}
	case "mysql":
		add("read_write", "mysql", "mysql_router", "database", "mysql-alternate", 6446, false, false)
		if s.Replicas > 0 {
			add("read_only", "mysql", "mysql_router", "database", "mysql-ro", 6447, true, false)
		}
	case "vitess":
		add("read_write", "mysql", "vitess_gateway", "database", "mysql", 3306, false, false)
		routes[len(routes)-1].BackendUser = "app"
		routes[len(routes)-1].BackendDatabase = "app@primary"
	case "clickhouse":
		add("native", "clickhouse_native", "direct", "database", "tcp-secure", 9440, false, false)
		add("https", "https", "direct", "database", "https", 8443, false, false)
	case "oracle":
		if s.Oracle != nil && s.Oracle.Edition == "free" && s.Mode == "standalone" && s.Replicas == 0 && s.Shards == 1 {
			add("read_write", "oracle_tcps", "direct", "database", "tcps", 2484, false, false)
			routes[len(routes)-1].BackendUser = "APP"
			routes[len(routes)-1].BackendDatabase = "FREEPDB1"
		}
	case "mongodb":
		add("read_write", "mongodb", "replica_set_horizons", "database-svc", "mongodb", 27017, false, false)
	case "redis":
		if s.Mode == "standalone" {
			add("read_write", "redis", "direct", "database", "redis-client", 6379, false, false)
		} else {
			add("cluster", "redis", "client_address_mapping", "database-leader", "redis", 6379, false, false)
		}
	}
	return routes
}

func PublicEndpointRouteFor(s Spec, purpose string) (PublicEndpointRoute, error) {
	for _, route := range PublicEndpointRoutes(s) {
		if route.Purpose == purpose {
			return route, nil
		}
	}
	return PublicEndpointRoute{}, fmt.Errorf("the selected public route is unavailable for this database")
}

func PublicEndpointCapabilitiesFor(s Spec) PublicEndpointCapabilities {
	result := PublicEndpointCapabilities{Engine: s.Engine, Routes: PublicEndpointRoutes(s)}
	if err := PublicEndpointAvailability(s); err != nil {
		result.UnavailableReason = err.Error()
	} else {
		result.Available = true
	}
	return result
}

var PostgreSQLPublicEndpointPurposes = []string{
	"read_write",
	"read_only",
	"pooled_read_write",
	"pooled_read_only",
}

var publicEndpointHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func NormalizePublicEndpointNames(names []string) ([]string, error) {
	if len(names) > MaxPublicEndpointNames {
		return nil, fmt.Errorf("database public certificate names exceed their bound")
	}
	result := append([]string(nil), names...)
	for _, name := range result {
		if len(name) > 253 || !publicEndpointHostPattern.MatchString(name) {
			return nil, fmt.Errorf("database public certificate hostname is invalid")
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

// PublicEndpointSpec is the customer-controlled part of a database listener.
// The address, hostname and external port always come from operator inventory.
type PublicEndpointSpec struct {
	Purpose        string   `json:"purpose"`
	SourceCIDRs    []string `json:"source_cidrs"`
	MaxConnections int      `json:"max_connections"`
}

func (s PublicEndpointSpec) Normalize() (PublicEndpointSpec, error) {
	if !slices.Contains(PostgreSQLPublicEndpointPurposes, s.Purpose) && s.Purpose != "native" && s.Purpose != "https" && s.Purpose != "cluster" {
		return s, fmt.Errorf("purpose must match one of the database's advertised public routes")
	}
	if len(s.SourceCIDRs) < 1 || len(s.SourceCIDRs) > 16 {
		return s, fmt.Errorf("source_cidrs must contain 1–16 explicit IPv4 networks")
	}
	seen := map[string]bool{}
	canonical := make([]string, 0, len(s.SourceCIDRs))
	for _, raw := range s.SourceCIDRs {
		if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n") {
			return s, fmt.Errorf("source_cidrs must use IPv4 CIDR notation")
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().Is4() {
			return s, fmt.Errorf("source_cidrs must use IPv4 CIDR notation")
		}
		value := prefix.Masked().String()
		if seen[value] {
			return s, fmt.Errorf("source_cidrs must not contain duplicate networks")
		}
		seen[value] = true
		canonical = append(canonical, value)
	}
	slices.Sort(canonical)
	s.SourceCIDRs = canonical
	if s.MaxConnections < 1 || s.MaxConnections > 256 {
		return s, fmt.Errorf("max_connections must be between 1 and 256")
	}
	return s, nil
}

func (s PublicEndpointSpec) Equal(other PublicEndpointSpec) bool {
	a, errA := s.Normalize()
	b, errB := other.Normalize()
	return errA == nil && errB == nil && reflect.DeepEqual(a, b)
}

type PublicEndpointAllocation struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Address string `json:"address"`
	Port    int32  `json:"port"`
}

// Each discovered member keeps its own allocation until all routes and sessions
// have closed. A pod replacement requires a new review of the member identity.
type PublicEndpointMemberAllocation struct {
	MemberName string                   `json:"member_name"`
	MemberUID  string                   `json:"member_uid"`
	Allocation PublicEndpointAllocation `json:"allocation"`
}

var publicEndpointMemberPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// PublicEndpointAllocations also reads existing singleton endpoints. Callers
// must reject invalid inventories before provider or cluster mutations.
func PublicEndpointAllocations(endpoint PublicEndpoint) ([]PublicEndpointMemberAllocation, error) {
	members := append([]PublicEndpointMemberAllocation(nil), endpoint.MemberAllocations...)
	if len(members) == 0 {
		members = []PublicEndpointMemberAllocation{{Allocation: endpoint.Allocation}}
	} else if len(members) > MaxMembers || members[0].Allocation != endpoint.Allocation {
		return nil, fmt.Errorf("database public member allocations exceed their bound or disagree with the primary allocation")
	}
	ids, hosts, addresses := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, member := range members {
		if len(endpoint.MemberAllocations) > 0 && (!publicEndpointMemberPattern.MatchString(member.MemberName) || member.MemberUID == "" || len(member.MemberUID) > 128 || strings.IndexFunc(member.MemberUID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || i > 0 && members[i-1].MemberName >= member.MemberName) {
			return nil, fmt.Errorf("database public members must have unique ordered names and immutable identities")
		}
		allocation := member.Allocation
		address, err := netip.ParseAddr(allocation.Address)
		if allocation.ID == "" || len(allocation.ID) > 64 || strings.TrimSpace(allocation.ID) != allocation.ID || strings.ContainsAny(allocation.ID, "\x00\r\n") || len(allocation.Host) > 253 || !publicEndpointHostPattern.MatchString(allocation.Host) || err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() || allocation.Port < 1 || allocation.Port > 65535 {
			return nil, fmt.Errorf("the operator endpoint allocation is invalid")
		}
		addressPort := netip.AddrPortFrom(address, uint16(allocation.Port)).String()
		if ids[allocation.ID] || hosts[allocation.Host] || addresses[addressPort] {
			return nil, fmt.Errorf("database public members must have distinct allocations, hosts and listeners")
		}
		ids[allocation.ID], hosts[allocation.Host], addresses[addressPort] = true, true, true
	}
	return members, nil
}

func PublicEndpointAllocationNames(endpoint PublicEndpoint) ([]string, error) {
	members, err := PublicEndpointAllocations(endpoint)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.Allocation.Host)
	}
	return NormalizePublicEndpointNames(names)
}

type PublicEndpointObservation struct {
	Configured         bool                          `json:"configured"`
	ExternallyVerified bool                          `json:"externally_verified"`
	Message            string                        `json:"message"`
	CheckedAt          *time.Time                    `json:"checked_at,omitempty"`
	ClientAddressMap   []PublicEndpointClientAddress `json:"client_address_map,omitempty"`
}

// Redis has one advertised address space. NAT-aware clients map every verified
// private member destination to its dedicated public listener and TLS hostname.
type PublicEndpointClientAddress struct {
	MemberName          string   `json:"member_name"`
	MemberUID           string   `json:"member_uid"`
	AdvertisedAddresses []string `json:"advertised_addresses"`
	PublicHost          string   `json:"public_host"`
	PublicPort          int32    `json:"public_port"`
}

// PublicEndpoint is durable independently of the database revision. Revoking
// keeps its allocation until HAProxy has closed old and new connections.
type PublicEndpoint struct {
	ID                string                           `json:"id"`
	DatabaseID        string                           `json:"database_id"`
	Revision          int64                            `json:"revision"`
	Spec              PublicEndpointSpec               `json:"spec"`
	Allocation        PublicEndpointAllocation         `json:"allocation"`
	MemberAllocations []PublicEndpointMemberAllocation `json:"member_allocations,omitempty"`
	Status            string                           `json:"status"`
	Observation       PublicEndpointObservation        `json:"observation"`
	CreatedAt         time.Time                        `json:"created_at"`
	UpdatedAt         time.Time                        `json:"updated_at"`
	RevokedAt         *time.Time                       `json:"revoked_at,omitempty"`
}

type PublicEndpointReview struct {
	DatabaseID          string                           `json:"database_id"`
	Project             string                           `json:"project"`
	Environment         string                           `json:"environment"`
	DatabaseRevision    int64                            `json:"database_revision"`
	EndpointID          string                           `json:"endpoint_id"`
	EndpointRevision    int64                            `json:"endpoint_revision"`
	Spec                PublicEndpointSpec               `json:"spec"`
	Route               *PublicEndpointRoute             `json:"route,omitempty"`
	RouteFingerprint    string                           `json:"route_fingerprint,omitempty"`
	Allocation          PublicEndpointAllocation         `json:"allocation"`
	MemberAllocations   []PublicEndpointMemberAllocation `json:"member_allocations,omitempty"`
	TopologyFingerprint string                           `json:"topology_fingerprint"`
	TLSFingerprint      string                           `json:"tls_fingerprint"`
	// AuthorityFingerprint binds a managed service review to the immutable
	// operator inventory and provider ownership preflight. Self-hosted reviews
	// leave it empty and retain their existing DNS-before-review behavior.
	AuthorityFingerprint string    `json:"authority_fingerprint,omitempty"`
	BlockedReasons       []string  `json:"blocked_reasons"`
	Warnings             []string  `json:"warnings"`
	ExpiresAt            time.Time `json:"expires_at"`
}

// Previously persisted PostgreSQL reviews did not carry a descriptor. Their
// purpose and database revision still select the same fixed PostgreSQL route.
// Missing descriptors never qualify a MySQL review.
func (r PublicEndpointReview) MatchesRoute(current PublicEndpointReview) bool {
	if current.Route == nil {
		return false
	}
	if r.Route == nil {
		return r.RouteFingerprint == "" && current.Route.Protocol == "postgresql" && r.Spec.Purpose == current.Route.Purpose
	}
	// The durable JSON descriptor omits private routing inputs. Its fingerprint
	// binds those inputs, including the selected account and logical database.
	return r.Route.Equal(*current.Route) && r.RouteFingerprint != "" && r.RouteFingerprint == current.RouteFingerprint
}

type PublicEndpointOperation struct {
	ID                 string                            `json:"id"`
	EndpointID         string                            `json:"endpoint_id"`
	DatabaseID         string                            `json:"database_id"`
	Revision           int64                             `json:"revision"`
	Kind               string                            `json:"kind"`
	Status             string                            `json:"status"`
	Phase              string                            `json:"phase"`
	Message            string                            `json:"message"`
	Review             *PublicEndpointReview             `json:"review,omitempty"`
	CreatedAt          time.Time                         `json:"created_at"`
	StartedAt          *time.Time                        `json:"started_at,omitempty"`
	FinishedAt         *time.Time                        `json:"finished_at,omitempty"`
	IdentityID         string                            `json:"-"`
	KeyID              string                            `json:"-"`
	Lease              string                            `json:"-"`
	IdentityTransition *PublicEndpointIdentityTransition `json:"-"`
}

const PublicEndpointIdentityTransitionSchemaVersion = 1

// PublicEndpointIdentityTransition is the durable proof for a database
// identity change that replaces a member. It intentionally contains only
// public identity and Kubernetes object metadata; private keys remain in the
// namespace-owned Secret.
type PublicEndpointIdentityTransition struct {
	SchemaVersion               int                             `json:"schema_version"`
	OperationID                 string                          `json:"operation_id"`
	Engine                      string                          `json:"engine"`
	Kind                        string                          `json:"kind"`
	DatabaseRevision            int64                           `json:"database_revision"`
	EndpointRevision            int64                           `json:"endpoint_revision"`
	RouteFingerprint            string                          `json:"route_fingerprint"`
	DesiredNames                []string                        `json:"desired_names"`
	ReviewedTopologyFingerprint string                          `json:"reviewed_topology_fingerprint"`
	ReviewedLeafFingerprint     string                          `json:"reviewed_leaf_fingerprint"`
	ReviewedCAFingerprint       string                          `json:"reviewed_ca_fingerprint"`
	StatefulSetUID              string                          `json:"statefulset_uid"`
	OriginalGeneration          int64                           `json:"original_generation"`
	OriginalTemplateHash        string                          `json:"original_template_hash"`
	OldMember                   PublicEndpointTransitionMember  `json:"old_member"`
	PVCs                        []PublicEndpointTransitionPVC   `json:"pvcs"`
	ProposedLeafFingerprint     string                          `json:"proposed_leaf_fingerprint,omitempty"`
	ProposedCAFingerprint       string                          `json:"proposed_ca_fingerprint,omitempty"`
	TargetGeneration            int64                           `json:"target_generation,omitempty"`
	TargetTemplateHash          string                          `json:"target_template_hash,omitempty"`
	FinalMember                 *PublicEndpointTransitionMember `json:"final_member,omitempty"`
	FinalTopologyFingerprint    string                          `json:"final_topology_fingerprint,omitempty"`
	ServedLeafFingerprint       string                          `json:"served_leaf_fingerprint,omitempty"`
	ServedCAFingerprint         string                          `json:"served_ca_fingerprint,omitempty"`
}

type PublicEndpointTransitionMember struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type PublicEndpointTransitionPVC struct {
	Name                     string `json:"name"`
	UID                      string `json:"uid"`
	VolumeName               string `json:"volume_name"`
	PersistentVolumeUID      string `json:"persistent_volume_uid"`
	BackingVolumeFingerprint string `json:"backing_volume_fingerprint"`
}

func PlanPublicEndpoint(d Resource, spec PublicEndpointSpec, allocation PublicEndpointAllocation, endpointID string, endpointRevision int64, now time.Time) (PublicEndpointReview, error) {
	if err := PublicEndpointAvailability(d.Spec); err != nil {
		return PublicEndpointReview{}, err
	}
	return planPublicEndpoint(d, spec, allocation, endpointID, endpointRevision, now)
}

func PublicEndpointRequiresMembers(s Spec) bool {
	return s.Engine == "mongodb" || s.Engine == "redis" && s.Mode == "cluster"
}

func PlanPublicEndpointMembers(d Resource, spec PublicEndpointSpec, endpoint PublicEndpoint, now time.Time) (PublicEndpointReview, error) {
	if err := PublicEndpointAvailability(d.Spec); err != nil {
		return PublicEndpointReview{}, err
	}
	return planPublicEndpointMembers(d, spec, endpoint, now)
}

func planPublicEndpointMembers(d Resource, spec PublicEndpointSpec, endpoint PublicEndpoint, now time.Time) (PublicEndpointReview, error) {
	members, err := PublicEndpointAllocations(endpoint)
	if err != nil {
		return PublicEndpointReview{}, err
	}
	if !PublicEndpointRequiresMembers(d.Spec) {
		if len(endpoint.MemberAllocations) != 0 {
			return PublicEndpointReview{}, fmt.Errorf("this database route requires one allocation")
		}
		return planPublicEndpoint(d, spec, endpoint.Allocation, endpoint.ID, endpoint.Revision, now)
	}
	if len(endpoint.MemberAllocations) != d.Spec.Members() || len(d.Observation.Members) != d.Spec.Members() {
		return PublicEndpointReview{}, fmt.Errorf("every observed database member requires a public allocation")
	}
	observed := make(map[string]Member, len(d.Observation.Members))
	for _, member := range d.Observation.Members {
		if _, exists := observed[member.Name]; exists {
			return PublicEndpointReview{}, fmt.Errorf("database observation contains duplicate members")
		}
		observed[member.Name] = member
	}
	for _, allocation := range members {
		member, exists := observed[allocation.MemberName]
		if !exists || !member.Ready || member.UID != allocation.MemberUID {
			return PublicEndpointReview{}, fmt.Errorf("database public member identity changed or is not ready")
		}
	}
	plan, err := planPublicEndpoint(d, spec, endpoint.Allocation, endpoint.ID, endpoint.Revision, now)
	if err != nil {
		return plan, err
	}
	plan.MemberAllocations = append([]PublicEndpointMemberAllocation(nil), endpoint.MemberAllocations...)
	return plan, nil
}

func planPublicEndpoint(d Resource, spec PublicEndpointSpec, allocation PublicEndpointAllocation, endpointID string, endpointRevision int64, now time.Time) (PublicEndpointReview, error) {
	plan := PublicEndpointReview{
		DatabaseID: d.ID, Project: d.Project, Environment: d.Environment,
		DatabaseRevision: d.Revision, EndpointID: endpointID, EndpointRevision: endpointRevision,
		Allocation: allocation, TopologyFingerprint: d.Observation.TopologyFingerprint,
		BlockedReasons: []string{}, Warnings: []string{}, ExpiresAt: now.Add(PublicEndpointReviewLifetime),
	}
	if d.Spec.Engine == "redis" && d.Spec.Mode == "cluster" {
		plan.Warnings = append(plan.Warnings, "Public Redis Cluster access requires a client that maps every advertised private member address to its dedicated public TLS listener. A Redis URI alone does not configure this mapping.")
	}
	var err error
	plan.Spec, err = spec.Normalize()
	if err != nil {
		return plan, err
	}
	route, err := PublicEndpointRouteFor(d.Spec, plan.Spec.Purpose)
	if err != nil {
		return plan, err
	}
	plan.Route, plan.RouteFingerprint = &route, route.Fingerprint()
	supported := false
	for _, endpoint := range d.Observation.Endpoints {
		if endpoint.Purpose == plan.Spec.Purpose && endpoint.Port == route.BackendPort {
			supported = true
			break
		}
	}
	if !supported {
		return plan, fmt.Errorf("the selected route is unavailable for this database")
	}
	if _, err := PublicEndpointAllocations(PublicEndpoint{Allocation: allocation}); err != nil {
		return plan, err
	}
	if d.Status != "ready" || d.Recovery != nil && (d.Recovery.RestoredAt == nil || d.Recovery.InspectedAt == nil) {
		plan.BlockedReasons = append(plan.BlockedReasons, "The database is not ready for public access.")
	}
	if d.Observation.Status != "ready" || !d.Observation.Fresh(now, d.Revision) {
		plan.BlockedReasons = append(plan.BlockedReasons, "A current healthy database observation is required.")
	}
	if d.Observation.TopologyFingerprint == "" {
		plan.BlockedReasons = append(plan.BlockedReasons, "A verified database topology fingerprint is required.")
	}
	if d.Observation.TLS == nil || !d.Observation.TLS.Verified || !d.Observation.TLS.PlaintextRejected || d.Observation.TLS.Fingerprint == "" {
		plan.BlockedReasons = append(plan.BlockedReasons, "Verified native database TLS and plaintext refusal are required.")
	} else {
		plan.TLSFingerprint = d.Observation.TLS.Fingerprint
	}
	if slices.Contains(plan.Spec.SourceCIDRs, "0.0.0.0/0") {
		plan.Warnings = append(plan.Warnings, "Every IPv4 address may connect to this port. Database credentials and the downloaded private CA remain required.")
	}
	if d.Spec.Engine == "oracle" && d.Spec.Oracle != nil && d.Spec.Oracle.Edition == "free" {
		plan.Warnings = append(plan.Warnings, "Oracle Free restarts its single database member to load the reviewed TCPS identity. Existing database connections will end during the replacement.")
	}
	plan.Warnings = append(plan.Warnings, "Publishing or changing this endpoint closes existing connections before the reviewed route opens.")
	return plan, nil
}
