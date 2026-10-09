package database

import "time"

// Capacity is a reserved database envelope, including replacement and recovery.
// Zero values never mean unlimited when a capacity resolver is configured.
type Capacity struct {
	CPUMilli    int64 `json:"cpu_milli" toml:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes" toml:"memory_bytes"`
	StorageGiB  int64 `json:"storage_gib" toml:"storage_gib"`
}

type CapacityAvailability struct {
	Known     bool     `json:"known"`
	Reason    string   `json:"reason,omitempty"`
	Limit     Capacity `json:"limit"`
	Used      Capacity `json:"used"`
	Remaining Capacity `json:"remaining"`
	AfterPlan Capacity `json:"after_plan"`
}

type ResourceAmount struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
	StorageGiB  int64 `json:"storage_gib"`
}

type RecommendationUsage struct {
	CurrentAvailable      bool       `json:"current_available"`
	CurrentCPUMilli       *float64   `json:"current_cpu_milli,omitempty"`
	CurrentMemoryBytes    *int64     `json:"current_memory_bytes,omitempty"`
	Peak24hCPUMilli       *float64   `json:"peak_24h_cpu_milli,omitempty"`
	Peak24hMemoryBytes    *int64     `json:"peak_24h_memory_bytes,omitempty"`
	Samples24h            int        `json:"samples_24h"`
	OldestSampleAt        *time.Time `json:"oldest_sample_at,omitempty"`
	NewestSampleAt        *time.Time `json:"newest_sample_at,omitempty"`
	ConnectedApplications int        `json:"connected_applications"`
	ConnectedServices     int        `json:"connected_services"`
	ConnectionsTruncated  bool       `json:"connections_truncated"`
}

type SizingRecommendation struct {
	PolicyVersion int      `json:"policy_version"`
	CPUMilli      int64    `json:"cpu_milli"`
	MemoryBytes   int64    `json:"memory_bytes"`
	Confidence    string   `json:"confidence"`
	Reasons       []string `json:"reasons"`
}

type CapacityPlan struct {
	DatabaseID          string               `json:"database_id,omitempty"`
	Spec                Spec                 `json:"spec"`
	PerMember           ResourceAmount       `json:"per_member"`
	RequestedAllocation Capacity             `json:"requested_allocation"`
	EffectiveAllocation Capacity             `json:"effective_allocation"`
	Capacity            CapacityAvailability `json:"capacity"`
	Usage               RecommendationUsage  `json:"usage"`
	Recommendation      SizingRecommendation `json:"recommendation"`
	Advisory            bool                 `json:"advisory"`
}
