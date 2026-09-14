package spec

import (
	"github.com/pelletier/go-toml/v2"
	"strings"
	"testing"
)

func TestJobDependenciesAndFiles(t *testing.T) {
	app, err := Parse([]byte(`name="jobs"
[services.db]
image="postgres:17"
port=5432
[services.migrate]
image="example/app:1"
depends_on=["db"]
[services.migrate.job]
timeout_seconds=120
retries=1
[services.migrate.files.config]
mount_path="/etc/app/config.yaml"
content="host: db"
[services.migrate.files.credentials]
mount_path="/app/password"
secret={ref="db-password"}
mode=288
[services.web]
image="example/app:1"
depends_on=["migrate"]
`))
	if err != nil {
		t.Fatal(err)
	}
	order, _ := Order(app)
	if strings.Join(order, ",") != "db,migrate,web" {
		t.Fatal(order)
	}
	refs := SecretReferences(app.Services["migrate"])
	if refs[FileSecretKey("credentials")].Ref != "db-password" {
		t.Fatal(refs)
	}
	for _, change := range Diff(nil, app) {
		if change.Field == "files" && (change.Before != "[redacted]" || change.After != "[redacted]") {
			t.Fatal("file body exposed in diff")
		}
	}
	encoded, err := toml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(encoded)); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleRejectsInvalidAndUnsafeInputs(t *testing.T) {
	content := "config"
	for name, svc := range map[string]Service{
		"job-listener":   {Job: &Job{}, Port: 80},
		"job-retries":    {Job: &Job{Retries: 4}},
		"job-timeout":    {Job: &Job{TimeoutSeconds: 901}},
		"job-replicas":   {Job: &Job{}, Replicas: 2},
		"job-health":     {Job: &Job{}, Healthcheck: "/health"},
		"file-system":    {Files: map[string]File{"config": {MountPath: "/proc/config", Content: &content}}},
		"file-parent":    {Files: map[string]File{"config": {MountPath: "/var/run", Content: &content}}},
		"file-traversal": {Files: map[string]File{"config": {MountPath: "/app/../etc/passwd", Content: &content}}},
		"file-double":    {Files: map[string]File{"config": {MountPath: "/app/config", Content: &content, Secret: &SecretRef{Ref: "password"}}}},
		"file-overlap":   {Files: map[string]File{"config": {MountPath: "/app/config", Content: &content}}, TemporaryMounts: []TemporaryMount{{MountPath: "/app", SizeMiB: 1}}},
		"file-mode":      {Files: map[string]File{"config": {MountPath: "/app/config", Content: &content, Mode: 0777}}},
	} {
		t.Run(name, func(t *testing.T) {
			svc.Image = "example/app:1"
			if _, err := Normalize(Application{Name: "test", Services: map[string]Service{"main": svc}}); err == nil {
				t.Fatal("accepted invalid lifecycle configuration")
			}
		})
	}
}
