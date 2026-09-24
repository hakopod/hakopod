package spec

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompleteCatalogDependencyCredentialsAndStorage(t *testing.T) {
	for _, id := range []string{"dagu", "couchdb", "blinko", "bytebase", "baserow"} {
		t.Run(id, func(t *testing.T) {
			app, err := PlanTemplate(id, TemplateOptions{Name: "reviewed", Public: true, SiteURL: func() string {
				if id == "blinko" || id == "baserow" || id == "calcom" {
					return "https://workspace.example.test"
				}
				return ""
			}(), Values: func() map[string]string {
				if id == "baserow" {
					return map[string]string{"storage-bucket": "private-media", "storage-region": "auto", "storage-endpoint": "https://account.r2.cloudflarestorage.com"}
				}
				return nil
			}()})
			if err != nil {
				t.Fatal(err)
			}
			for name, s := range app.Services {
				if s.RunAsUser <= 0 {
					t.Fatalf("%s has no explicit unprivileged UID", name)
				}
				if name != "main" && s.Public {
					t.Fatalf("dependency %s is public", name)
				}
				for _, binding := range s.Bindings {
					if binding.Protocol == "postgres" && binding.Password.Ref != app.Services[binding.Service].Secrets["POSTGRES_PASSWORD"].Ref {
						t.Fatal("database credential binding diverges")
					}
				}
			}
			switch id {
			case "dagu":
				if !strings.Contains(strings.Join(app.Services["main"].Command, " "), "/usr/local/bin/dagu") || strings.Contains(strings.Join(app.Services["main"].Command, " "), "entrypoint.sh") {
					t.Fatal("Dagu still requires privileged entrypoint")
				}
			case "couchdb":
				s := app.Services["main"]
				if s.Public || !strings.Contains(*s.Files["single-node"].Content, "single_node = true") || s.Secrets["COUCHDB_PASSWORD"].Ref == "" {
					t.Fatal("CouchDB is incomplete or public")
				}
			case "blinko":
				if app.Services["main"].Volume.MountPath != "/app/.blinko" {
					t.Fatal("uploaded files are not persistent")
				}
			case "bytebase":
				if !reflect.DeepEqual(TemplateSecretNames(app), []string{"database-password"}) {
					t.Fatal("Bytebase needs an unnecessary duplicate URL secret")
				}
			case "baserow":
				if len(app.Services) != 7 || len(app.Volumes) != 0 {
					t.Fatal("Baserow topology or storage changed")
				}
				for _, name := range []string{"backend", "worker", "beat"} {
					s := app.Services[name]
					if len(s.Mounts) > 0 || s.Env["AWS_STORAGE_BUCKET_NAME"] != "private-media" || s.Secrets["AWS_SECRET_ACCESS_KEY"].Ref != "storage-secret-key" {
						t.Fatal("S3 media settings missing", name)
					}
					if !strings.Contains(*s.Files["storage-settings"].Content, "AWS_DEFAULT_ACL = None") || !strings.Contains(*s.Files["storage-settings"].Content, "AWS_QUERYSTRING_AUTH = True") {
						t.Fatal("private signed media settings missing", name)
					}
				}
				if app.Services["worker"].Args[0] != "celery-worker" || app.Services["beat"].Args[0] != "celery-beat" {
					t.Fatal("background tasks omitted")
				}
				for _, name := range []string{"storage-access-key", "storage-secret-key"} {
					f, ok := TemplateSecretFieldByName(id, name)
					if !ok || f.Generate {
						t.Fatal("provider credential offered random generation")
					}
				}
			}
		})
	}
	if _, err := PlanTemplate("autobase", TemplateOptions{Name: "blocked"}); err == nil {
		t.Fatal("host Docker socket application was enabled")
	}
}

func TestBaserowStorageConfigurationRejectsInlineCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://s3.example.test", "https://user:password@s3.example.test", "https://s3.example.test/bucket", "https://s3.example.test?access_key=secret"} {
		_, err := PlanTemplate("baserow", TemplateOptions{Name: "app", SiteURL: "https://app.example.test", Values: map[string]string{"storage-bucket": "media-bucket", "storage-region": "auto", "storage-endpoint": endpoint}})
		if err == nil {
			t.Fatal("unsafe or incompatible S3 endpoint accepted")
		}
	}
}
