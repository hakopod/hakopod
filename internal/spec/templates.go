package spec

import (
	"fmt"
	"regexp"
)

// Catalog entries create normal specifications: no parallel orchestrator and
// no implicit containers. Images resolve to immutable digests at acceptance.
type Template struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Category        string   `json:"category"`
	Description     string   `json:"description"`
	License         string   `json:"license"`
	Upstream        string   `json:"upstream"`
	RequiredSecrets []string `json:"required_secrets"`
	Requirements    []string `json:"requirements"`
}

func Templates() []Template {
	return []Template{
		{"postgresql", "PostgreSQL", "database", "Persistent relational database on a private service endpoint.", "PostgreSQL", "https://www.postgresql.org", []string{"database-password"}, []string{"Persistent storage", "Separate database backups; one replica"}},
		{"valkey", "Valkey", "database", "Private persistent key-value database with append-only storage.", "BSD-3-Clause", "https://valkey.io", []string{"database-password"}, []string{"Persistent storage", "Separate backups; one replica"}},
		{"uptime-kuma", "Uptime Kuma", "application", "Self-hosted status and uptime monitoring.", "MIT", "https://github.com/louislam/uptime-kuma", []string{}, []string{"Persistent storage", "Complete its administrator setup before sharing its URL"}},
		{"gitea", "Gitea", "application", "Private Git hosting with an embedded SQLite database.", "MIT", "https://github.com/go-gitea/gitea", []string{}, []string{"Persistent storage", "Complete Gitea administrator setup; HTTP Git only"}},
		{"vllm", "vLLM", "ai", "Serve a public Hugging Face model behind an OpenAI-compatible endpoint.", "Apache-2.0 (engine); model license varies", "https://github.com/vllm-project/vllm", []string{}, []string{"NVIDIA GPU node and device plugin", "At least 8 GiB host RAM; GPU VRAM depends on model", "Review the model license and access terms", "Persistent model cache; one replica"}},
	}
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}/[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)
var modelRevisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func FromTemplate(id, name string, public bool, storage int64, model, modelRevision string) (Application, error) {
	if storage == 0 {
		storage = 5
	}
	service := Service{Size: "small", Replicas: 1, Public: public, Volume: &Volume{MountPath: "/data", SizeGiB: storage}}
	switch id {
	case "postgresql":
		service.Image = "postgres:17.11-alpine"
		service.Port = 5432
		service.Public = false
		service.Size = "medium"
		service.RunAsUser = 70
		service.Volume.MountPath = "/var/lib/postgresql/data"
		service.Env = map[string]string{"POSTGRES_USER": "hakopod", "POSTGRES_DB": "app", "PGDATA": "/var/lib/postgresql/data/pgdata"}
		service.Secrets = map[string]SecretRef{"POSTGRES_PASSWORD": {Ref: "database-password"}}
	case "valkey":
		service.Image = "valkey/valkey:9.1.2-alpine"
		service.Port = 6379
		service.Public = false
		service.RunAsUser = 999
		service.Command = []string{"sh", "-c"}
		service.Args = []string{`exec valkey-server --appendonly yes --requirepass "$VALKEY_PASSWORD"`}
		service.Secrets = map[string]SecretRef{"VALKEY_PASSWORD": {Ref: "database-password"}}
	case "uptime-kuma":
		service.Image = "louislam/uptime-kuma:2.5.4"
		service.Port = 3001
		service.Size = "medium"
		service.Volume.MountPath = "/app/data"
		service.Healthcheck = "/"
	case "gitea":
		service.Image = "gitea/gitea:1.27.3-rootless"
		service.Port = 3000
		service.Size = "medium"
		service.RunAsUser = 1000
		service.Volume.MountPath = "/var/lib/gitea"
		service.Env = map[string]string{"GITEA__server__DISABLE_SSH": "true", "GITEA__database__DB_TYPE": "sqlite3", "GITEA_APP_INI": "/var/lib/gitea/custom/conf/app.ini"}
		service.Healthcheck = "/api/healthz"
	case "vllm":
		if !modelPattern.MatchString(model) || !modelRevisionPattern.MatchString(modelRevision) {
			return Application{}, fmt.Errorf("a Hugging Face owner/model and immutable 40-character model revision are required")
		}
		service.Image = "vllm/vllm-openai:v0.29.0"
		service.Port = 8000
		service.Size = "gpu"
		service.Healthcheck = "/health"
		service.GPU = &GPU{Count: 1}
		service.Volume.MountPath = "/model-cache"
		if storage == 5 {
			service.Volume.SizeGiB = 30
		}
		service.Env = map[string]string{"HF_HOME": "/model-cache", "HOME": "/tmp"}
		service.Args = []string{"--model", model, "--revision", modelRevision, "--download-dir", "/model-cache", "--host", "0.0.0.0", "--port", "8000", "--max-model-len", "4096", "--gpu-memory-utilization", "0.85"}
	default:
		return Application{}, fmt.Errorf("unknown template")
	}
	return Normalize(Application{Name: name, Services: map[string]Service{"main": service}})
}
