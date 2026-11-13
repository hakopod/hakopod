package vercel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/migrate"
)

const token = "vc-secret-token-123"

// fake serves routes keyed by "path?teamId" and fails anything else.
func fake(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"forbidden","message":"Not authorized: ` + token + `"}}`))
			return
		}
		key := r.URL.Path + "?" + r.URL.Query().Get("teamId")
		if from := r.URL.Query().Get("from"); from != "" {
			key += "&from=" + from
		}
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found"}}`))
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListProjectsWithTeamsAndPages(t *testing.T) {
	srv := fake(t, map[string]string{
		"/v2/teams?":                        `{"teams":[{"id":"team_1","slug":"acme","name":"Acme"}],"pagination":{"next":null}}`,
		"/v10/projects?":                    `{"projects":[{"id":"prj_1","name":"blog"}],"pagination":{"count":1,"next":1700000000000,"prev":null}}`,
		"/v10/projects?&from=1700000000000": `{"projects":[{"id":"prj_2","name":"docs"}],"pagination":{"count":1,"next":null,"prev":null}}`,
		"/v10/projects?team_1":              `{"projects":[{"id":"prj_3","name":"shop"}],"pagination":{"count":1,"next":null}}`,
	})
	got, err := New(token, WithBaseURL(srv.URL)).ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []migrate.Project{{ID: "prj_1", Name: "blog"}, {ID: "prj_2", Name: "docs"}, {ID: "prj_3", Name: "shop", Team: "Acme"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

const teamID = "team_1"

var fullProject = map[string]string{
	"/v9/projects/prj_1?" + teamID: `{"id":"prj_1","name":"shop","framework":"nextjs","rootDirectory":"apps/web","buildCommand":null,"installCommand":"pnpm install","outputDirectory":null,
  "link":{"type":"github","org":"acme","repo":"shop","productionBranch":"main","repoId":1},
  "crons":{"enabledAt":1,"definitions":[{"host":"shop.vercel.app","path":"/api/cron/digest","schedule":"0 8 * * *"}]}}`,
	"/v10/projects/prj_1/env?" + teamID: `{"envs":[
  {"id":"e1","key":"POSTGRES_URL","type":"encrypted","target":["production","preview"]},
  {"id":"e2","key":"POSTGRES_URL_NON_POOLING","type":"encrypted","target":["production"]},
  {"id":"e3","key":"POSTGRES_PASSWORD","type":"encrypted","target":["production"]},
  {"id":"e4","key":"KV_URL","type":"encrypted","target":["production"]},
  {"id":"e5","key":"KV_REST_API_TOKEN","type":"encrypted","target":["production"]},
  {"id":"e6","key":"STRIPE_SECRET_KEY","type":"sensitive","target":["production"]},
  {"id":"e7","key":"NEXT_PUBLIC_SITE","type":"plain","target":"production"},
  {"id":"e8","key":"PREVIEW_ONLY","type":"plain","target":["preview"]},
  {"id":"e9","key":"VERCEL_URL","type":"system","target":["production"]},
  {"id":"e10","key":"GONE","type":"encrypted","target":["production"]}]}`,
	"/v1/projects/prj_1/env/e1?" + teamID: `{"value":"postgres://u:pw@ep-cool-1-pooler.us-east-1.aws.neon.tech/neondb?sslmode=require","decrypted":true}`,
	"/v1/projects/prj_1/env/e2?" + teamID: `{"value":"postgres://u:pw@ep-cool-1.us-east-1.aws.neon.tech/neondb?sslmode=require","decrypted":true}`,
	"/v1/projects/prj_1/env/e3?" + teamID: `{"value":"pw","decrypted":true}`,
	"/v1/projects/prj_1/env/e4?" + teamID: `{"value":"rediss://default:tok@fine-cat-1.upstash.io:6379","decrypted":true}`,
	"/v1/projects/prj_1/env/e5?" + teamID: `{"value":"tok","decrypted":true}`,
	"/v1/projects/prj_1/env/e7?" + teamID: `{"value":"https://shop.example.com"}`,
	"/v9/projects/prj_1/domains?" + teamID: `{"domains":[
  {"name":"shop.example.com","verified":true},
  {"name":"www.shop.example.com","verified":false,"redirect":"shop.example.com"},
  {"name":"shop.vercel.app","verified":true},
  {"name":"next.shop.example.com","verified":true,"gitBranch":"next"}],"pagination":{"next":null}}`,
}

func TestReadFullProject(t *testing.T) {
	srv := fake(t, fullProject)
	inv, err := New(token, WithBaseURL(srv.URL), WithTeam(teamID)).Read(context.Background(), "prj_1", "")
	if err != nil {
		t.Fatal(err)
	}
	wantDBs := []migrate.Database{
		{Name: "redis", Engine: migrate.Redis, URL: "rediss://default:tok@fine-cat-1.upstash.io:6379", Reachable: true},
		{Name: "postgres", Engine: migrate.PostgreSQL, URL: "postgres://u:pw@ep-cool-1.us-east-1.aws.neon.tech/neondb?sslmode=require", Reachable: true},
	}
	if !reflect.DeepEqual(inv.Databases, wantDBs) {
		t.Fatalf("databases %+v", inv.Databases)
	}
	want := migrate.Service{
		Name: "shop", Repo: &migrate.Repo{Provider: "github", Owner: "acme", Name: "shop", Branch: "main", RootDir: "apps/web"},
		Framework: "nextjs", InstallCommand: "pnpm install", Port: 3000, Public: true,
		Domains: []string{"shop.example.com", "www.shop.example.com"},
		Crons:   []migrate.Cron{{Schedule: "0 8 * * *", Path: "/api/cron/digest"}},
		Variables: []migrate.Variable{
			{Name: "GONE", Unreadable: true, Sensitive: true},
			{Name: "KV_REST_API_TOKEN", Value: "tok", Sensitive: true},
			{Name: "KV_URL", DatabaseRef: &migrate.DatabaseRef{Database: "redis", Part: "url"}, Sensitive: true},
			{Name: "NEXT_PUBLIC_SITE", Value: "https://shop.example.com"},
			{Name: "POSTGRES_PASSWORD", DatabaseRef: &migrate.DatabaseRef{Database: "postgres", Part: "password"}, Sensitive: true},
			{Name: "POSTGRES_URL", DatabaseRef: &migrate.DatabaseRef{Database: "postgres", Part: "url"}, Sensitive: true},
			{Name: "POSTGRES_URL_NON_POOLING", DatabaseRef: &migrate.DatabaseRef{Database: "postgres", Part: "url"}, Sensitive: true},
			{Name: "STRIPE_SECRET_KEY", Unreadable: true, Sensitive: true},
		},
	}
	if len(inv.Services) != 1 || !reflect.DeepEqual(inv.Services[0], want) {
		t.Fatalf("services\n got %+v\nwant %+v", inv.Services, want)
	}
	warnings := strings.Join(inv.Warnings, "\n")
	for _, w := range []string{"Upstash REST", "Next.js middleware"} {
		if !strings.Contains(warnings, w) {
			t.Errorf("missing %q in %s", w, warnings)
		}
	}
}

func TestReadFindsTeamAndSkipsNonGitHub(t *testing.T) {
	srv := fake(t, map[string]string{
		"/v2/teams?":                     `{"teams":[{"id":"team_1","name":"Acme"}]}`,
		"/v9/projects/prj_9?team_1":      `{"id":"prj_9","name":"legacy","link":{"type":"gitlab","org":"acme","repo":"legacy"}}`,
		"/v10/projects/prj_9/env?team_1": `{"envs":[{"id":"e1","key":"TOKEN","type":"sensitive","target":["production"]}]}`,
	})
	inv, err := New(token, WithBaseURL(srv.URL)).Read(context.Background(), "prj_9", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Services) != 0 || len(inv.Warnings) != 1 || !strings.Contains(inv.Warnings[0], "gitlab repository") {
		t.Fatalf("got %+v", inv)
	}
}

func TestAuthFailureIsClearAndHidesToken(t *testing.T) {
	srv := fake(t, nil)
	c := New("wrong-"+token, WithBaseURL(srv.URL))
	_, err := c.ListProjects(context.Background())
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), token) {
		t.Fatalf("got %v", err)
	}
	_, err = c.Read(context.Background(), "prj_1", "")
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), token) {
		t.Fatalf("got %v", err)
	}
	// A server error keeps the code and drops the message.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"code":"internal","message":"` + token + `"}}`))
	}))
	defer bad.Close()
	_, err = New(token, WithBaseURL(bad.URL), WithTeam(teamID)).ListProjects(context.Background())
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("got %v", err)
	}
}
