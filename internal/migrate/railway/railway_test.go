package railway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/migrate"
)

const token = "rw-secret-token-123"

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fake answers each query from a handler keyed by the first matching
// substring, and records the auth header that was used.
func fake(t *testing.T, project bool, answer func(r gqlRequest) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		bearer := r.Header.Get("Authorization") == "Bearer "+token
		projectHeader := r.Header.Get("Project-Access-Token") == token
		if strings.Contains(req.Query, "projectToken") {
			if project && projectHeader {
				w.Write([]byte(`{"data":{"projectToken":{"projectId":"p1","environmentId":"e1"}}}`))
			} else {
				w.Write([]byte(`{"errors":[{"message":"Not Authorized"}],"data":null}`))
			}
			return
		}
		if (project && !projectHeader) || (!project && !bearer) {
			w.Write([]byte(`{"errors":[{"message":"Not Authorized"}],"data":null}`))
			return
		}
		w.Write([]byte(answer(req)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListProjectsWithWorkspacesAndPages(t *testing.T) {
	srv := fake(t, false, func(r gqlRequest) string {
		switch {
		case strings.Contains(r.Query, "me {"):
			return `{"data":{"me":{"workspaces":[{"id":"w1","name":"Acme"}]}}}`
		case r.Variables["w"] == "w1":
			return `{"data":{"projects":{"edges":[{"node":{"id":"p3","name":"shop","environments":{"edges":[{"node":{"id":"e3","name":"production"}}]}}}],"pageInfo":{"hasNextPage":false}}}}`
		case r.Variables["after"] == nil:
			return `{"data":{"projects":{"edges":[{"node":{"id":"p1","name":"blog","environments":{"edges":[{"node":{"id":"e1","name":"production"}},{"node":{"id":"e2","name":"staging"}}]}}}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`
		default:
			return `{"data":{"projects":{"edges":[{"node":{"id":"p2","name":"api","environments":{"edges":[]}}},{"node":{"id":"p3","name":"shop","environments":{"edges":[]}}}],"pageInfo":{"hasNextPage":false}}}}`
		}
	})
	got, err := New(token, WithBaseURL(srv.URL)).ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []migrate.Project{
		{ID: "p3", Name: "shop", Team: "Acme", Environments: []migrate.Environment{{ID: "e3", Name: "production"}}},
		{ID: "p1", Name: "blog", Environments: []migrate.Environment{{ID: "e1", Name: "production"}, {ID: "e2", Name: "staging"}}},
		{ID: "p2", Name: "api", Environments: []migrate.Environment{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestListProjectsWithProjectToken(t *testing.T) {
	srv := fake(t, true, func(r gqlRequest) string {
		return `{"data":{"project":{"id":"p1","name":"blog","environments":{"edges":[{"node":{"id":"e1","name":"production"}},{"node":{"id":"e2","name":"staging"}}]}}}}`
	})
	got, err := New(token, WithBaseURL(srv.URL)).ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "p1" || len(got[0].Environments) != 1 || got[0].Environments[0].ID != "e1" {
		t.Fatalf("got %+v", got)
	}
}

const projectJSON = `{"data":{"project":{"id":"p1","name":"blog",
 "environments":{"edges":[{"node":{"id":"e1","name":"production"}}]},
 "services":{"edges":[{"node":{"id":"s-web","name":"web"}},{"node":{"id":"s-worker","name":"worker"}},{"node":{"id":"s-pg","name":"Postgres"}},{"node":{"id":"s-redis","name":"Redis"}},{"node":{"id":"s-cli","name":"uploaded"}},{"node":{"id":"s-other","name":"elsewhere"}}]},
 "volumes":{"edges":[
  {"node":{"name":"pg-data","volumeInstances":{"edges":[{"node":{"mountPath":"/var/lib/postgresql/data","serviceId":"s-pg","environmentId":"e1"}}]}}},
  {"node":{"name":"uploads","volumeInstances":{"edges":[{"node":{"mountPath":"/data","serviceId":"s-worker","environmentId":"e1"}},{"node":{"mountPath":"/data","serviceId":"s-worker","environmentId":"e9"}}]}}}]}}}}`

var serviceJSON = map[string]string{
	"s-web": `{"data":{
  "serviceInstance":{"source":{"repo":"acme/blog","image":null},"startCommand":"npm start","buildCommand":"npm run build","rootDirectory":"/app","healthcheckPath":"/health","numReplicas":2,"cronSchedule":null,"latestDeployment":{"meta":{"branch":"old"}}},
  "domains":{"serviceDomains":[{"domain":"web-production.up.railway.app","targetPort":8080}],"customDomains":[{"domain":"blog.example.com","targetPort":8080}]},
  "deploymentTriggers":{"edges":[{"node":{"branch":"main","repository":"acme/blog","provider":"github"}}]},
  "unrendered":{"DATABASE_URL":"${{Postgres.DATABASE_URL}}","PGHOST":"${{ Postgres.PGHOST }}","REDIS_URL":"${{Redis.REDIS_URL}}","LEGACY_URL":"postgres://${{Postgres.PGUSER}}@db/app","PORT":"8080","API_TOKEN":"abc","RAILWAY_ENVIRONMENT":"production","GREETING":"hi"},
  "rendered":{"DATABASE_URL":"postgresql://postgres:pw@postgres.railway.internal:5432/railway","PGHOST":"postgres.railway.internal","REDIS_URL":"redis://default:pw@redis.railway.internal:6379","LEGACY_URL":"postgres://postgres@db/app","PORT":"8080","API_TOKEN":"abc","RAILWAY_ENVIRONMENT":"production","GREETING":"hi"}}}`,
	"s-worker": `{"data":{
  "serviceInstance":{"source":{"repo":null,"image":"ghcr.io/acme/worker:1.4"},"startCommand":"./run-job","buildCommand":null,"rootDirectory":null,"healthcheckPath":null,"numReplicas":1,"cronSchedule":"*/15 * * * *","latestDeployment":null},
  "domains":{"serviceDomains":[],"customDomains":[]},
  "deploymentTriggers":{"edges":[]},
  "unrendered":{"MODE":"batch"},"rendered":{"MODE":"batch"}}}`,
	"s-pg": `{"data":{
  "serviceInstance":{"source":{"repo":null,"image":"ghcr.io/railwayapp-templates/postgres-ssl:16.4"},"numReplicas":1},
  "domains":{"serviceDomains":[],"customDomains":[]},"deploymentTriggers":{"edges":[]},
  "unrendered":{},"rendered":{"DATABASE_URL":"postgresql://postgres:pw@postgres.railway.internal:5432/railway","DATABASE_PUBLIC_URL":"postgresql://postgres:pw@roundhouse.proxy.rlwy.net:41234/railway"}}}`,
	"s-redis": `{"data":{
  "serviceInstance":{"source":{"repo":null,"image":"redis:7.2-alpine"},"numReplicas":1},
  "domains":{"serviceDomains":[],"customDomains":[]},"deploymentTriggers":{"edges":[]},
  "unrendered":{},"rendered":{"REDIS_URL":"redis://default:pw@redis.railway.internal:6379"}}}`,
	"s-cli":   `{"data":{"serviceInstance":{"source":null},"domains":{"serviceDomains":[],"customDomains":[]},"deploymentTriggers":{"edges":[]},"unrendered":{},"rendered":{}}}`,
	"s-other": `{"data":{"serviceInstance":null,"domains":null,"deploymentTriggers":{"edges":[]},"unrendered":{},"rendered":{}}}`,
}

func TestReadEnvironment(t *testing.T) {
	srv := fake(t, false, func(r gqlRequest) string {
		if strings.Contains(r.Query, "serviceInstance") {
			if r.Variables["e"] != "e1" || r.Variables["p"] != "p1" {
				t.Errorf("variables %v", r.Variables)
			}
			return serviceJSON[r.Variables["s"].(string)]
		}
		return projectJSON
	})
	inv, err := New(token, WithBaseURL(srv.URL)).Read(context.Background(), "p1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Provider != migrate.Railway || inv.ProjectName != "blog" || inv.Environment != "production" {
		t.Fatalf("header %+v", inv)
	}
	wantDBs := []migrate.Database{
		{Name: "Postgres", Engine: migrate.PostgreSQL, Version: "16", URL: "postgresql://postgres:pw@roundhouse.proxy.rlwy.net:41234/railway", Reachable: true},
		{Name: "Redis", Engine: migrate.Redis, Version: "7"},
	}
	if !reflect.DeepEqual(inv.Databases, wantDBs) {
		t.Fatalf("databases %+v", inv.Databases)
	}
	if len(inv.Services) != 2 {
		t.Fatalf("services %+v", inv.Services)
	}
	web, worker := inv.Services[0], inv.Services[1]
	wantWeb := migrate.Service{
		Name: "web", Repo: &migrate.Repo{Provider: "github", Owner: "acme", Name: "blog", Branch: "main", RootDir: "app"},
		BuildCommand: "npm run build", StartCommand: "npm start", Port: 8080, Public: true, Healthcheck: "/health", Replicas: 2,
		Domains: []string{"blog.example.com"},
		Variables: []migrate.Variable{
			{Name: "API_TOKEN", Value: "abc", Sensitive: true},
			{Name: "DATABASE_URL", Sensitive: true, DatabaseRef: &migrate.DatabaseRef{Database: "Postgres", Part: "url"}},
			{Name: "GREETING", Value: "hi"},
			{Name: "LEGACY_URL", Value: "postgres://postgres@db/app", Sensitive: true},
			{Name: "PGHOST", DatabaseRef: &migrate.DatabaseRef{Database: "Postgres", Part: "host"}},
			{Name: "PORT", Value: "8080"},
			{Name: "REDIS_URL", Sensitive: true, DatabaseRef: &migrate.DatabaseRef{Database: "Redis", Part: "url"}},
		},
	}
	if !reflect.DeepEqual(web, wantWeb) {
		t.Fatalf("web\n got %+v\nwant %+v", web, wantWeb)
	}
	wantWorker := migrate.Service{
		Name: "worker", Image: "ghcr.io/acme/worker:1.4", StartCommand: "./run-job", Replicas: 1,
		Variables: []migrate.Variable{{Name: "MODE", Value: "batch"}},
		Crons:     []migrate.Cron{{Schedule: "*/15 * * * *", Command: "./run-job"}},
		Volumes:   []string{"/data"},
	}
	if !reflect.DeepEqual(worker, wantWorker) {
		t.Fatalf("worker\n got %+v\nwant %+v", worker, wantWorker)
	}
	warnings := strings.Join(inv.Warnings, "\n")
	for _, want := range []string{"Redis has no public TCP proxy", "LEGACY_URL on web refers to Postgres", "uploaded was deployed from a local upload", "worker has a volume at /data"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing warning %q in:\n%s", want, warnings)
		}
	}
	if strings.Contains(warnings, "postgresql/data") {
		t.Errorf("database volumes need no warning:\n%s", warnings)
	}
}

func TestDatabaseImage(t *testing.T) {
	for image, want := range map[string][2]string{
		"ghcr.io/railwayapp-templates/postgres-ssl:17": {"postgresql", "17"},
		"postgres":                 {"postgresql", ""},
		"bitnami/redis:7.2.4":      {"redis", "7"},
		"mysql:8.0@sha256:abc":     {"mysql", "8"},
		"mongo:latest":             {"mongodb", ""},
		"ghcr.io/acme/app:1":       {"", ""},
		"ghcr.io/postgres-tools/x": {"", ""},
	} {
		e, v := databaseImage(image)
		if string(e) != want[0] || v != want[1] {
			t.Errorf("%s: got %s %s", image, e, v)
		}
	}
}

func TestAuthFailureNeverEchoesToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), token) {
			w.WriteHeader(http.StatusUnauthorized)
		}
		w.Write([]byte(`{"errors":[{"message":"Invalid token ` + token + `"}]}`))
	}))
	defer srv.Close()
	c := New(token, WithBaseURL(srv.URL))
	_, err := c.ListProjects(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	_, err = c.Read(context.Background(), "p1", "e1")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("got %v", err)
	}

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"Problem processing request ` + token + `"}]}`))
	}))
	defer echo.Close()
	_, err = New(token, WithBaseURL(echo.URL), WithProjectToken(false)).ListProjects(context.Background())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("got %v", err)
	}
}
