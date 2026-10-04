package managedplatform

import "testing"

func TestCatalogDefaultsValidateAfterRequiredUserConfiguration(t *testing.T) {
	for _, item := range CatalogEntries() {
		spec := item.DefaultSpec
		spec.Name = "configured"
		for _, key := range item.RequiredSecretKeys {
			spec.Secrets[key] = SecretReference{Name: key, Revision: 1}
		}
		if item.Kind == "supabase" {
			for _, component := range []string{"postgres-meta", "storage"} {
				if resources := spec.Resources[component]; resources.CPU != "250m" || resources.Memory != "512Mi" {
					t.Fatalf("Supabase %s default resources were reduced to its safety floor: %#v", component, resources)
				}
			}
			spec.Placement.NodeNames = []string{"node-a"}
			spec.Supabase.PublicURL, spec.Supabase.SiteURL = "https://api.example.com", "https://app.example.com"
		} else {
			spec.Placement.NodeNames = []string{"node-a", "node-b", "node-c"}
			spec.Neon.ObjectStorageURL = "https://objects.example.com"
			spec.Neon.ObjectStorageBucket, spec.Neon.ObjectStorageRegion, spec.Neon.ObjectStoragePrefix = "database-backups", "us-east-1", "configured"
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("%s defaults: %v", item.Kind, err)
		}
	}
}
