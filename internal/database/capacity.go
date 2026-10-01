package database

// Capacity is a reserved database envelope, including replacement and recovery.
// Zero values never mean unlimited when a capacity resolver is configured.
type Capacity struct {
	CPUMilli    int64 `json:"cpu_milli" toml:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes" toml:"memory_bytes"`
	StorageGiB  int64 `json:"storage_gib" toml:"storage_gib"`
}
