package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
)

func TestImportedEnvironmentSecretsScopedStableAndRedacted(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "environment-import")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Cluster: templateSecretKube(t), Auth: AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))}}
	config := []byte("name='envfiles'\nenv_file='.env'\n[services.web]\nimage='nginx'\n[services.worker]\nimage='nginx'")
	files := map[string]string{".env": "MODE=production\nAPI_TOKEN=private-fixture"}
	first, err := s.importEnvironment(ctx, p, "demo", "development", "", config, files)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.importEnvironment(ctx, p, "demo", "development", "", config, files)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("identical import changed references", err)
	}
	if strings.Contains(string(store.JSON(first)), "private-fixture") || first.Secrets["API_TOKEN"].Ref == "" || first.Env["MODE"] != "production" {
		t.Fatal("unredacted or missing imported values")
	}
	items, err := s.Cluster.ListWorkloadSecrets(ctx, "demo", "development", "envfiles")
	if err != nil || len(items) != 1 {
		t.Fatal("retry duplicated secret storage", err)
	}
	files[".env"] = "API_TOKEN=changed-fixture"
	changed, err := s.importEnvironment(ctx, p, "demo", "development", "", config, files)
	if err != nil || changed.Secrets["API_TOKEN"].Ref == first.Secrets["API_TOKEN"].Ref {
		t.Fatal("changed value did not create a new deployment input", err)
	}
	denied := p
	denied.Admin, denied.Owner, denied.Permissions = false, false, []string{"deployments:read"}
	if _, err := s.importEnvironment(ctx, denied, "demo", "development", "", config, files); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("reader imported secrets", err)
	}
	if _, err := s.importEnvironment(ctx, p, "demo", "development", "different-app", config, files); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("wrong target imported secrets", err)
	}
	other, err := s.importEnvironment(ctx, p, "demo", "development", "", bytes.Replace(config, []byte("envfiles"), []byte("otherapp"), 1), files)
	if err != nil || other.Secrets["API_TOKEN"].Ref == changed.Secrets["API_TOKEN"].Ref {
		t.Fatal("references crossed application scope", err)
	}
	// The HTTP Compose conversion follows the same secret storage and redaction path.
	result := gitConnectionCall(t, s.Handler(), token, "POST", "/compose/convert", map[string]any{"project": "demo", "environment": "development", "name": "compose-env", "yaml": "services:\n  api:\n    image: nginx\n    env_file: .env\n", "env_files": files}, 200)
	if strings.Contains(string(store.JSON(result)), "changed-fixture") {
		t.Fatal("Compose response exposed file secret")
	}
}

func TestSourceEnvironmentUsesReviewedCommitAndConfigDirectory(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			commit := strings.Repeat("a", 40)
			requests := 0
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if !strings.HasSuffix(r.URL.Path, "/config/runtime/.env") || r.URL.Query().Get("ref") != commit {
					t.Error("environment file not read from reviewed directory/commit")
				}
				content := "MODE=production"
				write(w, 200, map[string]any{"type": "file", "encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))})
			}))
			defer remote.Close()
			credentials := func(context.Context) (map[string][]byte, error) { return map[string][]byte{}, nil }
			s := &Server{githubAPIURL: remote.URL, gitlabAPIURL: remote.URL, githubHTTP: remote.Client(), gitlabHTTP: remote.Client(), githubTestCredentials: credentials, gitlabTestCredentials: credentials}
			binding := sourceBinding{Provider: provider, Repository: "example/repo", Path: "config/hakopod.toml"}
			scope := environmentImportScope{Principal: store.Principal{Admin: true, Permissions: []string{"admin"}}, Project: "demo", Environment: "development", Application: "app"}
			config := []byte("name='app'\nenv_file='runtime/.env'\n[services.web]\nimage='nginx'")
			app, err := s.importSourceEnvironment(context.Background(), binding, commit, config, scope)
			if err != nil || requests != 1 || app.Env["MODE"] != "production" || !app.InjectEnv {
				t.Fatal("source environment import failed", err)
			}
			requests = 0
			config = bytes.Replace(config, []byte("runtime/.env"), []byte("../.env"), 1)
			if _, err = s.importSourceEnvironment(context.Background(), binding, commit, config, scope); err == nil || requests != 0 {
				t.Fatal("unsafe path reached source provider")
			}
		})
	}
}
