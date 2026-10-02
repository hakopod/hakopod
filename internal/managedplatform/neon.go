package managedplatform

import (
	"fmt"
	"regexp"
)

// This immutable upstream snapshot is an implementation input, not a qualified release.
const NeonVersion = "fa504217c61bbcaf5c512d75830564541f917f8f"
const NeonProxyControlPlanePatchSHA256 = "e9a1df309106d166adfc0982500f6500df220dbc6173761c48c7c2038563fbd6"

var neonComponents = []string{"broker", "compute", "compute-tls", "controller-database", "pageserver", "proxy", "safekeeper", "storage-controller"}
var neonSecretKeys = []string{"broker-auth", "compute-auth", "controller-auth", "controller-database-password", "object-storage", "pageserver-auth", "proxy-auth", "safekeeper-auth"}
var neonStorageKeys = []string{"compute-cache", "controller-database", "pageserver", "safekeeper"}

func NeonComponents() []string  { return append([]string(nil), neonComponents...) }
func NeonSecretKeys() []string  { return append([]string(nil), neonSecretKeys...) }
func NeonStorageKeys() []string { return append([]string(nil), neonStorageKeys...) }

type NeonConfig struct {
	PostgresVersion              string `json:"postgres_version" toml:"postgres_version"`
	ComputeReplicas              int    `json:"compute_replicas" toml:"compute_replicas"`
	Pageservers                  int    `json:"pageservers" toml:"pageservers"`
	Safekeepers                  int    `json:"safekeepers" toml:"safekeepers"`
	BranchLimit                  int    `json:"branch_limit" toml:"branch_limit"`
	ObjectStorageURL             string `json:"object_storage_url" toml:"object_storage_url"`
	ObjectStorageBucket          string `json:"object_storage_bucket" toml:"object_storage_bucket"`
	ObjectStorageRegion          string `json:"object_storage_region" toml:"object_storage_region"`
	ObjectStoragePrefix          string `json:"object_storage_prefix" toml:"object_storage_prefix"`
	ProxyControlPlanePatchSHA256 string `json:"proxy_control_plane_patch_sha256" toml:"proxy_control_plane_patch_sha256"`
}

func (s Spec) ValidateNeon() error {
	if s.Kind != "neon" || s.Version != NeonVersion || s.Neon == nil {
		return fmt.Errorf("Neon requires the immutable upstream snapshot %s", NeonVersion)
	}
	if err := s.ValidateResources(neonComponents); err != nil {
		return err
	}
	if err := s.ValidateStorage(neonStorageKeys); err != nil {
		return err
	}
	if err := s.ValidateSecrets(neonSecretKeys); err != nil {
		return err
	}
	c := s.Neon
	if c.PostgresVersion != "17" {
		return fmt.Errorf("the Neon source candidate requires PostgreSQL 17")
	}
	if c.ComputeReplicas < 1 || c.ComputeReplicas > 6 || c.Pageservers < 2 || c.Pageservers > 8 || c.Safekeepers != 3 || c.BranchLimit < 1 || c.BranchLimit > 64 {
		return fmt.Errorf("Neon requires 1-6 compute replicas, 2-8 pageservers, three safekeepers and a 1-64 branch limit")
	}
	if err := s.Placement.Validate("cluster", max(3, c.Pageservers)); err != nil {
		return err
	}
	if len(s.Placement.NodeNames) < max(3, c.Pageservers) {
		return fmt.Errorf("Neon requires explicit placement.node_names for every storage member so availability zones come from Kubernetes node inventory")
	}
	if err := ValidateObjectStorageOrigin(c.ObjectStorageURL); err != nil {
		return err
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(c.ObjectStorageBucket) || !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`).MatchString(c.ObjectStoragePrefix) || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(c.ObjectStorageRegion) {
		return fmt.Errorf("Neon requires an explicit bucket, region and bounded resource-specific object prefix")
	}
	if c.ProxyControlPlanePatchSHA256 != NeonProxyControlPlanePatchSHA256 {
		return fmt.Errorf("Neon proxy image must be built from the reviewed bounded-timeout patch %s", NeonProxyControlPlanePatchSHA256)
	}
	return nil
}

func PlanNeon(s Spec, images map[string]string) (Plan, error) {
	if err := s.Validate(); err != nil {
		return Plan{}, err
	}
	if s.Kind != "neon" {
		return Plan{}, fmt.Errorf("Neon planning requires a Neon platform")
	}
	if err := ValidateImages(images, neonComponents); err != nil {
		return Plan{}, err
	}
	c := s.Neon
	components := []Component{
		{Name: "broker", Replicas: 1, Ports: []int32{50051}, SecretKeys: []string{"broker-auth"}},
		{Name: "compute", Replicas: c.ComputeReplicas, Ports: []int32{3081, 55433}, SecretKeys: []string{"compute-auth", "pageserver-auth", "safekeeper-auth"}, StorageKeys: []string{"compute-cache"}},
		{Name: "compute-tls", Replicas: c.ComputeReplicas, Ports: []int32{3081}, SecretKeys: []string{"compute-auth"}},
		{Name: "controller-database", Replicas: 1, Ports: []int32{5432}, SecretKeys: []string{"controller-database-password"}, StorageKeys: []string{"controller-database"}},
		{Name: "pageserver", Replicas: c.Pageservers, Ports: []int32{6400, 9898}, SecretKeys: []string{"pageserver-auth", "object-storage"}, StorageKeys: []string{"pageserver"}},
		{Name: "proxy", Replicas: 1, Ports: []int32{5432, 7001}, SecretKeys: []string{"proxy-auth"}},
		{Name: "safekeeper", Replicas: c.Safekeepers, Ports: []int32{5454, 7676}, SecretKeys: []string{"safekeeper-auth", "object-storage"}, StorageKeys: []string{"safekeeper"}},
		{Name: "storage-controller", Replicas: 1, Ports: []int32{6699}, SecretKeys: []string{"controller-auth", "controller-database-password", "pageserver-auth", "safekeeper-auth"}},
	}
	for i := range components {
		components[i].Image = images[components[i].Name]
		components[i].Resources = s.Resources[components[i].Name]
	}
	return Plan{Namespace: "managed-platform-" + s.Name, Components: components, PublicService: "proxy", Capability: Capability{Reason: "Neon tenant, timeline and compute control plane, secure runtime, recovery and native acceptance are not complete"}}, nil
}
