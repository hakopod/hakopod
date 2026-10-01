package managedplatform

import (
	"fmt"
	"regexp"
	"sort"

	"k8s.io/apimachinery/pkg/api/resource"
)

const SupabaseVersion = "0.8.2"

var supabaseComponents = []string{
	"api-gateway",
	"auth",
	"database",
	"edge-runtime",
	"image-proxy",
	"pooler",
	"postgres-meta",
	"realtime",
	"rest",
	"storage",
	"studio",
}

var supabaseSecretKeys = []string{
	"anon-key",
	"auth-database-url",
	"dashboard-password",
	"dashboard-username",
	"database-owner-password",
	"database-role-bootstrap",
	"database-tls-certificate",
	"envoy-runtime-config",
	"gateway-tls-certificate",
	"jwt-secret",
	"jwt-signing-keys",
	"jwt-verification-keys",
	"pg-meta-crypto-key",
	"postgres-meta-database-password",
	"publishable-key",
	"realtime-database-password",
	"realtime-db-encryption-key",
	"rest-database-url",
	"secret-key-base",
	"service-role-key",
	"secret-key",
	"storage-s3-access-key",
	"storage-s3-secret-key",
	"storage-database-url",
	"supavisor-database-url",
	"vault-encryption-key",
}

var supabaseStorageKeys = []string{
	"database",
	"database-encryption",
	"edge-functions",
	"objects",
	"studio-snippets",
}

func SupabaseComponentNames() []string      { return append([]string(nil), supabaseComponents...) }
func SupabaseRequiredSecretKeys() []string  { return append([]string(nil), supabaseSecretKeys...) }
func SupabaseRequiredStorageKeys() []string { return append([]string(nil), supabaseStorageKeys...) }

var supabaseResourceMinimums = map[string]Resources{
	"database": {CPU: "500m", Memory: "2Gi"},
	"pooler":   {CPU: "250m", Memory: "512Mi"},
	"realtime": {CPU: "250m", Memory: "512Mi"},
}

type SupabaseConfig struct {
	PublicURL             string           `json:"public_url" toml:"public_url"`
	SiteURL               string           `json:"site_url" toml:"site_url"`
	RedirectURLs          []string         `json:"redirect_urls,omitempty" toml:"redirect_urls"`
	DatabaseName          string           `json:"database_name" toml:"database_name"`
	JWTExpirySeconds      int              `json:"jwt_expiry_seconds" toml:"jwt_expiry_seconds"`
	RESTMaxRows           int              `json:"rest_max_rows" toml:"rest_max_rows"`
	StorageFileLimitBytes int64            `json:"storage_file_limit_bytes" toml:"storage_file_limit_bytes"`
	PoolSize              int              `json:"pool_size" toml:"pool_size"`
	PoolMaxClients        int              `json:"pool_max_clients" toml:"pool_max_clients"`
	EmailSignup           bool             `json:"email_signup" toml:"email_signup"`
	AnonymousSignup       bool             `json:"anonymous_signup" toml:"anonymous_signup"`
	SMTPSecret            *SecretReference `json:"smtp_secret,omitempty" toml:"smtp_secret"`
}

func (s Spec) ValidateSupabase() error {
	if s.Kind != "supabase" || s.Version != SupabaseVersion || s.Supabase == nil {
		return fmt.Errorf("Supabase requires the immutable %s platform release", SupabaseVersion)
	}
	if err := s.ValidateResources(supabaseComponents); err != nil {
		return err
	}
	for name, minimum := range supabaseResourceMinimums {
		actual := s.Resources[name]
		actualCPU, minimumCPU := resource.MustParse(actual.CPU), resource.MustParse(minimum.CPU)
		actualMemory, minimumMemory := resource.MustParse(actual.Memory), resource.MustParse(minimum.Memory)
		if actualCPU.Cmp(minimumCPU) < 0 || actualMemory.Cmp(minimumMemory) < 0 {
			return fmt.Errorf("resources.%s requires at least %s CPU and %s memory", name, minimum.CPU, minimum.Memory)
		}
	}
	if err := s.ValidateStorage(supabaseStorageKeys); err != nil {
		return err
	}
	if err := s.ValidateSecrets(supabaseSecretKeys); err != nil {
		return err
	}
	if err := s.Placement.Validate("standalone", 1); err != nil {
		return err
	}
	if len(s.Placement.NodeNames) != 1 {
		return fmt.Errorf("Supabase requires exactly one qualified node so shared ReadWriteOnce claims cannot multi-attach")
	}
	c := s.Supabase
	if err := ValidateHTTPSOrigin(c.PublicURL, "public_url"); err != nil {
		return err
	}
	if err := ValidateHTTPSOrigin(c.SiteURL, "site_url"); err != nil {
		return err
	}
	if len(c.RedirectURLs) > 32 {
		return fmt.Errorf("redirect_urls may contain at most 32 exact HTTPS origins")
	}
	seen := map[string]bool{}
	for _, value := range c.RedirectURLs {
		if err := ValidateHTTPSOrigin(value, "redirect_urls"); err != nil {
			return err
		}
		if seen[value] {
			return fmt.Errorf("redirect_urls must not contain duplicates")
		}
		seen[value] = true
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(c.DatabaseName) {
		return fmt.Errorf("database_name must contain 1-63 lowercase letters, digits or underscores and begin with a letter")
	}
	if c.JWTExpirySeconds < 300 || c.JWTExpirySeconds > 86400 {
		return fmt.Errorf("jwt_expiry_seconds must be between 300 and 86400")
	}
	if c.RESTMaxRows < 1 || c.RESTMaxRows > 10000 {
		return fmt.Errorf("rest_max_rows must be between 1 and 10000")
	}
	if c.StorageFileLimitBytes < 1<<20 || c.StorageFileLimitBytes > 5<<30 {
		return fmt.Errorf("storage_file_limit_bytes must be between 1 MiB and 5 GiB")
	}
	if c.PoolSize < 1 || c.PoolSize > 100 || c.PoolMaxClients < c.PoolSize || c.PoolMaxClients > 1000 {
		return fmt.Errorf("pool_size must be 1-100 and pool_max_clients must be between pool_size and 1000")
	}
	if c.EmailSignup || c.SMTPSecret != nil {
		return fmt.Errorf("email signup remains unavailable until bounded SMTP egress is implemented")
	}
	return nil
}

func PlanSupabase(s Spec, images map[string]string) (Plan, error) {
	if err := s.Validate(); err != nil {
		return Plan{}, err
	}
	if s.Kind != "supabase" {
		return Plan{}, fmt.Errorf("Supabase planning requires a Supabase platform")
	}
	if err := ValidateImages(images, supabaseComponents); err != nil {
		return Plan{}, err
	}
	components := make([]Component, 0, len(supabaseComponents))
	add := func(name string, ports []int32, secrets, storage []string) {
		components = append(components, Component{
			Name:        name,
			Image:       images[name],
			Resources:   s.Resources[name],
			Replicas:    1,
			Ports:       ports,
			SecretKeys:  append([]string(nil), secrets...),
			StorageKeys: append([]string(nil), storage...),
		})
	}
	add("api-gateway", []int32{8443}, []string{"envoy-runtime-config", "gateway-tls-certificate"}, nil)
	add("auth", []int32{9999}, []string{"auth-database-url", "jwt-secret", "jwt-signing-keys"}, nil)
	add("database", []int32{5432}, []string{"database-owner-password", "database-role-bootstrap", "database-tls-certificate"}, []string{"database", "database-encryption"})
	add("edge-runtime", []int32{9000}, []string{"anon-key", "jwt-secret", "jwt-verification-keys", "publishable-key", "secret-key", "service-role-key"}, []string{"edge-functions"})
	add("image-proxy", []int32{5001}, nil, []string{"objects"})
	add("pooler", []int32{4000, 5432, 6543}, []string{"jwt-secret", "secret-key-base", "supavisor-database-url", "vault-encryption-key"}, nil)
	add("postgres-meta", []int32{8080}, []string{"pg-meta-crypto-key", "postgres-meta-database-password"}, nil)
	add("realtime", []int32{4000}, []string{"anon-key", "jwt-secret", "jwt-verification-keys", "realtime-database-password", "realtime-db-encryption-key", "secret-key-base"}, nil)
	add("rest", []int32{3000}, []string{"jwt-verification-keys", "rest-database-url"}, nil)
	add("storage", []int32{5000}, []string{"anon-key", "jwt-secret", "jwt-verification-keys", "service-role-key", "storage-database-url", "storage-s3-access-key", "storage-s3-secret-key"}, []string{"objects"})
	add("studio", []int32{3000}, []string{"anon-key", "jwt-secret", "pg-meta-crypto-key", "postgres-meta-database-password", "publishable-key", "secret-key", "service-role-key"}, []string{"edge-functions", "studio-snippets"})
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	return Plan{
		Namespace:     "managed-platform-" + s.Name,
		Components:    components,
		PublicService: "api-gateway",
		Capability: Capability{
			Available:        false,
			ClusterQualified: false,
			PublicQualified:  false,
			Reason:           "Supabase API, durable store, Kubernetes reconciliation, backup and native acceptance are not yet complete",
		},
	}, nil
}
