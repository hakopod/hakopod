package main

import (
	"strings"
	"testing"
)

func mergeFiles(pairs ...string) []mergeFile {
	files := make([]mergeFile, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		files = append(files, mergeFile{Path: pairs[i], Data: []byte(pairs[i+1])})
	}
	return files
}

func TestMergeFolderSucceeds(t *testing.T) {
	cases := []struct {
		name     string
		files    []mergeFile
		services []string
		check    func(t *testing.T, got map[string]string)
	}{
		{
			name:     "bare service file alone is named after the file",
			files:    mergeFiles("app/web.toml", "image = \"nginx\"\nport = 80\nnetworks = [\"edge\"]\n"),
			services: []string{"web"},
		},
		{
			name: "two bare service files",
			files: mergeFiles(
				"app/api.toml", "image = \"api\"\n",
				"app/web.toml", "image = \"nginx\"\n",
			),
			services: []string{"api", "web"},
		},
		{
			name:     "application document with two service tables",
			files:    mergeFiles("app/hakopod.toml", "[services.web]\nimage = \"nginx\"\n\n[services.api]\nimage = \"api\"\n"),
			services: []string{"api", "web"},
		},
		{
			name: "mixed shapes merge",
			files: mergeFiles(
				"app/base.toml", "schema_version = 1\ninject_env = true\n[services.api]\nimage = \"api\"\n",
				"app/web.toml", "image = \"nginx\"\n",
			),
			services: []string{"api", "web"},
		},
		{
			name: "equal schema_version agrees",
			files: mergeFiles(
				"app/a.toml", "schema_version = 1\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "schema_version = 1\n[services.b]\nimage = \"b\"\n",
			),
			services: []string{"a", "b"},
		},
		{
			name: "different keys in networks, env and secrets merge cleanly",
			files: mergeFiles(
				"app/a.toml", "env = { A = \"1\" }\n[networks.edge]\ninternal = false\n[secrets.one]\nref = \"one\"\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "env = { B = \"2\" }\n[networks.core]\ninternal = true\n[secrets.two]\nref = \"two\"\n[services.b]\nimage = \"b\"\n",
			),
			services: []string{"a", "b"},
		},
		{
			name:     "single legacy hakopod.toml keeps env_file",
			files:    mergeFiles("app/hakopod.toml", "env_file = [\".env\"]\n[services.web]\nimage = \"nginx\"\n"),
			services: []string{"web"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, err := mergeFolder("shop", testCase.files)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if app.Name != "shop" {
				t.Fatalf("name = %q, want shop", app.Name)
			}
			if len(app.Services) != len(testCase.services) {
				t.Fatalf("services = %v, want %v", app.Services, testCase.services)
			}
			for _, want := range testCase.services {
				if _, ok := app.Services[want]; !ok {
					t.Fatalf("service %q missing from %v", want, app.Services)
				}
			}
		})
	}
}

func TestMergeFolderDetails(t *testing.T) {
	app, err := mergeFolder("shop", mergeFiles(
		"app/a.toml", "schema_version = 1\nenv = { A = \"1\" }\ndomains = { \"x.test\" = \"web\" }\n[networks.edge]\ninternal = false\n[secrets.one]\nref = \"one\"\n[services.a]\nimage = \"a\"\n",
		"app/b.toml", "env = { B = \"2\" }\n[networks.core]\ninternal = true\n[secrets.two]\nref = \"two\"\n[services.b]\nimage = \"b\"\n",
		"app/web.toml", "image = \"nginx\"\nnetworks = [\"edge\"]\nenv = { PORT = \"80\" }\n",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", app.SchemaVersion)
	}
	if app.Env["A"] != "1" || app.Env["B"] != "2" || len(app.Env) != 2 {
		t.Fatalf("application env = %v", app.Env)
	}
	if len(app.Networks) != 2 || app.Networks["core"].Internal != true {
		t.Fatalf("application networks = %v", app.Networks)
	}
	if len(app.Secrets) != 2 {
		t.Fatalf("application secrets = %v", app.Secrets)
	}
	if app.Domains["x.test"] != "web" {
		t.Fatalf("domains = %v", app.Domains)
	}
	// A bare service file's networks/env belong to the service, not the application.
	web := app.Services["web"]
	if len(web.Networks) != 1 || web.Networks[0] != "edge" {
		t.Fatalf("service networks = %v", web.Networks)
	}
	if web.Env["PORT"] != "80" {
		t.Fatalf("service env = %v", web.Env)
	}
}

func TestMergeFolderErrors(t *testing.T) {
	cases := []struct {
		name  string
		files []mergeFile
		want  []string
	}{
		{
			name:  "empty slice",
			files: nil,
			want:  []string{"no TOML files"},
		},
		{
			name: "duplicate service across files",
			files: mergeFiles(
				"app/one.toml", "[services.web]\nimage = \"a\"\n",
				"app/two.toml", "[services.web]\nimage = \"b\"\n",
			),
			want: []string{"app/one.toml", "app/two.toml", "web"},
		},
		{
			name: "bare service file collides with a services table",
			files: mergeFiles(
				"app/hako.toml", "[services.web]\nimage = \"a\"\n",
				"app/web.toml", "image = \"b\"\n",
			),
			want: []string{"app/hako.toml", "app/web.toml", "web"},
		},
		{
			name: "name set twice",
			files: mergeFiles(
				"app/a.toml", "name = \"shop\"\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "name = \"shop\"\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "name"},
		},
		{
			name: "recovery set twice",
			files: mergeFiles(
				"app/a.toml", "[recovery]\non_failure = \"rollback\"\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "[recovery]\non_failure = \"disabled\"\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "recovery"},
		},
		{
			name: "inject_env set twice",
			files: mergeFiles(
				"app/a.toml", "inject_env = true\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "inject_env = false\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "inject_env"},
		},
		{
			name: "domains set twice",
			files: mergeFiles(
				"app/a.toml", "domains = { \"a.test\" = \"a\" }\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "domains = { \"b.test\" = \"b\" }\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "domains"},
		},
		{
			name: "volumes set twice",
			files: mergeFiles(
				"app/a.toml", "[volumes.data]\nsize_gib = 1\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "[volumes.more]\nsize_gib = 2\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "volumes"},
		},
		{
			name: "schema_version differs",
			files: mergeFiles(
				"app/a.toml", "schema_version = 1\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "schema_version = 2\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "schema_version"},
		},
		{
			name:  "name disagrees with folder",
			files: mergeFiles("app/a.toml", "name = \"other\"\n[services.a]\nimage = \"a\"\n"),
			want:  []string{"app/a.toml", "other", "shop"},
		},
		{
			name: "same key inside networks",
			files: mergeFiles(
				"app/a.toml", "[networks.edge]\ninternal = false\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "[networks.edge]\ninternal = true\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "networks.edge"},
		},
		{
			name: "same key inside env",
			files: mergeFiles(
				"app/a.toml", "env = { SHARED = \"1\" }\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "env = { SHARED = \"2\" }\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "env.SHARED"},
		},
		{
			name: "same key inside secrets",
			files: mergeFiles(
				"app/a.toml", "[secrets.token]\nref = \"one\"\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "[secrets.token]\nref = \"two\"\n[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", "app/b.toml", "secrets.token"},
		},
		{
			name: "env_file in a merged application document",
			files: mergeFiles(
				"app/a.toml", "env_file = [\".env\"]\n[services.a]\nimage = \"a\"\n",
				"app/b.toml", "[services.b]\nimage = \"b\"\n",
			),
			want: []string{"app/a.toml", envFileUnsupported},
		},
		{
			name:  "env_file in a bare service file",
			files: mergeFiles("app/web.toml", "image = \"nginx\"\nenv_file = [\".env\"]\n"),
			want:  []string{"app/web.toml", envFileUnsupported},
		},
		{
			name:  "env_file in a merged file named hakopod.toml",
			files: mergeFiles("app/hakopod.toml", "env_file = [\".env\"]\n[services.a]\nimage = \"a\"\n", "app/web.toml", "image = \"nginx\"\n"),
			want:  []string{"app/hakopod.toml", envFileUnsupported},
		},
		{
			name:  "decode error names the path",
			files: mergeFiles("app/broken.toml", "image = = \"nginx\"\n"),
			want:  []string{"app/broken.toml"},
		},
		{
			name:  "type error in a bare service file names the path",
			files: mergeFiles("app/web.toml", "image = 12\n"),
			want:  []string{"app/web.toml"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := mergeFolder("shop", testCase.files)
			if err == nil {
				t.Fatalf("expected an error")
			}
			for _, want := range testCase.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}
