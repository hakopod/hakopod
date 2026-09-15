package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	catalog "github.com/hakopod/hakopod/templates"
	"github.com/pelletier/go-toml/v2"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Templates are ordinary bounded application specifications. Catalog metadata
// distinguishes registry architecture support from actual runtime verification.
type TemplateConfigField struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
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
	return items
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
	if o.StorageGiB == 0 {
		o.StorageGiB = 5
	}
	if o.StorageGiB < 1 || o.StorageGiB > 200 {
		return Application{}, fmt.Errorf("storage_gib must be between 1 and 200")
	}
	if o.Architecture != "" && o.Architecture != "amd64" && o.Architecture != "arm64" {
		return Application{}, fmt.Errorf("architecture must be amd64, arm64, or empty for automatic scheduling")
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
		if field.Required && strings.TrimSpace(value) == "" {
			return Application{}, fmt.Errorf("%s is required", field.Label)
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
	data, err := catalog.Files.ReadFile("blueprints/" + id + "/hakopod.toml")
	if err != nil {
		return Application{}, fmt.Errorf("template source unavailable: %w", err)
	}
	var app Application
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&app); err != nil {
		return Application{}, fmt.Errorf("template source: %w", err)
	}
	app.Name = o.Name
	for name, service := range app.Services {
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
		app.Volumes[name] = volume
	}
	main := app.Services["main"]
	switch id {
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
	return strings.TrimRight(u.String(), "/"), nil
}
func TemplateSecretNames(a Application) []string {
	refs := map[string]bool{}
	for _, s := range a.Services {
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
