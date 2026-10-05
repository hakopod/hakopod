package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	catalog "github.com/hakopod/hakopod/templates"
	"github.com/pelletier/go-toml/v2"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Templates are ordinary bounded application specifications. Catalog metadata
// distinguishes registry architecture support from actual runtime verification.
type TemplateConfigField struct {
	Name        string                   `json:"name"`
	Label       string                   `json:"label"`
	Description string                   `json:"description"`
	Default     string                   `json:"default"`
	Required    bool                     `json:"required"`
	Options     []TemplateConfigOption   `json:"options,omitempty"`
	When        *TemplateConfigCondition `json:"when,omitempty"`
}

type TemplateConfigOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type TemplateConfigCondition struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type Template struct {
	LogoBackground string                `json:"logo_background,omitempty"`
	Logo           string                `json:"logo,omitempty"`
	ConfigFields   []TemplateConfigField `json:"config_fields"`

	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Category             string                `json:"category"`
	Description          string                `json:"description"`
	License              string                `json:"license"`
	Upstream             string                `json:"upstream"`
	RequiredSecrets      []string              `json:"required_secrets"`
	Requirements         []string              `json:"requirements"`
	Architectures        []string              `json:"architectures"`
	ResourceSummary      string                `json:"resource_summary"`
	Verification         string                `json:"verification"`
	Providers            []string              `json:"providers"`
	Configuration        string                `json:"configuration"`
	SiteURLRequired      bool                  `json:"site_url_required"`
	Deployable           bool                  `json:"deployable"`
	SiteURLSupported     bool                  `json:"site_url_supported"`
	DatabaseConfig       bool                  `json:"database_config"`
	SecretFields         []TemplateSecretField `json:"secret_fields"`
	Sources              []string              `json:"sources"`
	WorkloadRequirements []string              `json:"workload_requirements"`
}

// Templates returns a fresh copy so callers cannot mutate the embedded catalog.
func Templates() []Template {
	var items []Template
	data, err := catalog.Files.ReadFile("catalog.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &items); err != nil {
		panic(err)
	}
	for i := range items {
		if items[i].ConfigFields == nil {
			items[i].ConfigFields = []TemplateConfigField{}
		}
		items[i].WorkloadRequirements = templateWorkloadRequirements(items[i].ID)
	}
	return append(items, Template{ID: "managed-actions", Name: "Managed Actions", Category: "automation", Description: "Organization or repository GitHub Actions runners with isolated Docker support and single-job workspaces.", License: "Pro feature; GitHub Actions runner MIT", Upstream: "https://github.com/actions/runner", RequiredSecrets: []string{"github-runner-token"}, Requirements: []string{"Pro access", "Managed Actions sandbox installed", "A GitHub credential with organization Self-hosted runners or repository Administration read/write permission, plus repository Actions read permission for job details and logs", "A dedicated application; each replica is one concurrent job slot"}, Architectures: []string{"amd64", "arm64"}, ResourceSummary: "At least 4 GiB per runner including Docker and temporary storage", Verification: "Managed Actions runtime prerequisites are checked by this installation", Deployable: true, ConfigFields: []TemplateConfigField{{Name: "organization", Label: "GitHub organization", Description: "Choose an organization or a repository"}, {Name: "repository", Label: "GitHub repository", Description: "Optional alternative to organization"}, {Name: "runner_group_id", Label: "Organization runner group ID", Description: "Optional; uses the GitHub default group"}, {Name: "labels", Label: "Runner labels", Default: "hakopod"}, {Name: "replicas", Label: "Concurrent jobs", Default: "1"}, {Name: "credential", Label: "Application secret", Default: "github-runner-token"}}, SecretFields: []TemplateSecretField{{Name: "github-runner-token", Description: "GitHub fine-grained token: organization Self-hosted runners or repository Administration read and write, plus repository Actions read-only", Format: "provider-key"}}, Providers: []string{}, Sources: []string{"https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/about-self-hosted-runners"}, WorkloadRequirements: []string{"managed_actions"}})
}

// Describe requirements from the embedded workload, not marketing text. Editions
// can explain unsupported compute choices before users fill out a deployment form.
func templateWorkloadRequirements(id string) []string {
	data, err := catalog.Files.ReadFile("blueprints/" + id + "/hakopod.toml")
	var app Application
	if err != nil || toml.Unmarshal(data, &app) != nil {
		return []string{}
	}
	required := map[string]bool{}
	if len(app.Services) > 1 {
		required["multiple_services"] = true
	}
	if len(app.Volumes) > 0 {
		required["persistent_storage"] = true
	}
	for _, volume := range app.Volumes {
		if volume.AccessMode == "ReadWriteMany" {
			required["shared_storage"] = true
		}
	}
	for _, n := range app.Networks {
		if n.VirtualNetwork != "" {
			required["virtual_networks"] = true
		}
	}
	for _, s := range app.Services {
		for capability, needed := range map[string]bool{
			"persistent_storage": s.Volume != nil || len(s.Mounts) > 0,
			"larger_service":     s.Size != "" && s.Size != "small",
			"multiple_replicas":  s.Replicas > 1,
			"jobs":               s.Job != nil,
			"autoscaling":        s.Autoscaling != nil,
			"public_tcp":         len(s.PublicTCP) > 0,
			"certificate_mounts": len(s.CertificateMounts) > 0,
			"cloud_identity":     s.AWSIdentity != "",
			"gpu":                s.GPU != nil,
			"service_bindings":   len(s.Bindings) > 0,
			"custom_networking":  s.NetworkAccess != nil,
			"custom_readiness":   s.Readiness != nil,
		} {
			if needed {
				required[capability] = true
			}
		}
		for _, ref := range s.Secrets {
			if ref.Provider != "" {
				required["external_secrets"] = true
			}
		}
	}
	for _, ref := range app.Secrets {
		if ref.Provider != "" {
			required["external_secrets"] = true
		}
	}
	result := []string{}
	for key := range required {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}/[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)
var modelRevisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var providerModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:@+-]{0,199}$`)

type TemplateOptions struct {
	Size          string            `json:"size,omitempty"`
	Values        map[string]string `json:"values,omitempty"`
	Name          string            `json:"name"`
	Public        bool              `json:"public"`
	StorageGiB    int64             `json:"storage_gib"`
	Architecture  string            `json:"architecture"`
	Model         string            `json:"model"`
	ModelRevision string            `json:"model_revision"`
	SiteURL       string            `json:"site_url"`
	Provider      string            `json:"provider"`
	ProviderURL   string            `json:"provider_url"`
	DatabaseName  string            `json:"database_name"`
	DatabaseUser  string            `json:"database_user"`
	UseModelToken bool              `json:"use_model_token"`
}

// FromTemplate preserves the small Go caller interface. Templates requiring a
// site URL or external provider configuration use PlanTemplate explicitly.
func FromTemplate(id, name string, public bool, storage int64, model, modelRevision string) (Application, error) {
	return PlanTemplate(id, TemplateOptions{Name: name, Public: public, StorageGiB: storage, Model: model, ModelRevision: modelRevision})
}

func PlanTemplate(id string, o TemplateOptions) (Application, error) {
	if id == "managed-actions" {
		for key := range o.Values {
			if key != "organization" && key != "runner_group_id" && key != "repository" && key != "labels" && key != "replicas" && key != "credential" {
				return Application{}, fmt.Errorf("unknown runner pool field: %s", key)
			}
		}
		replicas := int64(1)
		var err error
		if value := o.Values["replicas"]; value != "" {
			replicas, err = strconv.ParseInt(value, 10, 32)
			if err != nil || replicas < 1 || replicas > 10 {
				return Application{}, fmt.Errorf("replicas: choose 1–10 concurrent jobs")
			}
		}
		group := int64(0)
		if value := o.Values["runner_group_id"]; value != "" {
			group, err = strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Application{}, fmt.Errorf("runner_group_id: use a nonnegative integer")
			}
		}
		credential := o.Values["credential"]
		if credential == "" {
			credential = "github-runner-token"
		}
		labels := []string{"hakopod"}
		if value := o.Values["labels"]; value != "" {
			labels = strings.Split(value, ",")
			for i := range labels {
				labels[i] = strings.TrimSpace(labels[i])
			}
		}
		if o.Public {
			return Application{}, fmt.Errorf("Managed Actions runners do not expose public ports")
		}
		return Normalize(Application{SchemaVersion: 1, Name: o.Name, Services: map[string]Service{"runner": {Image: ActionsRunnerImage, Size: o.Size, Architecture: o.Architecture, Replicas: int32(replicas), Actions: &Actions{Organization: o.Values["organization"], RunnerGroupID: group, Repository: o.Values["repository"], Credential: credential, Labels: labels}}}})
	}
	if o.StorageGiB == 0 {
		o.StorageGiB = 5
	}
	if o.StorageGiB < 1 || o.StorageGiB > 200 {
		return Application{}, fmt.Errorf("storage_gib: must be between 1 and 200")
	}
	if o.Architecture != "" && o.Architecture != "amd64" && o.Architecture != "arm64" {
		return Application{}, fmt.Errorf("architecture: must be amd64, arm64, or empty for automatic scheduling")
	}
	var template *Template
	for _, t := range Templates() {
		if t.ID == id {
			template = &t
			break
		}
	}
	if template == nil {
		return Application{}, fmt.Errorf("unknown template")
	}
	if !template.Deployable {
		return Application{}, fmt.Errorf("%s is a setup guide: %s", template.Name, strings.Join(template.Requirements, "; "))
	}
	if o.UseModelToken && id != "vllm" {
		return Application{}, fmt.Errorf("model access tokens are supported by the vLLM template")
	}
	if o.DatabaseName != "" || o.DatabaseUser != "" {
		if !template.DatabaseConfig {
			return Application{}, fmt.Errorf("database identity settings are available for PostgreSQL and MySQL")
		}
	}
	if o.DatabaseName == "" {
		o.DatabaseName = "app"
	}
	if o.DatabaseUser == "" {
		o.DatabaseUser = "hakopod"
	}
	if !databaseIdentifier.MatchString(o.DatabaseName) || !databaseIdentifier.MatchString(o.DatabaseUser) || id == "mysql" && o.DatabaseUser == "root" {
		return Application{}, fmt.Errorf("database and user names must start with a lowercase letter and contain up to 32 lowercase letters, numbers or underscores; MySQL application users cannot be root")
	}
	if o.SiteURL != "" && !template.SiteURLSupported {
		return Application{}, fmt.Errorf("this template does not use a canonical site URL")
	}
	if template.SiteURLRequired || o.SiteURL != "" {
		var err error
		o.SiteURL, err = templateURL(o.SiteURL)
		if err != nil {
			return Application{}, fmt.Errorf("site_url: %w", err)
		}
		parsed, _ := url.Parse(o.SiteURL)
		if parsed.Path != "" || !ValidHostname(parsed.Hostname()) {
			return Application{}, fmt.Errorf("site_url must be an HTTPS origin with a lowercase DNS hostname and no path")
		}
	}
	if o.Architecture != "" && !slices.Contains(template.Architectures, o.Architecture) {
		return Application{}, fmt.Errorf("template does not support the selected architecture")
	}
	values := map[string]string{}
	for _, field := range template.ConfigFields {
		value, exists := o.Values[field.Name]
		if !exists {
			value = field.Default
		}
		if len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, "{{") {
			return Application{}, fmt.Errorf("%s must be one line, at most 2048 characters", field.Label)
		}
		values[field.Name] = value
	}
	for key := range o.Values {
		if _, ok := values[key]; !ok {
			return Application{}, fmt.Errorf("unknown configuration field %q", key)
		}
	}
	// Preserve existing callers that supplied a shared class before storage modes existed.
	if id == "mathesar" {
		if _, explicit := o.Values["media-storage-mode"]; !explicit && values["media-storage-class"] != "" {
			values["media-storage-mode"] = "shared"
		}
	}
	// Resolve defaults before checking conditions so catalog field order does not
	// affect which fields are required. Inactive drafts never alter the workload.
	activeValues := make(map[string]string, len(values))
	for _, field := range template.ConfigFields {
		if field.When != nil && values[field.When.Field] != field.When.Value {
			continue
		}
		value := values[field.Name]
		if field.Required && strings.TrimSpace(value) == "" {
			return Application{}, fmt.Errorf("%s is required", field.Label)
		}
		if len(field.Options) > 0 && !slices.ContainsFunc(field.Options, func(option TemplateConfigOption) bool { return option.Value == value }) {
			return Application{}, fmt.Errorf("%s: choose a listed option", field.Label)
		}
		activeValues[field.Name] = value
	}
	values = activeValues
	if id == "baserow" || id == "xem" && values["storage-mode"] == "external" {
		endpoint, err := templateURL(values["storage-endpoint"])
		if err != nil {
			return Application{}, fmt.Errorf("storage-endpoint: %w", err)
		}
		parsed, _ := url.Parse(endpoint)
		if parsed.Path != "" || !ValidHostname(parsed.Hostname()) {
			return Application{}, fmt.Errorf("storage-endpoint must be an HTTPS origin without a bucket path")
		}
		values["storage-endpoint"] = endpoint
		bucket := values["storage-bucket"]
		if len(bucket) < 3 || len(bucket) > 63 || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`).MatchString(bucket) || strings.Contains(bucket, "..") || strings.Contains(bucket, ".-") || strings.Contains(bucket, "-.") {
			return Application{}, fmt.Errorf("storage-bucket must be a valid 3–63 character S3 bucket name")
		}
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(values["storage-region"]) {
			return Application{}, fmt.Errorf("storage-region must be a provider region such as us-east-1 or auto")
		}
	}
	if id == "xem" {
		address := values["admin-email"]
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Address != address || len(address) > 254 {
			return Application{}, fmt.Errorf("admin-email: use a single email address without a display name")
		}
		for _, key := range []string{"admin-name", "team-name"} {
			if len(values[key]) > 128 || strings.TrimSpace(values[key]) != values[key] {
				return Application{}, fmt.Errorf("%s: use up to 128 characters without surrounding whitespace", key)
			}
		}
	}
	data, err := catalog.Files.ReadFile("blueprints/" + id + "/hakopod.toml")
	if err != nil {
		return Application{}, fmt.Errorf("template source unavailable: %w", err)
	}
	var app Application
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&app); err != nil {
		return Application{}, fmt.Errorf("template source: %w", err)
	}
	if o.Size != "" {
		if _, ok := Profiles[o.Size]; !ok {
			return Application{}, fmt.Errorf("size: select a known service profile")
		}
	}
	app.Name = o.Name
	for name, service := range app.Services {
		if o.Size != "" {
			service.Size = o.Size
		}
		service.Architecture = o.Architecture
		service.Public = service.Public && o.Public
		if service.Volume != nil {
			service.Volume.SizeGiB = o.StorageGiB
			if id == "vllm" && o.StorageGiB == 5 {
				service.Volume.SizeGiB = 30
			}
		}
		app.Services[name] = service
	}
	for name, volume := range app.Volumes {
		volume.SizeGiB = o.StorageGiB
		// Shared storage classes are installation-specific. Resolve declared
		// configuration as data and let Normalize validate the Kubernetes name.
		for key, value := range values {
			volume.StorageClass = strings.ReplaceAll(volume.StorageClass, "{{config."+key+"}}", value)
		}
		app.Volumes[name] = volume
	}
	if id == "mathesar" && values["media-storage-mode"] == "shared" {
		media := app.Volumes["media"]
		media.AccessMode = "ReadWriteMany"
		media.StorageClass = values["media-storage-class"]
		app.Volumes["media"] = media
	}
	main := app.Services["main"]
	switch id {
	case "outpost":
		if err := configureOutpostTemplate(&app, values); err != nil {
			return Application{}, err
		}
		main = app.Services["main"]
	case "mathesar", "xem":
		if values["database-mode"] == "external" {
			host := values["database-host"]
			if len(validation.IsDNS1123Subdomain(host)) != 0 && net.ParseIP(host) == nil {
				return Application{}, fmt.Errorf("database-host: use a hostname or IP address without a scheme, port or credentials")
			}
			port, err := strconv.Atoi(values["database-port"])
			if err != nil || port < 1 || port > 65535 {
				return Application{}, fmt.Errorf("database-port: use a port between 1 and 65535")
			}
			for _, key := range []string{"database-name", "database-user"} {
				if value := values[key]; len(value) > 63 || strings.TrimSpace(value) != value {
					return Application{}, fmt.Errorf("%s: use up to 63 characters without surrounding whitespace", key)
				}
			}
			backend := app.Services["backend"]
			backend.DependsOn = slices.DeleteFunc(backend.DependsOn, func(name string) bool { return name == "db" })
			backend.Env["POSTGRES_HOST"] = host
			backend.Env["POSTGRES_PORT"] = strconv.Itoa(port)
			backend.Env["POSTGRES_DB"] = values["database-name"]
			backend.Env["POSTGRES_USER"] = values["database-user"]
			backend.Env["POSTGRES_SSLMODE"] = values["database-sslmode"]
			if id == "mathesar" {
				// Restore Mathesar's overwritten TLS options and give direct
				// libpq connections the same policy.
				backend.Env["PGSSLMODE"] = values["database-sslmode"]
				if values["database-sslmode"] == "verify-full" {
					backend.Env["PGSSLROOTCERT"] = "/etc/ssl/certs/ca-certificates.crt"
				}
			}
			app.Services["backend"] = backend
			delete(app.Services, "db")
		}
		if id == "xem" && values["redis-mode"] == "external" {
			host := values["redis-host"]
			if len(validation.IsDNS1123Subdomain(host)) != 0 && net.ParseIP(host) == nil {
				return Application{}, fmt.Errorf("redis-host: use a hostname or IP address without a scheme, port or credentials")
			}
			port, err := strconv.Atoi(values["redis-port"])
			if err != nil || port < 1 || port > 65535 {
				return Application{}, fmt.Errorf("redis-port: use a port between 1 and 65535")
			}
			database, err := strconv.Atoi(values["redis-db"])
			if err != nil || database < 0 || database > 15 {
				return Application{}, fmt.Errorf("redis-db: use a database number between 0 and 15")
			}
			if user := values["redis-username"]; len(user) > 256 || strings.TrimSpace(user) != user {
				return Application{}, fmt.Errorf("redis-username: use up to 256 characters without surrounding whitespace")
			}
			backend := app.Services["backend"]
			backend.DependsOn = slices.DeleteFunc(backend.DependsOn, func(name string) bool { return name == "redis" })
			// Xem concatenates host and port instead of using net.JoinHostPort.
			if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
				host = "[" + host + "]"
			}
			backend.Env["REDIS_HOST"] = host
			backend.Env["REDIS_PORT"] = strconv.Itoa(port)
			backend.Env["REDIS_USERNAME"] = values["redis-username"]
			backend.Env["REDIS_DB"] = strconv.Itoa(database)
			backend.Env["REDIS_USE_TLS"] = values["redis-tls"]
			app.Services["backend"] = backend
			delete(app.Services, "redis")
		}
		if id == "xem" && values["storage-mode"] == "external" {
			backend := app.Services["backend"]
			backend.DependsOn = slices.DeleteFunc(backend.DependsOn, func(name string) bool { return name == "storage" })
			backend.Env["S3_BUCKET_NAME"] = values["storage-bucket"]
			backend.Env["S3_ENDPOINT_URL"] = values["storage-endpoint"]
			backend.Env["S3_REGION"] = values["storage-region"]
			delete(backend.Env, "S3_PUBLIC_ENDPOINT_URL")
			delete(backend.Env, "S3_CREATE_BUCKET")
			delete(backend.Env, "S3_ACCESS_KEY")
			backend.Secrets["S3_ACCESS_KEY"] = SecretRef{Ref: "storage-access-key"}
			backend.Secrets["S3_SECRET_KEY"] = SecretRef{Ref: "storage-secret-key"}
			app.Services["backend"] = backend
			delete(app.Services, "storage")
			delete(main.Files, "storage")
			main.DependsOn = slices.DeleteFunc(main.DependsOn, func(name string) bool { return name == "storage" })
		}
	case "postgresql":
		main.Env["POSTGRES_USER"] = o.DatabaseUser
		main.Env["POSTGRES_DB"] = o.DatabaseName
	case "mysql":
		main.Env["MYSQL_USER"] = o.DatabaseUser
		main.Env["MYSQL_DATABASE"] = o.DatabaseName
	case "open-webui":
		provider := o.Provider
		if provider == "" {
			provider = "openai"
		}
		endpoint := "https://api.openai.com/v1"
		switch provider {
		case "openai":
			if o.ProviderURL != "" && o.ProviderURL != endpoint {
				return Application{}, fmt.Errorf("choose openai-compatible to use a custom provider_url")
			}
		case "openai-compatible":
			endpoint, err = templateURL(o.ProviderURL)
			if err != nil {
				return Application{}, fmt.Errorf("provider_url: %w", err)
			}
		default:
			return Application{}, fmt.Errorf("provider must be openai or openai-compatible")
		}
		if !providerModelPattern.MatchString(o.Model) {
			return Application{}, fmt.Errorf("a provider model identifier is required")
		}
		main.Env["OPENAI_API_BASE_URL"] = endpoint
		main.Env["DEFAULT_MODELS"] = o.Model
	case "vllm":
		if !modelPattern.MatchString(o.Model) || !modelRevisionPattern.MatchString(o.ModelRevision) {
			return Application{}, fmt.Errorf("a Hugging Face owner/model and immutable 40-character model revision are required")
		}
		main.Args[0], main.Args[2] = o.Model, o.ModelRevision
		if o.UseModelToken {
			main.Secrets["HF_TOKEN"] = SecretRef{Ref: "model-token"}
		}
	}
	if _, ok := app.Services["main"]; ok {
		app.Services["main"] = main
	}
	for name, service := range app.Services {
		for key, value := range service.Env {
			for name, replacement := range values {
				value = strings.ReplaceAll(value, "{{config."+name+"}}", replacement)
			}
			if strings.Contains(value, "{{site_host}}") {
				parsed, _ := url.Parse(o.SiteURL)
				value = strings.ReplaceAll(value, "{{site_host}}", parsed.Host)
			}
			if strings.Contains(value, "{{site_hostname}}") {
				parsed, _ := url.Parse(o.SiteURL)
				value = strings.ReplaceAll(value, "{{site_hostname}}", parsed.Hostname())
			}
			service.Env[key] = value
			if strings.Contains(value, "https://catalog.example.test") {
				if o.SiteURL == "" {
					delete(service.Env, key)
				} else {
					service.Env[key] = strings.ReplaceAll(value, "https://catalog.example.test", o.SiteURL)
				}
			}
		}
		app.Services[name] = service
	}
	return Normalize(app)
}

func templateURL(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(value) > 1024 || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("use an absolute HTTPS URL without credentials, query or fragment")
	}
	if port := u.Port(); port != "" || strings.HasSuffix(u.Host, ":") {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("URL port must be between 1 and 65535")
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func TemplateSecretNames(a Application) []string {
	refs := map[string]bool{}
	for _, s := range a.Services {
		if s.Actions != nil {
			refs[s.Actions.Credential] = true
			if s.Actions.JobsCredential != "" {
				refs[s.Actions.JobsCredential] = true
			}
			if s.Actions.Cache != nil && s.Actions.Cache.Credential != "" {
				refs[s.Actions.Cache.Credential] = true
			}
		}
		for _, r := range SecretReferences(EffectiveService(a, s)) {
			refs[r.Ref] = true
		}
	}
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}
