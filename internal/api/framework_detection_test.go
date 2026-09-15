package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestFrameworkDetectionReadsPinnedMetadataAndChecksAuthority(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	key, err := db.Bootstrap(ctx, "detector")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			sha := strings.Repeat("a", 40)
			manifest := []byte(`{"scripts":{"build":"astro build"},"dependencies":{"astro":"7.3.2"}}`)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("ref") != "" && r.URL.Query().Get("ref") != sha {
					t.Error("metadata was not read from resolved commit")
				}
				switch {
				case strings.Contains(r.URL.Path, "commits/"):
					json.NewEncoder(w).Encode(map[string]string{"sha": sha, "id": sha})
				case strings.Contains(r.URL.Path, "tree") || strings.HasSuffix(r.URL.Path, "/contents/"):
					if provider == "gitlab" && r.URL.Query().Get("path") != "" {
						t.Error("GitLab root path is not empty")
					}
					kind := "file"
					if provider == "gitlab" {
						kind = "blob"
					}
					json.NewEncoder(w).Encode([]map[string]string{{"name": "package.json", "type": kind}, {"name": "package-lock.json", "type": kind}})
				case strings.Contains(r.URL.Path, "package.json"):
					json.NewEncoder(w).Encode(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(manifest), "size": len(manifest)})
				default:
					t.Error("unexpected provider request", r.URL.String())
					http.NotFound(w, r)
				}
			}))
			defer remote.Close()
			s := &Server{Store: db, githubAPIURL: remote.URL, gitlabAPIURL: remote.URL, githubHTTP: remote.Client(), gitlabHTTP: remote.Client(), githubTestCredentials: func(context.Context) (map[string][]byte, error) {
				return map[string][]byte{"token": []byte("synthetic-provider-token")}, nil
			}, gitlabTestCredentials: func(context.Context) (map[string][]byte, error) {
				return map[string][]byte{"token": []byte("synthetic-provider-token")}, nil
			}}
			body := store.JSON(map[string]any{"project": "demo", "environment": "development", "name": "demo", "service": "web", "provider": provider, "repository": "team/repo", "branch": "main", "mode": "dockerfile"})
			request := func(principal store.Principal) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://localhost/api/v1/builds/detect", bytes.NewReader(body))
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
				w := httptest.NewRecorder()
				s.detectBuild(w, r)
				return w
			}
			denied := p
			denied.Admin = false
			denied.Permissions = []string{"deployments:write"}
			denied.IdentityPermissions = []string{"deployments:write"}
			if result := request(denied); result.Code != 403 || calls != 0 {
				t.Fatal("non-admin repository access", result.Code, result.Body.String())
			}
			result := request(p)
			if result.Code != 200 || !strings.Contains(result.Body.String(), `"framework":"astro"`) || calls != 3 {
				t.Fatal(result.Code, result.Body.String(), calls)
			}
			// A scoped deployer may reuse the exact administrator-approved binding,
			// but must not switch repositories or Git connections through detection.
			appName := "detect-" + provider
			d, err := db.Accept(ctx, p, "demo", "development", spec.Application{Name: appName, Services: map[string]spec.Service{"web": {Image: "nginx:alpine", Port: 8080}}}, 0, "detect-binding-"+provider)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Pool.Exec(ctx, "INSERT INTO application_sources(application_id,provider,repository,branch,path,grant_id) VALUES($1,$2,'team/repo','main','hakopod.toml',$3)", d.ApplicationID, provider, p.KeyID); err != nil {
				t.Fatal(err)
			}
			input := map[string]any{"project": "demo", "environment": "development", "name": appName, "service": "web", "application_id": d.ApplicationID, "provider": provider, "repository": "team/repo", "branch": "main", "mode": "dockerfile"}
			body = store.JSON(input)
			if result = request(denied); result.Code != 200 || calls != 6 {
				t.Fatal("approved source detection failed", result.Code, result.Body.String(), calls)
			}
			input["repository"] = "other/private"
			body = store.JSON(input)
			if result = request(denied); result.Code != 403 || calls != 6 {
				t.Fatal("repository switch reached provider", result.Code, calls)
			}
			input["repository"] = "team/repo"
			input["connection_id"] = strings.Repeat("b", 32)
			body = store.JSON(input)
			if result = request(denied); result.Code != 403 || calls != 6 {
				t.Fatal("connection switch reached provider", result.Code, calls)
			}
		})
	}
}
