package spec

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestMathesarTemplateStorageCredentialsAndProxy(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		app, err := PlanTemplate("mathesar", TemplateOptions{
			Name: "tables", Public: true, StorageGiB: 9, Architecture: architecture,
			SiteURL: "https://tables.example.test:8443",
			Values:  map[string]string{"media-storage-class": "shared-media"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(app.Services) != 3 || !app.Services["main"].Public || app.Services["backend"].Public || app.Services["db"].Public {
			t.Fatal("only the proxy may be public")
		}
		for name, service := range app.Services {
			if service.RunAsUser <= 0 || service.Architecture != architecture || !strings.Contains(service.Image, "@sha256:") {
				t.Fatalf("%s lacks its non-root identity, architecture or immutable image", name)
			}
		}
		media := app.Volumes["media"]
		if media.StorageClass != "shared-media" || media.AccessMode != "ReadWriteMany" || media.SizeGiB != 9 || app.Services["db"].Volume.SizeGiB != 9 {
			t.Fatal("media must use the selected shared class and both data volumes must retain the selected capacity")
		}
		backend, proxy, database := app.Services["backend"], app.Services["main"], app.Services["db"]
		if len(backend.Mounts) != 1 || len(proxy.Mounts) != 1 || backend.Mounts[0].Volume != "media" || proxy.Mounts[0].Volume != "media" || backend.Mounts[0].ReadOnly || !proxy.Mounts[0].ReadOnly || backend.FSGroup != proxy.FSGroup {
			t.Fatal("proxy must read the same media that the backend writes")
		}
		if backend.Env["ALLOWED_HOSTS"] != "tables.example.test" || proxy.Env["SITE_HOST"] != "tables.example.test" {
			t.Fatal("host validation must exclude the origin's port")
		}
		if backend.Secrets["POSTGRES_PASSWORD"].Ref != database.Secrets["POSTGRES_PASSWORD"].Ref || backend.Env["POSTGRES_DB"] != database.Env["POSTGRES_DB"] || backend.Env["POSTGRES_USER"] != database.Env["POSTGRES_USER"] {
			t.Fatal("application and PostgreSQL initialization credentials diverged")
		}
		if !reflect.DeepEqual(TemplateSecretNames(app), []string{"database-password", "secret-key"}) || len(proxy.Secrets) != 0 {
			t.Fatal("only the database password and stable signing key should be required")
		}
		if backend.Env["DEBUG"] != "false" || backend.Env["WEB_CONCURRENCY"] != "1" || backend.Healthcheck != "/healthz/ready/" || !reflect.DeepEqual(backend.DependsOn, []string{"db"}) || !reflect.DeepEqual(proxy.DependsOn, []string{"backend"}) {
			t.Fatal("production configuration, bounded workers or readiness ordering changed")
		}
		data, err := toml.Marshal(app)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err != nil || strings.Contains(string(data), "{{") {
			t.Fatal("reviewed TOML contains invalid configuration or unresolved placeholders", err)
		}
	}
}

func TestMathesarTemplateRejectsMissingOrInvalidPrerequisites(t *testing.T) {
	options := TemplateOptions{Name: "tables", SiteURL: "https://tables.example.test", Values: map[string]string{"media-storage-mode": "shared", "media-storage-class": "shared-media"}}
	for _, class := range []string{"", " ", "Uppercase", "with/slash", "two words", "class\n[services.injected]", "{{config.injected}}"} {
		options.Values["media-storage-class"] = class
		if _, err := PlanTemplate("mathesar", options); err == nil {
			t.Fatalf("invalid storage class accepted: %q", class)
		}
	}
	options.Values["media-storage-class"] = "shared-media"
	options.SiteURL = ""
	if _, err := PlanTemplate("mathesar", options); err == nil {
		t.Fatal("missing canonical HTTPS origin accepted")
	}
	if err := ValidateTemplateSecret("mathesar", "secret-key", strings.Repeat("a", 63)); err == nil {
		t.Fatal("short signing key accepted")
	}
	if err := ValidateTemplateSecret("mathesar", "secret-key", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestMathesarAutomaticLocalMedia(t *testing.T) {
	for _, values := range []map[string]string{
		nil,
		{"media-storage-class": ""},
		{"media-storage-mode": "local", "media-storage-class": "unfinished shared draft"},
	} {
		app, err := PlanTemplate("mathesar", TemplateOptions{Name: "tables", SiteURL: "https://tables.example.test", StorageGiB: 7, Values: values})
		if err != nil {
			t.Fatal(err)
		}
		media := app.Volumes["media"]
		if media.AccessMode != "ReadWriteOnce" || media.StorageClass != "" || media.SizeGiB != 7 {
			t.Fatal("automatic media must use a retained claim from the installation default class", media)
		}
		if !reflect.DeepEqual(SharedReadWriteOnceGroups(app), [][]string{{"backend", "main"}}) {
			t.Fatal("automatic media consumers must be scheduled together")
		}
		data, err := toml.Marshal(app)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err != nil {
			t.Fatal("reviewed automatic plan did not round trip", err)
		}
	}
	for _, mode := range []string{"", "nfs", "auto"} {
		if _, err := PlanTemplate("mathesar", TemplateOptions{Name: "tables", SiteURL: "https://tables.example.test", Values: map[string]string{"media-storage-mode": mode}}); err == nil {
			t.Fatalf("invalid media mode accepted: %q", mode)
		}
	}
}

func TestMathesarExternalDatabaseOmitsBundledDatabase(t *testing.T) {
	options := TemplateOptions{Name: "tables", SiteURL: "https://tables.example.test", Values: map[string]string{
		"media-storage-class": "shared-media", "database-mode": "external", "database-host": "postgres.example.test",
		"database-name": "existing-mathesar", "database-user": "existing-user", "database-port": "6543",
	}}
	app, err := PlanTemplate("mathesar", options)
	if err != nil {
		t.Fatal(err)
	}
	backend := app.Services["backend"]
	tls := backend.Files["database-tls"]
	if tls.MountPath != "/code/config/settings/local.py" || tls.Content == nil || !strings.Contains(*tls.Content, `"sslmode"`) || !strings.Contains(*tls.Content, `"sslrootcert"`) || backend.Env["PGCONNECT_TIMEOUT"] != "10" {
		t.Fatal("external TLS settings or connection deadline would be lost in upstream configuration")
	}
	if len(app.Services) != 2 || len(backend.DependsOn) != 0 || app.Services["db"].Image != "" {
		t.Fatal("existing database mode retained a bundled database or startup dependency")
	}
	for key, expected := range map[string]string{
		"POSTGRES_HOST": "postgres.example.test", "POSTGRES_PORT": "6543", "POSTGRES_DB": "existing-mathesar",
		"POSTGRES_USER": "existing-user", "POSTGRES_SSLMODE": "verify-full", "PGSSLMODE": "verify-full",
		"PGSSLROOTCERT": "/etc/ssl/certs/ca-certificates.crt",
	} {
		if backend.Env[key] != expected {
			t.Fatalf("%s does not match the reviewed external connection", key)
		}
	}
	if backend.Secrets["POSTGRES_PASSWORD"].Ref != "database-password" || !reflect.DeepEqual(TemplateSecretNames(app), []string{"database-password", "secret-key"}) {
		t.Fatal("external connection must use the same scoped password and signing-key flow")
	}
	options.Values["database-host"] = "postgres"
	if _, err := PlanTemplate("mathesar", options); err != nil {
		t.Fatal("private single-label DNS host was rejected", err)
	}
	for key, invalid := range map[string]string{
		"database-mode": "mysql", "database-host": "postgres://user:password@host/db", "database-port": "65536",
		"database-name": "", "database-user": "", "database-sslmode": "prefer",
	} {
		previous, exists := options.Values[key]
		options.Values[key] = invalid
		if _, err := PlanTemplate("mathesar", options); err == nil {
			t.Errorf("invalid external %s was accepted", key)
		}
		if exists {
			options.Values[key] = previous
		} else {
			delete(options.Values, key)
		}
	}
	options.Values["database-mode"] = "bundled"
	options.Values["database-host"] = "unfinished external draft"
	options.Values["database-port"] = "invalid draft"
	options.Values["database-sslmode"] = "invalid draft"
	app, err = PlanTemplate("mathesar", options)
	if err != nil || len(app.Services) != 3 || app.Services["backend"].Env["POSTGRES_HOST"] != "db" || app.Services["backend"].Env["PGSSLMODE"] != "" {
		t.Fatal("inactive connection drafts changed the bundled workload", err)
	}
}
