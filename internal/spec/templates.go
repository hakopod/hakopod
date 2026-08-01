package spec

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Templates are ordinary bounded application specifications. Catalog metadata
// distinguishes registry architecture support from actual runtime verification.
type Template struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Category        string   `json:"category"`
	Description     string   `json:"description"`
	License         string   `json:"license"`
	Upstream        string   `json:"upstream"`
	RequiredSecrets []string `json:"required_secrets"`
	Requirements    []string `json:"requirements"`
	Architectures   []string `json:"architectures"`
	ResourceSummary string   `json:"resource_summary"`
	Verification    string   `json:"verification"`
	Providers       []string `json:"providers"`
	Configuration   string   `json:"configuration"`
	SiteURLRequired bool     `json:"site_url_required"`
	Deployable      bool     `json:"deployable"`
}

const (
	postgresImage   = "docker.io/library/postgres:17.11-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73"
	valkeyImage     = "docker.io/valkey/valkey:9.1.2-alpine@sha256:a0dbf4c1d5708782907c10e2c72deff317518518b5288a58416981d9db95d30b"
	redisImage      = "docker.io/library/redis:8.6.6-alpine@sha256:75934ddb37bfaebe3b4082ba673cac39f66495244134f33dd0a502ce03cdcd36"
	mysqlImage      = "docker.io/library/mysql:8.4.11@sha256:85b9bf2e29cf836ecb8c2a15a935d4ba0c606631dff1dd79531a11983c638f2a"
	cockroachImage  = "docker.io/cockroachdb/cockroach:v25.4.16@sha256:d505f1e479cd4a4305acdfa3abf435298469058d6745cf5dde56b0a239da22fa"
	clickhouseImage = "docker.io/clickhouse/clickhouse-server:26.3.33.24@sha256:810861a2e2d0188744f5f23b2d3ec9ff95812bcb9ddbb8fed13a377a7f305893"
	metabaseImage   = "docker.io/metabase/metabase:v0.63.17@sha256:918187453bd5d70849d14bb6af0bb482a617dc849f251502679a8e626d98f4ee"
	infisicalImage  = "docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266"
	openWebUIImage  = "ghcr.io/open-webui/open-webui:v0.11.3-slim@sha256:bb3633af77b35d97783affc9cd8097d8a6dc89fedd5a410ce9a50d85556a870c"
	flowiseImage    = "docker.io/flowiseai/flowise:3.1.4@sha256:3922767afb52a5777759fd8b28a3c9eee864daea96018a791f2429eae2a76571"
	kumaImage       = "docker.io/louislam/uptime-kuma:2.5.4@sha256:917318f9d7be5257f43ba412c766a473be336eb451d70744f3b482d0c3997c0e"
	giteaImage      = "docker.io/gitea/gitea:1.27.3-rootless@sha256:1c17ecaead42eb3b5391553d8708103a4beb0e86edf5b9ebc1eb269c318845f2"
	vllmImage       = "docker.io/vllm/vllm-openai:v0.29.0@sha256:c2914767605584b6d8f45686b82de173ecc99e781897aa3d0a66dacd72c51ae1"
)

func Templates() []Template {
	items := []Template{
		{ID: "postgresql", Name: "PostgreSQL", Category: "database", Description: "Persistent relational database on a private endpoint.", License: "PostgreSQL", Upstream: "https://www.postgresql.org", RequiredSecrets: []string{"database-password"}, ResourceSummary: "256 MiB requested; 512 MiB limit", Verification: "ARM64 startup, SQL and persistent restart verified", Requirements: []string{"Persistent storage; one replica", "Configure separate database backups"}},
		{ID: "valkey", Name: "Valkey", Category: "database", Description: "Private key-value database with append-only persistence.", License: "BSD-3-Clause", Upstream: "https://valkey.io", RequiredSecrets: []string{"database-password"}, ResourceSummary: "128 MiB requested; 256 MiB limit; 64 MiB data budget", Verification: "ARM64 authenticated Service DNS commands verified", Requirements: []string{"Persistent storage; one replica", "No eviction: writes fail when the data budget is full"}},
		{ID: "redis", Name: "Redis", Category: "database", Description: "Private authenticated Redis with append-only persistence.", License: "AGPL-3.0 / RSALv2 / SSPL-1.0 (upstream options)", Upstream: "https://redis.io", RequiredSecrets: []string{"database-password"}, ResourceSummary: "128 MiB requested; 256 MiB limit; 64 MiB data budget", Verification: "ARM64 authenticated Service DNS commands and AOF data survive persistent restart", Requirements: []string{"Review Redis 8 license options", "Persistent storage; no eviction when the data budget is full"}},
		{ID: "mysql", Name: "MySQL", Category: "database", Description: "Private MySQL 8.4 LTS database with persistent storage.", License: "GPL-2.0", Upstream: "https://www.mysql.com", RequiredSecrets: []string{"database-password", "database-root-password"}, ResourceSummary: "256 MiB requested; 512 MiB limit; 32 MiB InnoDB buffer; 12 connections", Verification: "ARM64 non-root startup, SQL dump and fresh-database restore verified", Requirements: []string{"Separate application and root password references", "Persistent storage; one replica; configure backups"}},
		{ID: "cockroachdb", Name: "CockroachDB", Category: "database", Description: "Secure single-node SQL database with operator-supplied TLS certificates.", License: "CockroachDB Software License", Upstream: "https://www.cockroachlabs.com", RequiredSecrets: []string{"database-ca", "database-node-cert", "database-node-key"}, ResourceSummary: "512 MiB requested; 1 GiB limit; 128 MiB cache and SQL budgets", Verification: "ARM64 TLS SQL rejects missing credentials; data survives persistent restart", Requirements: []string{"Review upstream license eligibility and registration", "Provide CA, node certificate and key; certificate SAN must include main and localhost", "Use a client certificate to initialize SQL users; admin HTTP is loopback only", "Single node is not highly available"}},
		{ID: "clickhouse", Name: "ClickHouse", Category: "database", Description: "Private authenticated HTTP analytics database with persistent data.", License: "Apache-2.0", Upstream: "https://clickhouse.com", RequiredSecrets: []string{"database-password"}, ResourceSummary: "512 MiB requested; 1 GiB limit; query memory capped at 256 MiB", Verification: "ARM64 authenticated HTTP queries and data survive persistent restart", Requirements: []string{"HTTP port 8123 only; native TCP is not exposed", "Persistent storage; one replica; background pools bounded"}},
		{ID: "metabase", Name: "Metabase", Category: "application", Description: "Analytics workspace with its own private PostgreSQL application database.", License: "AGPL-3.0", Upstream: "https://www.metabase.com", RequiredSecrets: []string{"database-password"}, ResourceSummary: "2.25 GiB requested; 4.5 GiB total limits; 1 GiB Java heap", Requirements: []string{"Complete administrator setup before sharing its URL", "PostgreSQL holds dashboards and settings; back it up", "Uses the open-source edition"}},
		{ID: "infisical", Name: "Infisical", Category: "application", Description: "Self-hosted secrets workspace with private PostgreSQL and Redis.", License: "MIT core; enterprise extensions have separate terms", Upstream: "https://infisical.com", RequiredSecrets: []string{"auth-secret", "database-password", "database-url", "encryption-key", "redis-password", "redis-url"}, SiteURLRequired: true, ResourceSummary: "2.375 GiB requested; 4.75 GiB total limits; 1 GiB Node heap", Requirements: []string{"Use a stable HTTPS site URL; complete administrator setup", "encryption-key must be 32 hexadecimal characters; back it up securely", "database-url must target db:5432/app; redis-url must target redis:6379 with matching passwords", "Back up PostgreSQL and encryption keys together", "This is the Infisical server; Hakopod's optional operator integration is configured separately"}},
		{ID: "open-webui", Name: "Open WebUI", Category: "agent", Description: "Self-hosted chat and tool workspace using an explicit external model provider.", License: "Open WebUI License (branding clause)", Upstream: "https://github.com/open-webui/open-webui", RequiredSecrets: []string{"provider-key", "session-secret"}, Providers: []string{"openai", "openai-compatible"}, ResourceSummary: "2 GiB requested; 4 GiB limit; no local model service", Requirements: []string{"Choose a provider, model and provider-key; inference is billed by that provider", "Complete the first administrator account before sharing its URL", "Local Ollama and local embedding downloads are disabled", "Retain upstream branding as required by its license", "Workspace and attachments use persistent storage"}},
		{ID: "flowise", Name: "Flowise", Category: "agent", Description: "Self-hosted visual agent and workflow builder with selectable model nodes.", License: "Apache-2.0 core; enterprise extensions have separate terms", Upstream: "https://github.com/FlowiseAI/Flowise", RequiredSecrets: []string{"credential-encryption-key", "session-secret", "token-hash-secret", "token-refresh-secret", "token-signing-secret"}, Providers: []string{"OpenAI", "Anthropic", "Google", "OpenAI-compatible"}, Configuration: "workspace", SiteURLRequired: true, ResourceSummary: "2 GiB requested; 4 GiB limit; 1 GiB Node heap", Requirements: []string{"Create the first administrator, then select provider/model nodes and save their credentials in Flowise", "Create or import an agent flow and protect its prediction API with an API key", "No model weights or ready-made autonomous flow are installed", "Persist SQLite, uploaded files and credential encryption keys together", "Tool execution uses this service's restricted container"}},
		{ID: "uptime-kuma", Name: "Uptime Kuma", Category: "application", Description: "Self-hosted status and uptime monitoring.", License: "MIT", Upstream: "https://github.com/louislam/uptime-kuma", ResourceSummary: "256 MiB requested; 512 MiB limit", Verification: "ARM64 startup and administrator setup page verified", Requirements: []string{"Persistent storage", "Complete administrator setup before sharing its URL"}},
		{ID: "gitea", Name: "Gitea", Category: "application", Description: "Private Git hosting with a persistent SQLite database.", License: "MIT", Upstream: "https://github.com/go-gitea/gitea", ResourceSummary: "256 MiB requested; 512 MiB limit", Verification: "ARM64 administrator setup and login survive persistent restart", Requirements: []string{"Persistent storage; HTTP Git only", "Complete administrator setup before sharing its URL"}},
		{ID: "vllm", Name: "vLLM", Category: "ai", Description: "GPU model serving through an authenticated OpenAI-compatible endpoint.", License: "Apache-2.0 engine; model license varies", Upstream: "https://github.com/vllm-project/vllm", RequiredSecrets: []string{"inference-api-key"}, ResourceSummary: "8 GiB requested; 16 GiB RAM limit; VRAM depends on model", Requirements: []string{"Compatible NVIDIA GPU, CUDA 13 driver and device plugin", "ARM64 image requires NVIDIA SBSA hardware; ordinary ARM CPU nodes are unsupported", "Review model license and access terms", "Persistent model cache; image download is about 10 GB", "GPU execution has not been verified on the local development host"}},
		{ID: "xem", Name: "xem.email", Category: "application", Description: "Email marketing workspace; requires a frontend built for your API origin.", License: "GPL-3.0", Upstream: "https://github.com/mailxem/devops", Configuration: "guide", SiteURLRequired: true, ResourceSummary: "Four services: web, API, PostgreSQL and Redis", Verification: "Official AMD64/ARM64 manifests checked; deployment blocked by build-time frontend URL", Requirements: []string{"The upstream frontend embeds NEXT_PUBLIC_API_URL at image build time", "Build and pin your frontend with your own API origin before deployment", "Provide database, Redis, JWT, session, encryption and administrator credentials", "SMTP sending needs your own provider setup; no hosted Xem or AI credentials are assumed", "See docs/templates-xem.md for the reviewed upstream pins and requirements"}},
	}
	for i := range items {
		t := &items[i]
		t.Architectures = []string{"amd64", "arm64"}
		t.Deployable = t.ID != "xem"
		if t.RequiredSecrets == nil {
			t.RequiredSecrets = []string{}
		}
		if t.Providers == nil {
			t.Providers = []string{}
		}
		if t.SiteURLRequired {
			t.Requirements = append(t.Requirements, "After deployment, open Custom domains, verify the site URL hostname, and apply routing and TLS before using login or callbacks")
		}
		if t.Configuration == "" {
			t.Configuration = "template"
		}
		if t.Verification == "" {
			t.Verification = "AMD64/ARM64 registry manifests checked; runtime acceptance pending"
		}
	}
	return items
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}/[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)
var modelRevisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var providerModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:@+-]{0,199}$`)

type TemplateOptions struct {
	Name          string
	Public        bool
	StorageGiB    int64
	Architecture  string
	Model         string
	ModelRevision string
	SiteURL       string
	Provider      string
	ProviderURL   string
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
		return Application{}, fmt.Errorf("xem requires a frontend built and pinned for your own API origin; follow docs/templates-xem.md")
	}
	if template.SiteURLRequired {
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
	main := Service{Size: "small", Replicas: 1, Public: o.Public, Volume: &Volume{MountPath: "/data", SizeGiB: o.StorageGiB}}
	services := map[string]Service{}
	switch id {
	case "postgresql":
		main = templatePostgres(o.StorageGiB, "database-password")
	case "valkey", "redis":
		main = templateRedis(id, o.StorageGiB, "database-password")
	case "mysql":
		main.Image = mysqlImage
		main.Port = 3306
		main.Public = false
		main.Size = "medium"
		main.RunAsUser = 999
		main.Volume.MountPath = "/var/lib/mysql"
		main.Env = map[string]string{"MYSQL_USER": "hakopod", "MYSQL_DATABASE": "app"}
		main.Secrets = map[string]SecretRef{"MYSQL_PASSWORD": {Ref: "database-password"}, "MYSQL_ROOT_PASSWORD": {Ref: "database-root-password"}}
		main.Args = []string{"--socket=/tmp/mysql.sock", "--pid-file=/tmp/mysql.pid", "--innodb-buffer-pool-size=32M", "--innodb-log-buffer-size=4M", "--max-connections=12", "--performance-schema=OFF", "--key-buffer-size=8M", "--temptable-max-ram=16M", "--mysqlx=0"}
	case "cockroachdb":
		main.Image = cockroachImage
		main.Port = 26257
		main.Public = false
		main.Size = "large"
		main.Volume.MountPath = "/cockroach/cockroach-data"
		main.Secrets = map[string]SecretRef{"COCKROACH_CA": {Ref: "database-ca"}, "COCKROACH_NODE_CERT": {Ref: "database-node-cert"}, "COCKROACH_NODE_KEY": {Ref: "database-node-key"}}
		main.Command = []string{"/bin/sh", "-ec"}
		main.Args = []string{`umask 077
mkdir -p /tmp/hakopod-certs
printf '%s' "$COCKROACH_CA" > /tmp/hakopod-certs/ca.crt
printf '%s' "$COCKROACH_NODE_CERT" > /tmp/hakopod-certs/node.crt
printf '%s' "$COCKROACH_NODE_KEY" > /tmp/hakopod-certs/node.key
exec /cockroach/cockroach start-single-node --certs-dir=/tmp/hakopod-certs --listen-addr=0.0.0.0:26257 --advertise-addr=main:26257 --http-addr=127.0.0.1:8080 --store=/cockroach/cockroach-data --cache=128MiB --max-sql-memory=128MiB`}
	case "clickhouse":
		main.Image = clickhouseImage
		main.Port = 8123
		main.Public = false
		main.Size = "large"
		main.RunAsUser = 101
		main.Healthcheck = "/ping"
		main.Secrets = map[string]SecretRef{"CLICKHOUSE_PASSWORD": {Ref: "database-password"}}
		main.Command = []string{"/bin/sh", "-ec"}
		main.Args = []string{clickhouseCommand}
	case "metabase":
		main.Image = metabaseImage
		main.Port = 3000
		main.Size = "compute"
		main.Healthcheck = "/api/health"
		main.RunAsUser = 2000
		main.Env = map[string]string{"MB_DB_TYPE": "postgres", "MB_DB_HOST": "db", "MB_DB_PORT": "5432", "MB_DB_DBNAME": "app", "MB_DB_USER": "hakopod", "MB_PLUGINS_DIR": "/data/plugins", "JAVA_TOOL_OPTIONS": "-Xmx1024m -XX:ActiveProcessorCount=2"}
		main.Secrets = map[string]SecretRef{"MB_DB_PASS": {Ref: "database-password"}}
		main.DependsOn = []string{"db"}
		services["db"] = templatePostgres(o.StorageGiB, "database-password")
	case "infisical":
		main.Image = infisicalImage
		main.Port = 8080
		main.Size = "compute"
		main.RunAsUser = 1001
		main.Healthcheck = "/api/status"
		main.Volume = nil
		main.Env = map[string]string{"SITE_URL": o.SiteURL, "NODE_OPTIONS": "--max-old-space-size=1024", "TELEMETRY_ENABLED": "false"}
		main.Secrets = map[string]SecretRef{"ENCRYPTION_KEY": {Ref: "encryption-key"}, "AUTH_SECRET": {Ref: "auth-secret"}, "DB_CONNECTION_URI": {Ref: "database-url"}, "REDIS_URL": {Ref: "redis-url"}}
		main.DependsOn = []string{"db", "redis"}
		services["db"] = templatePostgres(o.StorageGiB, "database-password")
		services["redis"] = templateRedis("redis", o.StorageGiB, "redis-password")
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
			var err error
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
		main.Image = openWebUIImage
		main.Port = 8080
		main.Size = "compute"
		main.Healthcheck = "/health"
		main.Volume.MountPath = "/app/backend/data"
		main.Env = map[string]string{"HOME": "/tmp", "DATA_DIR": "/app/backend/data", "ENABLE_OLLAMA_API": "false", "ENABLE_OPENAI_API": "true", "OPENAI_API_BASE_URL": endpoint, "DEFAULT_MODELS": o.Model, "DEFAULT_USER_ROLE": "pending", "RAG_EMBEDDING_ENGINE": "openai", "AUDIO_STT_ENGINE": "openai", "HF_HUB_OFFLINE": "1", "DO_NOT_TRACK": "true", "ANONYMIZED_TELEMETRY": "false"}
		main.Secrets = map[string]SecretRef{"WEBUI_SECRET_KEY": {Ref: "session-secret"}, "OPENAI_API_KEY": {Ref: "provider-key"}}
	case "flowise":
		main.Image = flowiseImage
		main.Port = 3000
		main.Size = "compute"
		main.RunAsUser = 1000
		main.Healthcheck = "/api/v1/ping"
		main.Env = map[string]string{"PORT": "3000", "HOME": "/data", "APP_URL": o.SiteURL, "DATABASE_TYPE": "sqlite", "DATABASE_PATH": "/data", "SECRETKEY_PATH": "/data", "BLOB_STORAGE_PATH": "/data/storage", "LOG_PATH": "/data/logs", "NODE_OPTIONS": "--max-old-space-size=1024", "DISABLE_FLOWISE_TELEMETRY": "true", "FLOWISE_FILE_SIZE_LIMIT": "10mb", "SECURE_COOKIES": "true"}
		main.Secrets = map[string]SecretRef{"FLOWISE_SECRETKEY_OVERWRITE": {Ref: "credential-encryption-key"}, "JWT_AUTH_TOKEN_SECRET": {Ref: "token-signing-secret"}, "JWT_REFRESH_TOKEN_SECRET": {Ref: "token-refresh-secret"}, "EXPRESS_SESSION_SECRET": {Ref: "session-secret"}, "TOKEN_HASH_SECRET": {Ref: "token-hash-secret"}}
	case "uptime-kuma":
		main.Image = kumaImage
		main.Port = 3001
		main.Size = "medium"
		main.Volume.MountPath = "/app/data"
		main.Healthcheck = "/"
		main.Env = map[string]string{"NODE_OPTIONS": "--max-old-space-size=256"}
	case "gitea":
		main.Image = giteaImage
		main.Port = 3000
		main.Size = "medium"
		main.RunAsUser = 1000
		main.Volume.MountPath = "/var/lib/gitea"
		main.Healthcheck = "/api/healthz"
		main.Env = map[string]string{"GITEA__server__DISABLE_SSH": "true", "GITEA__database__DB_TYPE": "sqlite3", "GITEA_APP_INI": "/var/lib/gitea/custom/conf/app.ini"}
	case "vllm":
		if !modelPattern.MatchString(o.Model) || !modelRevisionPattern.MatchString(o.ModelRevision) {
			return Application{}, fmt.Errorf("a Hugging Face owner/model and immutable 40-character model revision are required")
		}
		main.Image = vllmImage
		main.Port = 8000
		main.Size = "gpu"
		main.Healthcheck = "/health"
		main.GPU = &GPU{Count: 1}
		main.Volume.MountPath = "/model-cache"
		if o.StorageGiB == 5 {
			main.Volume.SizeGiB = 30
		}
		main.Env = map[string]string{"HF_HOME": "/model-cache", "HOME": "/tmp"}
		main.Secrets = map[string]SecretRef{"VLLM_API_KEY": {Ref: "inference-api-key"}}
		main.Args = []string{o.Model, "--revision", o.ModelRevision, "--download-dir", "/model-cache", "--host", "0.0.0.0", "--port", "8000", "--max-model-len", "4096", "--gpu-memory-utilization", "0.85"}
	}
	services["main"] = main
	for name, s := range services {
		s.Architecture = o.Architecture
		services[name] = s
	}
	return Normalize(Application{Name: o.Name, Services: services})
}

func templatePostgres(storage int64, password string) Service {
	return Service{Image: postgresImage, Port: 5432, Size: "medium", Replicas: 1, RunAsUser: 70, Volume: &Volume{MountPath: "/var/lib/postgresql/data", SizeGiB: storage}, Env: map[string]string{"POSTGRES_USER": "hakopod", "POSTGRES_DB": "app", "PGDATA": "/var/lib/postgresql/data/pgdata"}, Secrets: map[string]SecretRef{"POSTGRES_PASSWORD": {Ref: password}}, Args: []string{"postgres", "-c", "shared_buffers=64MB", "-c", "max_connections=50"}}
}
func templateRedis(kind string, storage int64, password string) Service {
	image, executable, variable := redisImage, "redis-server", "REDIS_PASSWORD"
	if kind == "valkey" {
		image, executable, variable = valkeyImage, "valkey-server", "VALKEY_PASSWORD"
	}
	return Service{Image: image, Port: 6379, Size: "small", Replicas: 1, RunAsUser: 999, Volume: &Volume{MountPath: "/data", SizeGiB: storage}, Command: []string{"sh", "-ec"}, Args: []string{`exec ` + executable + ` --appendonly yes --maxmemory 64mb --maxmemory-policy noeviction --requirepass "$` + variable + `"`}, Secrets: map[string]SecretRef{variable: {Ref: password}}}
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
		for _, r := range s.Secrets {
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

const clickhouseCommand = `umask 077
password_hash=$(printf '%s' "$CLICKHOUSE_PASSWORD" | sha256sum | cut -d' ' -f1)
mkdir -p /data/tmp /data/user_files /data/format_schemas /data/access
cat > /tmp/hakopod-clickhouse.xml <<EOF_CONFIG
<clickhouse>
<logger><level>warning</level><console>1</console></logger>
<http_port>8123</http_port><listen_host>0.0.0.0</listen_host>
<path>/data/</path><tmp_path>/data/tmp/</tmp_path><user_files_path>/data/user_files/</user_files_path><format_schema_path>/data/format_schemas/</format_schema_path><access_control_path>/data/access/</access_control_path>
<max_server_memory_usage>671088640</max_server_memory_usage><mark_cache_size>67108864</mark_cache_size><uncompressed_cache_size>16777216</uncompressed_cache_size>
<background_pool_size>4</background_pool_size><background_schedule_pool_size>4</background_schedule_pool_size><background_message_broker_schedule_pool_size>2</background_message_broker_schedule_pool_size><background_distributed_schedule_pool_size>2</background_distributed_schedule_pool_size><background_buffer_flush_schedule_pool_size>2</background_buffer_flush_schedule_pool_size><background_move_pool_size>2</background_move_pool_size><background_fetches_pool_size>2</background_fetches_pool_size><background_common_pool_size>2</background_common_pool_size>
<merge_tree><number_of_free_entries_in_pool_to_lower_max_size_of_merge>0</number_of_free_entries_in_pool_to_lower_max_size_of_merge><number_of_free_entries_in_pool_to_execute_mutation>0</number_of_free_entries_in_pool_to_execute_mutation><number_of_free_entries_in_pool_to_execute_optimize_entire_partition>0</number_of_free_entries_in_pool_to_execute_optimize_entire_partition></merge_tree>
<profiles><default><max_memory_usage>268435456</max_memory_usage><max_threads>2</max_threads><max_insert_threads>1</max_insert_threads></default></profiles>
<users><default><password_sha256_hex>$password_hash</password_sha256_hex><networks><ip>::/0</ip></networks><profile>default</profile><quota>default</quota><access_management>1</access_management></default></users>
<quotas><default><interval><duration>3600</duration><queries>0</queries><errors>0</errors><result_rows>0</result_rows><read_rows>0</read_rows><execution_time>0</execution_time></interval></default></quotas>
</clickhouse>
EOF_CONFIG
unset password_hash CLICKHOUSE_PASSWORD
exec clickhouse-server --config-file=/tmp/hakopod-clickhouse.xml`
