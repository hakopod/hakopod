package spec

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func xemTemplateOptions() TemplateOptions {
	return TemplateOptions{Name: "mail", Public: true, SiteURL: "https://mail.example.test", Values: map[string]string{
		"admin-email": "admin@example.test", "storage-endpoint": "https://s3.example.test", "storage-bucket": "xem-media", "storage-region": "us-east-1",
	}}
}

func TestXemDefaultsBundleEveryDependency(t *testing.T) {
	app, err := PlanTemplate("xem", TemplateOptions{Name: "mail", SiteURL: "https://mail.example.test", StorageGiB: 9,
		Values: map[string]string{"admin-email": "admin@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"db", "redis", "storage"} {
		service, ok := app.Services[name]
		if !ok || service.Volume == nil || service.Volume.SizeGiB != 9 {
			t.Fatalf("default %s must be bundled with the selected persistent capacity", name)
		}
	}
	if slices.Contains(TemplateSecretNames(app), "storage-access-key") || slices.Contains(TemplateSecretNames(app), "storage-secret-key") {
		t.Fatal("default installation must not need external storage credentials")
	}
}

func TestXemIndependentDependencyChoices(t *testing.T) {
	for _, database := range []string{"bundled", "external"} {
		for _, redis := range []string{"bundled", "external"} {
			for _, storage := range []string{"bundled", "external"} {
				t.Run(database+"/"+redis+"/"+storage, func(t *testing.T) {
					o := xemTemplateOptions()
					o.Values["database-mode"] = database
					o.Values["redis-mode"] = redis
					o.Values["storage-mode"] = storage
					o.Values["database-host"] = "postgres.example.test"
					o.Values["redis-host"] = "redis.example.test"
					app, err := PlanTemplate("xem", o)
					if err != nil {
						t.Fatal(err)
					}
					_, hasDB := app.Services["db"]
					_, hasRedis := app.Services["redis"]
					_, hasStorage := app.Services["storage"]
					backend := app.Services["backend"]
					if backend.Env["S3_DISABLE_ACL"] != "true" {
						t.Fatal("self-hosted storage must omit object ACLs and retain private signed reads")
					}
					if hasDB != (database == "bundled") || hasRedis != (redis == "bundled") || hasStorage != (storage == "bundled") || slices.Contains(backend.DependsOn, "db") != hasDB || slices.Contains(backend.DependsOn, "redis") != hasRedis || slices.Contains(backend.DependsOn, "storage") != hasStorage {
						t.Fatal("selected dependencies and services disagree")
					}
					if database == "external" && (backend.Env["POSTGRES_HOST"] != "postgres.example.test" || backend.Env["POSTGRES_SSLMODE"] != "verify-full") {
						t.Fatal("existing PostgreSQL must use the supplied host and verify TLS by default")
					}
					if redis == "external" && (backend.Env["REDIS_HOST"] != "redis.example.test" || backend.Env["REDIS_USE_TLS"] != "true") {
						t.Fatal("existing Redis must use the supplied host and verify TLS by default")
					}
					for name, service := range app.Services {
						if service.Public != (name == "main") || service.RunAsUser <= 0 || !strings.Contains(service.Image, "@sha256:") {
							t.Fatalf("%s lost private networking, non-root identity or image pin", name)
						}
					}
					if backend.Secrets["POSTGRES_PASSWORD"].Ref != "database-password" || backend.Secrets["REDIS_PASSWORD"].Ref != "redis-password" || backend.Secrets["PRIVATE_KEY"].Ref != "encryption-private-key" {
						t.Fatal("existing connections must retain scoped secret bindings")
					}
					required := []string{"admin-password", "database-password", "encryption-private-key", "frontend-session-secret", "jwt-secret", "redis-password"}
					route, hasRoute := app.Services["main"].Files["storage"]
					if hasStorage {
						required = append(required, "storage-password", "storage-user")
						if backend.Env["S3_ENDPOINT_URL"] != "http://storage:9000" || backend.Env["S3_PUBLIC_ENDPOINT_URL"] != o.SiteURL || backend.Env["S3_BUCKET_NAME"] != "xem-files" || backend.Env["S3_CREATE_BUCKET"] != "true" || backend.Secrets["S3_SECRET_KEY"].Ref != "storage-password" || backend.Secrets["S3_ACCESS_KEY"].Ref != "storage-user" || backend.Env["S3_ACCESS_KEY"] != "" {
							t.Fatal("bundled storage must use private writes and the installation origin for signed reads")
						}
						minio := app.Services["storage"]
						if minio.Volume == nil || minio.Volume.MountPath != "/data" || minio.Volume.SizeGiB != 5 || minio.Secrets["MINIO_ROOT_PASSWORD"].Ref != "storage-password" || minio.Secrets["MINIO_ROOT_USER"].Ref != "storage-user" || len(minio.Ports) != 0 {
							t.Fatal("bundled MinIO lost durable storage, matching credentials or private console")
						}
						if !hasRoute || route.Content == nil || !strings.Contains(*route.Content, "xem-files") || !strings.Contains(*route.Content, "GET HEAD") || !strings.Contains(*route.Content, "-Cookie") || !strings.Contains(*route.Content, "-Authorization") {
							t.Fatal("public object route must be limited to signed reads without application credentials")
						}
					} else {
						required = append(required, "storage-access-key", "storage-secret-key")
						if hasRoute || backend.Env["S3_PUBLIC_ENDPOINT_URL"] != "" || backend.Env["S3_CREATE_BUCKET"] != "" || backend.Env["S3_ACCESS_KEY"] != "" || backend.Env["S3_ENDPOINT_URL"] != "https://s3.example.test" || backend.Env["S3_BUCKET_NAME"] != "xem-media" || backend.Secrets["S3_ACCESS_KEY"].Ref != "storage-access-key" || backend.Secrets["S3_SECRET_KEY"].Ref != "storage-secret-key" {
							t.Fatal("external storage must use supplied credentials without bucket provisioning or a local proxy")
						}
					}
					if got := TemplateSecretNames(app); !reflect.DeepEqual(got, required) {
						t.Fatal("preset secret references changed", got)
					}
					encoded, err := toml.Marshal(app)
					if err != nil || strings.Contains(string(encoded), "{{") {
						t.Fatal("review contains unresolved values", err)
					}
					parsed, err := Parse(encoded)
					if err != nil || !reflect.DeepEqual(app, parsed) {
						t.Fatal("reviewed TOML changed the deployment", err)
					}
				})
			}
		}
	}
}

func TestXemTemplateValidationAndInactiveDrafts(t *testing.T) {
	o := xemTemplateOptions()
	o.Values["database-mode"] = "external"
	o.Values["database-host"] = "postgres"
	o.Values["redis-mode"] = "external"
	o.Values["redis-host"] = "redis"
	o.Values["storage-mode"] = "external"
	for field, invalid := range map[string]string{
		"database-mode": "mysql", "database-host": "postgres://secret@host/db", "database-port": "0", "database-sslmode": "prefer",
		"database-name": "", "database-user": "", "redis-mode": "managed", "redis-host": "rediss://secret@host/0", "redis-port": "65536",
		"redis-db": "-1", "redis-tls": "maybe", "redis-username": " padded ", "storage-endpoint": "http://s3.example.test",
		"storage-mode": "disk", "storage-bucket": "bad/bucket", "storage-region": "bad region", "admin-email": "Display <admin@example.test>", "unknown-field": "ignored",
	} {
		t.Run(field, func(t *testing.T) {
			options := o
			options.Values = maps.Clone(o.Values)
			options.Values[field] = invalid
			if _, err := PlanTemplate("xem", options); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	o.Values["database-mode"] = "bundled"
	o.Values["redis-mode"] = "bundled"
	o.Values["storage-mode"] = "bundled"
	o.Values["storage-endpoint"] = "unfinished draft"
	o.Values["storage-bucket"] = "unfinished draft"
	o.Values["storage-region"] = "unfinished draft"
	o.Values["database-host"] = "unfinished draft"
	o.Values["database-port"] = "unfinished draft"
	o.Values["redis-port"] = "unfinished draft"
	o.Values["redis-tls"] = "unfinished draft"
	app, err := PlanTemplate("xem", o)
	if err != nil || len(app.Services) != 6 || app.Services["backend"].Env["POSTGRES_HOST"] != "db" || app.Services["backend"].Env["REDIS_HOST"] != "redis" {
		t.Fatal("hidden draft changed the bundled deployment", err)
	}
	o.Values["redis-mode"] = "external"
	o.Values["redis-host"] = "2001:db8::1"
	o.Values["redis-port"] = "6380"
	o.Values["redis-tls"] = "true"
	app, err = PlanTemplate("xem", o)
	if err != nil || app.Services["backend"].Env["REDIS_HOST"] != "[2001:db8::1]" {
		t.Fatal("IPv6 Redis host would form an invalid host:port address", err)
	}
}

func TestTemplateRSAEncryptionKeyValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(kind string, der []byte) string {
		return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
	}
	valid := encode("PRIVATE KEY", pkcs8)
	for _, value := range []string{valid, encode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))} {
		if err := ValidateTemplateSecret("xem", "encryption-private-key", value); err != nil {
			t.Fatal("valid RSA private key rejected", err)
		}
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(valid)
	for name, invalid := range map[string]string{
		"not base64": "sensitive-invalid-value", "newline": valid + "\n", "ec": encode("PRIVATE KEY", ecDER),
		"weak RSA": encode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(weak)), "public key": encode("PUBLIC KEY", x509.MarshalPKCS1PublicKey(&key.PublicKey)),
		"encrypted": encode("ENCRYPTED PRIVATE KEY", pkcs8), "multiple PEM": base64.StdEncoding.EncodeToString(append(decoded, decoded...)),
		"prefix": base64.StdEncoding.EncodeToString(append([]byte("ignored leading data"), decoded...)), "too large": strings.Repeat("a", 8193),
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateTemplateSecret("xem", "encryption-private-key", invalid)
			if err == nil || strings.Contains(err.Error(), invalid) {
				t.Fatal("invalid credential accepted or disclosed")
			}
		})
	}
	if err := ValidateTemplateSecret("xem", "admin-password", strings.Repeat("é", 37)); err == nil {
		t.Fatal("bcrypt byte limit was not enforced")
	}
	if err := ValidateTemplateSecret("xem", "admin-password", strings.Repeat("é", 36)); err != nil {
		t.Fatal("72-byte bcrypt password rejected", err)
	}
}
