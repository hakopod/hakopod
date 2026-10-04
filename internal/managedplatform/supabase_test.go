package managedplatform

import (
	"strings"
	"testing"
)

func supabaseCandidateSpec() Spec {
	resources := map[string]Resources{}
	images := SupabaseComponentNames()
	for _, name := range images {
		resources[name] = Resources{CPU: "250m", Memory: "512Mi"}
	}
	resources["database"] = Resources{CPU: "500m", Memory: "2Gi"}
	secrets := map[string]SecretReference{}
	for _, name := range SupabaseRequiredSecretKeys() {
		secrets[name] = SecretReference{Name: "supabase-" + name, Revision: 1}
	}
	return Spec{
		SchemaVersion: 1,
		Name:          "customer-data",
		Kind:          "supabase",
		Version:       SupabaseVersion,
		Resources:     resources,
		Storage:       map[string]int64{"database": 20, "database-encryption": 1, "edge-functions": 1, "objects": 20, "studio-snippets": 1},
		Secrets:       secrets,
		Placement:     Placement{NodeNames: []string{"worker-a"}},
		Supabase: &SupabaseConfig{
			PublicURL:             "https://data.example.test",
			SiteURL:               "https://app.example.test",
			RedirectURLs:          []string{"https://app.example.test"},
			DatabaseName:          "postgres",
			JWTExpirySeconds:      3600,
			RESTMaxRows:           1000,
			StorageFileLimitBytes: 50 << 20,
			PoolSize:              20,
			PoolMaxClients:        100,
		},
	}
}

func TestSupabaseCandidateRemainsUnavailable(t *testing.T) {
	spec := supabaseCandidateSpec()
	images := map[string]string{}
	for _, name := range SupabaseComponentNames() {
		images[name] = "registry.example.test/supabase/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	plan, err := PlanSupabase(spec, images)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Capability.Available || plan.Capability.ClusterQualified || plan.Capability.PublicQualified {
		t.Fatal("unqualified Supabase candidate became available")
	}
	if len(plan.Components) != len(SupabaseComponentNames()) || plan.PublicService != "api-gateway" {
		t.Fatal("Supabase plan omitted a required component")
	}
}

func TestSupabaseCandidateRejectsUnsafeConfiguration(t *testing.T) {
	base := supabaseCandidateSpec()
	cases := []func(*Spec){
		func(s *Spec) { s.Version = "latest" },
		func(s *Spec) { s.Kind = "neon" },
		func(s *Spec) { delete(s.Resources, "realtime") },
		func(s *Spec) { delete(s.Storage, "objects") },
		func(s *Spec) { delete(s.Secrets, "jwt-secret") },
		func(s *Spec) { delete(s.Secrets, "pooler-api-jwt-secret") },
		func(s *Spec) { s.Secrets["pooler-api-jwt-secret"] = s.Secrets["jwt-secret"] },
		func(s *Spec) { s.Resources["database"] = Resources{CPU: "499m", Memory: "2Gi"} },
		func(s *Spec) { s.Resources["database"] = Resources{CPU: "500m", Memory: "2047Mi"} },
		func(s *Spec) { s.Resources["realtime"] = Resources{CPU: "249m", Memory: "512Mi"} },
		func(s *Spec) { s.Resources["pooler"] = Resources{CPU: "250m", Memory: "511Mi"} },
		func(s *Spec) { s.Resources["postgres-meta"] = Resources{CPU: "249m", Memory: "256Mi"} },
		func(s *Spec) { s.Resources["postgres-meta"] = Resources{CPU: "250m", Memory: "255Mi"} },
		func(s *Spec) { s.Resources["storage"] = Resources{CPU: "249m", Memory: "256Mi"} },
		func(s *Spec) { s.Resources["storage"] = Resources{CPU: "250m", Memory: "255Mi"} },
		func(s *Spec) { s.Supabase.PublicURL = "http://data.example.test" },
		func(s *Spec) { s.Supabase.PublicURL = "https://*.example.test" },
		func(s *Spec) { s.Supabase.PublicURL = "https://data.example.test?" },
		func(s *Spec) { s.Supabase.PublicURL = "https://data.example.test#" },
		func(s *Spec) { s.Supabase.PublicURL = "https://data.example.test:8443" },
		func(s *Spec) { s.Supabase.PublicURL = "https://data.example.test/%2e%2e" },
		func(s *Spec) { s.Supabase.RedirectURLs = []string{"https://app.example.test/callback"} },
		func(s *Spec) { s.Supabase.DatabaseName = "custom" },
		func(s *Spec) { s.Placement.Spread = "nodes" },
		func(s *Spec) { s.Supabase.EmailSignup = true },
		func(s *Spec) { s.Supabase.SMTPSecret = &SecretReference{Name: "smtp", Revision: 1} },
		func(s *Spec) { s.Supabase.PoolMaxClients = 1001 },
	}
	for _, mutate := range cases {
		spec := base
		spec.Resources = cloneMap(base.Resources)
		spec.Storage = cloneMap(base.Storage)
		spec.Secrets = cloneMap(base.Secrets)
		config := *base.Supabase
		spec.Supabase = &config
		mutate(&spec)
		if err := spec.ValidateSupabase(); err == nil {
			t.Fatal("unsafe Supabase configuration was accepted")
		}
	}
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
