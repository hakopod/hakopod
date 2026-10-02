package managedplatform

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	MaxCapacityReservations        = 256
	MaxCapacityPoolWorkloads       = 1000
	PodMemoryOverheadBytes   int64 = 50 << 20
)

type Capacity struct {
	CPUMilli    int64 `json:"cpu_milli" toml:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes" toml:"memory_bytes"`
	StorageGiB  int64 `json:"storage_gib" toml:"storage_gib"`
}

func (c Capacity) Add(other Capacity) Capacity {
	return Capacity{CPUMilli: c.CPUMilli + other.CPUMilli, MemoryBytes: c.MemoryBytes + other.MemoryBytes, StorageGiB: c.StorageGiB + other.StorageGiB}
}

func (c Capacity) Fits(limit Capacity) bool {
	return c.CPUMilli <= limit.CPUMilli && c.MemoryBytes <= limit.MemoryBytes && c.StorageGiB <= limit.StorageGiB
}

type CapacityNode struct {
	Name            string `json:"name" toml:"name"`
	UID             string `json:"uid" toml:"uid"`
	Architecture    string `json:"architecture" toml:"architecture"`
	OperatingSystem string `json:"operating_system" toml:"operating_system"`
}

// CapacityNamespaceOwnership identifies the exact namespace and workload
// controllers whose pods are already included in durable platform capacity.
// Controller keys use the durable resource claim form, such as
// "deployment.supabase-auth" or "statefulset.neon-pageserver-0".
type CapacityNamespaceOwnership struct {
	PlatformID   string
	PlatformName string
	UID          string
	Controllers  map[string]string
	Workloads    map[string]CapacityWorkloadOwnership
}

// CapacityWorkloadOwnership binds one durable controller identity to the
// exact per-node pod request envelopes reserved for it. Current renderers use
// one replica per controller; placement changes may temporarily retain one
// old and one new node envelope until reconciliation succeeds.
type CapacityWorkloadOwnership struct {
	UID   string
	Nodes map[string]Capacity
}

// CapacityPoolWorkload identifies one durable application or database whose
// pods are already included in the shared pool grant. Capacity is the retained
// rollout envelope used to account for live drift without double counting it.
type CapacityPoolWorkload struct {
	Kind        string
	ID          string
	Project     string
	Environment string
	Capacity    Capacity
}

// CapacityPoolOwnership is the complete durable workload inventory for one
// shared capacity pool. Live node checks use it to avoid counting a reserved
// workload a second time while retaining exact namespace ownership checks.
type CapacityPoolOwnership struct {
	Workloads          []CapacityPoolWorkload
	PlatformNamespaces map[string]CapacityNamespaceOwnership
}

// CapacityPolicy is trusted operator configuration. It is never accepted from
// a managed platform request. Capacity is shared with managed databases.
type CapacityPolicy struct {
	Enabled      bool           `json:"enabled" toml:"enabled"`
	Pool         string         `json:"pool" toml:"pool"`
	Capacity     Capacity       `json:"capacity" toml:"capacity"`
	Nodes        []CapacityNode `json:"nodes" toml:"nodes"`
	StorageClass string         `json:"storage_class" toml:"storage_class"`
}

func (p CapacityPolicy) Validate() error {
	if !p.Enabled || len(validation.IsDNS1123Label(p.Pool)) != 0 || p.Capacity.CPUMilli < 1 || p.Capacity.MemoryBytes < 1 || p.Capacity.StorageGiB < 1 || len(p.Nodes) < 1 || len(p.Nodes) > 48 || len(validation.IsDNS1123Subdomain(p.StorageClass)) != 0 {
		return fmt.Errorf("managed platform capacity policy is unavailable")
	}
	seenNames, seenUIDs := map[string]bool{}, map[string]bool{}
	for _, node := range p.Nodes {
		if len(validation.IsDNS1123Subdomain(node.Name)) != 0 || node.UID == "" || len(node.UID) > 128 || node.Architecture == "" || len(validation.IsQualifiedName(node.Architecture)) != 0 || node.OperatingSystem == "" || len(validation.IsQualifiedName(node.OperatingSystem)) != 0 || seenNames[node.Name] || seenUIDs[node.UID] {
			return fmt.Errorf("managed platform capacity node identity is invalid")
		}
		seenNames[node.Name], seenUIDs[node.UID] = true, true
	}
	return nil
}

func (p CapacityPolicy) Fingerprint() ([32]byte, error) {
	if err := p.Validate(); err != nil {
		return [32]byte{}, err
	}
	copy := p
	copy.Nodes = append([]CapacityNode(nil), p.Nodes...)
	sort.Slice(copy.Nodes, func(i, j int) bool { return copy.Nodes[i].Name < copy.Nodes[j].Name })
	body, err := json.Marshal(copy)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(body), nil
}

func (p CapacityPolicy) Allows(spec Spec, plan Plan) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if plan.StorageClass == "" || plan.StorageClass != p.StorageClass {
		return fmt.Errorf("managed platform storage class is not approved")
	}
	allowed := make(map[string]bool, len(p.Nodes))
	for _, node := range p.Nodes {
		allowed[node.Name] = true
	}
	reservations, err := CapacityReservations(spec, plan)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if !allowed[reservation.NodeName] {
			return fmt.Errorf("managed platform placement includes an unapproved node")
		}
	}
	return nil
}

type CapacityReservation struct {
	Key      string   `json:"key"`
	NodeName string   `json:"node_name"`
	Capacity Capacity `json:"capacity"`
}

func quantityCapacity(resources Resources) (Capacity, error) {
	cpu, err := resource.ParseQuantity(resources.CPU)
	if err != nil || cpu.MilliValue() < 1 {
		return Capacity{}, fmt.Errorf("managed platform CPU reservation is invalid")
	}
	memory, err := resource.ParseQuantity(resources.Memory)
	if err != nil || memory.Value() < 1 {
		return Capacity{}, fmt.Errorf("managed platform memory reservation is invalid")
	}
	return Capacity{CPUMilli: cpu.MilliValue(), MemoryBytes: memory.Value()}, nil
}

// CapacityReservations mirrors the current renderers: Supabase runs on its
// single node; Neon spreads pageservers and safekeepers round-robin and places
// all other pods on node zero. compute-tls is a sidecar in each compute pod.
// Storage is counted once per rendered PVC rather than once per volume mount.
func CapacityReservations(spec Spec, plan Plan) ([]CapacityReservation, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if plan.Namespace == "" || len(plan.Components) < 1 || len(plan.Components) > MaxComponents || len(validation.IsDNS1123Subdomain(plan.StorageClass)) != 0 {
		return nil, fmt.Errorf("managed platform capacity plan is invalid")
	}
	components := make(map[string]Component, len(plan.Components))
	for _, component := range plan.Components {
		if _, exists := components[component.Name]; exists || component.Replicas < 1 || component.Replicas > 64 || component.Resources != spec.Resources[component.Name] {
			return nil, fmt.Errorf("managed platform capacity component inventory is invalid")
		}
		components[component.Name] = component
	}
	var expected []string
	if spec.Kind == "supabase" {
		expected = SupabaseComponentNames()
	} else {
		expected = NeonComponents()
	}
	if len(components) != len(expected) {
		return nil, fmt.Errorf("managed platform capacity component inventory is incomplete")
	}
	for _, name := range expected {
		if _, ok := components[name]; !ok {
			return nil, fmt.Errorf("managed platform capacity component inventory is incomplete")
		}
	}
	if spec.Kind == "neon" {
		expectedReplicas := map[string]int{"broker": 1, "compute": spec.Neon.ComputeReplicas, "compute-tls": spec.Neon.ComputeReplicas, "controller-database": 1, "pageserver": spec.Neon.Pageservers, "proxy": 1, "safekeeper": spec.Neon.Safekeepers, "storage-controller": 1}
		for name, replicas := range expectedReplicas {
			if components[name].Replicas != replicas {
				return nil, fmt.Errorf("Neon capacity must match the rendered replica inventory")
			}
		}
	}
	nodes := spec.Placement.NodeNames
	if len(nodes) == 0 {
		return nil, fmt.Errorf("managed platform capacity placement is empty")
	}
	reservations := make([]CapacityReservation, 0, MaxCapacityReservations)
	addWorkload := func(key, node string, values ...Resources) error {
		amount := Capacity{MemoryBytes: PodMemoryOverheadBytes}
		for _, value := range values {
			parsed, err := quantityCapacity(value)
			if err != nil {
				return err
			}
			amount = amount.Add(parsed)
		}
		reservations = append(reservations, CapacityReservation{Key: "workload/" + key, NodeName: node, Capacity: amount})
		return nil
	}
	if spec.Kind == "supabase" {
		for _, name := range expected {
			component := components[name]
			if component.Replicas != 1 {
				return nil, fmt.Errorf("Supabase capacity must match one rendered pod per component")
			}
			if err := addWorkload(name, nodes[0], component.Resources); err != nil {
				return nil, err
			}
		}
		keys := make([]string, 0, len(spec.Storage))
		for key := range spec.Storage {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			reservations = append(reservations, CapacityReservation{Key: "storage/" + key, NodeName: nodes[0], Capacity: Capacity{StorageGiB: spec.Storage[key]}})
		}
	} else {
		for _, name := range expected {
			component := components[name]
			if name == "compute-tls" {
				continue
			}
			for ordinal := 0; ordinal < component.Replicas; ordinal++ {
				node := nodes[0]
				if name == "pageserver" || name == "safekeeper" {
					node = nodes[ordinal%len(nodes)]
				}
				values := []Resources{component.Resources}
				if name == "compute" {
					values = append(values, components["compute-tls"].Resources)
				}
				if err := addWorkload(name+"/"+strconv.Itoa(ordinal), node, values...); err != nil {
					return nil, err
				}
			}
		}
		storage := func(key string, count int, spread bool) {
			for ordinal := 0; ordinal < count; ordinal++ {
				node := nodes[0]
				if spread {
					node = nodes[ordinal%len(nodes)]
				}
				reservations = append(reservations, CapacityReservation{Key: "storage/" + key + "/" + strconv.Itoa(ordinal), NodeName: node, Capacity: Capacity{StorageGiB: spec.Storage[key]}})
			}
		}
		storage("controller-database", 1, false)
		storage("pageserver", spec.Neon.Pageservers, true)
		storage("safekeeper", spec.Neon.Safekeepers, true)
		storage("compute-cache", spec.Neon.ComputeReplicas, false)
	}
	if len(reservations) > MaxCapacityReservations {
		return nil, fmt.Errorf("managed platform capacity reservation inventory exceeds %d", MaxCapacityReservations)
	}
	sort.Slice(reservations, func(i, j int) bool {
		if reservations[i].Key == reservations[j].Key {
			return reservations[i].NodeName < reservations[j].NodeName
		}
		return reservations[i].Key < reservations[j].Key
	})
	return reservations, nil
}

func ReservationTotal(reservations []CapacityReservation) Capacity {
	total := Capacity{}
	for _, reservation := range reservations {
		total = total.Add(reservation.Capacity)
	}
	return total
}
