package managedplatform

import "k8s.io/apimachinery/pkg/api/resource"

// CatalogEntry supplies editable desired configuration. Its capability is the
// runtime qualification gate; the defaults do not describe a running platform.
type CatalogEntry struct {
	Kind               string     `json:"kind"`
	Version            string     `json:"version"`
	MinimumNodes       int        `json:"minimum_nodes"`
	MaximumNodes       int        `json:"maximum_nodes"`
	RequiredSecretKeys []string   `json:"required_secret_keys"`
	DefaultSpec        Spec       `json:"default_spec"`
	Capability         Capability `json:"capability"`
}

func CatalogEntries() []CatalogEntry {
	defaults := func(kind, version string, components, storage []string) Spec {
		s := Spec{SchemaVersion: 1, Kind: kind, Version: version, TLSMode: "managed", Resources: map[string]Resources{}, Storage: map[string]int64{}, Secrets: map[string]SecretReference{}}
		for _, name := range components {
			s.Resources[name] = Resources{CPU: "250m", Memory: "512Mi"}
		}
		for _, name := range storage {
			s.Storage[name] = 1
		}
		return s
	}
	supabase := defaults("supabase", SupabaseVersion, SupabaseComponentNames(), SupabaseRequiredStorageKeys())
	for name, minimum := range supabaseResourceMinimums {
		current := supabase.Resources[name]
		currentCPU, minimumCPU := resource.MustParse(current.CPU), resource.MustParse(minimum.CPU)
		currentMemory, minimumMemory := resource.MustParse(current.Memory), resource.MustParse(minimum.Memory)
		if currentCPU.Cmp(minimumCPU) < 0 {
			current.CPU = minimum.CPU
		}
		if currentMemory.Cmp(minimumMemory) < 0 {
			current.Memory = minimum.Memory
		}
		supabase.Resources[name] = current
	}
	supabase.Storage["database"] = 10
	supabase.Supabase = &SupabaseConfig{DatabaseName: "postgres", JWTExpirySeconds: 3600, RESTMaxRows: 1000, StorageFileLimitBytes: 50 << 20, PoolSize: 10, PoolMaxClients: 100}
	neon := defaults("neon", NeonVersion, NeonComponents(), NeonStorageKeys())
	neon.Neon = &NeonConfig{PostgresVersion: "17", ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3, BranchLimit: 1, ProxyControlPlanePatchSHA256: NeonProxyControlPlanePatchSHA256}
	return []CatalogEntry{
		{Kind: "supabase", Version: SupabaseVersion, MinimumNodes: 1, MaximumNodes: 1, RequiredSecretKeys: supabase.UserSecretKeys(SupabaseRequiredSecretKeys()), DefaultSpec: supabase, Capability: Capability{Reason: "Supabase runtime, encrypted storage, recovery and native acceptance remain unqualified"}},
		{Kind: "neon", Version: NeonVersion, MinimumNodes: 3, MaximumNodes: 8, RequiredSecretKeys: neon.UserSecretKeys(NeonSecretKeys()), DefaultSpec: neon, Capability: NeonRuntimeQualification()},
	}
}
