package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	dbmodel "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/migrate"
	"github.com/hakopod/hakopod/internal/store"
)

const migrationToken = "vc-secret-token-123"

// A Vercel project with a Next.js app from GitHub, Neon Postgres, Upstash
// Redis, two custom domains, a cron and two variables Vercel will not return.
var vercelMigrationRoutes = map[string]string{
	"/v10/projects?team_1": `{"projects":[{"id":"prj_1","name":"shop"}],"pagination":{"next":null}}`,
	"/v9/projects/prj_1?team_1": `{"id":"prj_1","name":"shop","framework":"nextjs","rootDirectory":"apps/web","installCommand":"pnpm install",
  "link":{"type":"github","org":"acme","repo":"shop","productionBranch":"main"},
  "crons":{"definitions":[{"path":"/api/cron/digest","schedule":"0 8 * * *"}]}}`,
	"/v10/projects/prj_1/env?team_1": `{"envs":[
  {"id":"e1","key":"POSTGRES_URL","type":"encrypted","target":["production"]},
  {"id":"e4","key":"KV_URL","type":"encrypted","target":["production"]},
  {"id":"e5","key":"KV_REST_API_TOKEN","type":"encrypted","target":["production"]},
  {"id":"e6","key":"STRIPE_SECRET_KEY","type":"sensitive","target":["production"]},
  {"id":"e7","key":"NEXT_PUBLIC_SITE","type":"plain","target":"production"},
  {"id":"e10","key":"GONE","type":"encrypted","target":["production"]}]}`,
	"/v1/projects/prj_1/env/e1?team_1":  `{"value":"postgres://u:dbpass@ep-cool-1.us-east-1.aws.neon.tech/neondb?sslmode=require","decrypted":true}`,
	"/v1/projects/prj_1/env/e4?team_1":  `{"value":"rediss://default:redispass@fine-cat-1.upstash.io:6379","decrypted":true}`,
	"/v1/projects/prj_1/env/e5?team_1":  `{"value":"kv-rest-value","decrypted":true}`,
	"/v1/projects/prj_1/env/e7?team_1":  `{"value":"https://shop.example.com"}`,
	"/v9/projects/prj_1/domains?team_1": `{"domains":[{"name":"shop.example.com","verified":true},{"name":"www.shop.example.com","verified":true}],"pagination":{"next":null}}`,
}

// Words that must never appear in an API response.
var migrationLeaks = []string{migrationToken, "dbpass", "redispass", "neon.tech", "upstash.io", "kv-rest-value", "sk_live_supplied", "gone-supplied"}

type migrationClusterFake struct {
	mu        sync.Mutex
	secrets   map[string]string
	copies    []cluster.CopyRequest
	cancelled []cluster.CopyID
	phase     string
	onCopy    func()
}

func (f *migrationClusterFake) PutWorkloadSecret(_ context.Context, project, environment, application, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[project+"/"+environment+"/"+application+"/"+name] = value
	return nil
}
func (f *migrationClusterFake) DatabaseControllerAvailable(context.Context, dbmodel.Spec) error {
	return nil
}
func (f *migrationClusterFake) ValidateDatabasePlacement(context.Context, dbmodel.Resource) error {
	return nil
}
func (f *migrationClusterFake) ObserveDatabase(_ context.Context, d dbmodel.Resource) (dbmodel.Observation, error) {
	return dbmodel.Observation{Status: "ready", Revision: d.Revision}, nil
}
func (f *migrationClusterFake) DatabaseEmpty(context.Context, dbmodel.Resource, dbmodel.Observation) error {
	return nil
}
func (f *migrationClusterFake) StartDatabaseCopy(_ context.Context, req cluster.CopyRequest) (cluster.CopyID, error) {
	f.mu.Lock()
	f.copies = append(f.copies, req)
	hook := f.onCopy
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return cluster.CopyID(req.Database.ID + "/copy"), nil
}
func (f *migrationClusterFake) DatabaseCopyStatus(context.Context, cluster.CopyID) (cluster.CopyStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cluster.CopyStatus{Phase: f.phase}, nil
}
func (f *migrationClusterFake) CancelDatabaseCopy(_ context.Context, id cluster.CopyID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return nil
}
func (f *migrationClusterFake) CleanupDatabaseCopies(context.Context, string) error { return nil }
func (f *migrationClusterFake) Nodes(context.Context) ([]cluster.Node, error) {
	return []cluster.Node{{Name: "one", Architecture: "amd64"}}, nil
}

type migrationFixture struct {
	t       *testing.T
	db      *store.Store
	s       *Server
	h       http.Handler
	key     string
	cluster *migrationClusterFake
	pulls   int
}

func newMigrationFixture(t *testing.T) *migrationFixture {
	t.Helper()
	db := sourceDatabase(t)
	raw, err := db.Bootstrap(context.Background(), "migration-operator")
	if err != nil {
		t.Fatal(err)
	}
	f := &migrationFixture{t: t, db: db, key: raw, cluster: &migrationClusterFake{secrets: map[string]string{}, phase: "succeeded"}}
	vercel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+migrationToken {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":"forbidden"}}`))
			return
		}
		body, ok := vercelMigrationRoutes[r.URL.Path+"?"+r.URL.Query().Get("teamId")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found"}}`))
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(vercel.Close)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/acme/shop":
			w.Write([]byte(`{"default_branch":"main"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/acme/shop/git/ref/heads/main":
			w.Write([]byte(`{"object":{"sha":"` + strings.Repeat("a", 40) + `"}}`))
		case r.Method == "POST" && r.URL.Path == "/repos/acme/shop/git/refs":
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/acme/shop/contents/.github/workflows/"):
			w.WriteHeader(404)
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/repos/acme/shop/contents/.github/workflows/"):
			w.WriteHeader(201)
			w.Write([]byte(`{"commit":{"sha":"` + strings.Repeat("b", 40) + `"}}`))
		case r.Method == "POST" && r.URL.Path == "/repos/acme/shop/pulls":
			f.pulls++
			w.WriteHeader(201)
			w.Write([]byte(`{"html_url":"https://github.com/acme/shop/pull/7"}`))
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(github.Close)
	migrationPoll, migrationHeartbeat = 10*time.Millisecond, time.Hour
	f.s = &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("ab", 32)}, migrationTestRuntime: f.cluster,
		migrationSourceURLs: map[string]string{"vercel": vercel.URL}, githubAPIURL: github.URL,
		githubTestCredentials: func(context.Context) (map[string][]byte, error) {
			return map[string][]byte{"token": []byte("local-github-token")}, nil
		}}
	f.h = f.s.Handler()
	// Stand in for RunManagedDatabases: every created database becomes ready.
	ctx, stop := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for ctx.Err() == nil {
			_, _ = db.Pool.Exec(ctx, "UPDATE managed_database_operations SET status='succeeded' WHERE status='queued'")
			_, _ = db.Pool.Exec(ctx, "UPDATE managed_databases SET status='ready' WHERE status='pending'")
			time.Sleep(5 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { stop(); <-finished })
	return f
}

func (f *migrationFixture) call(method, path, idem string, body any, want int) map[string]any {
	f.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(store.JSON(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "/api/v1"+path, reader)
	r.Header.Set("Authorization", "Bearer "+f.key)
	if idem != "" {
		r.Header.Set("Idempotency-Key", idem)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	for _, leak := range migrationLeaks {
		if strings.Contains(w.Body.String(), leak) {
			f.t.Fatalf("%s %s leaked %q: %s", method, path, leak, w.Body.String())
		}
	}
	if w.Code != want {
		f.t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *migrationFixture) plan(application string) string {
	f.t.Helper()
	m := f.call("POST", "/migrations", "", map[string]string{"provider": "vercel", "token": migrationToken, "source_project_id": "prj_1", "source_team_id": "team_1",
		"project": "demo", "environment": "development", "application_name": application}, 201)
	if m["status"] != "planned" || m["has_token"] != true {
		f.t.Fatal("unexpected plan", m)
	}
	id := m["id"].(string)
	m = f.call("PUT", "/migrations/"+id+"/variables", "", map[string]any{"values": map[string]map[string]string{"shop": {"GONE": "gone-supplied", "STRIPE_SECRET_KEY": "sk_live_supplied"}}}, 200)
	if missing := m["proposal"].(map[string]any)["missing"].([]any); len(missing) != 0 {
		f.t.Fatal("variables did not clear missing", missing)
	}
	return id
}

func (f *migrationFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestPlatformMigrationPlanStartAndRun(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	projects := f.call("POST", "/migrations/projects", "", map[string]string{"provider": "vercel", "token": migrationToken, "team_id": "team_1"}, 200)
	if list := projects["projects"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != "prj_1" {
		t.Fatal("projects", projects)
	}
	f.call("POST", "/migrations/projects", "", map[string]string{"provider": "vercel", "token": "wrong-token-value"}, 400)

	m := f.call("POST", "/migrations", "", map[string]string{"provider": "vercel", "token": migrationToken, "source_project_id": "prj_1", "source_team_id": "team_1",
		"project": "demo", "environment": "development", "application_name": "shop"}, 201)
	id := m["id"].(string)
	if missing := m["proposal"].(map[string]any)["missing"].([]any); len(missing) != 2 {
		t.Fatal("missing variables", missing)
	}
	f.call("POST", "/migrations/"+id+"/start", "start-key-0001", nil, 409)
	f.call("POST", "/migrations", "", map[string]string{"provider": "vercel", "token": migrationToken, "source_project_id": "prj_1", "source_team_id": "team_1",
		"project": "demo", "environment": "development", "application_name": "shop"}, 201) // a plan alone does not take the name

	f.call("PUT", "/migrations/"+id+"/variables", "", map[string]any{"values": map[string]map[string]string{"shop": {"GONE": "gone-supplied"}}}, 200)
	m = f.call("PUT", "/migrations/"+id+"/variables", "", map[string]any{"values": map[string]map[string]string{"shop": {"STRIPE_SECRET_KEY": "sk_live_supplied"}}}, 200)
	if missing := m["proposal"].(map[string]any)["missing"].([]any); len(missing) != 0 {
		t.Fatal("earlier values were not kept", missing)
	}

	first := f.call("POST", "/migrations/"+id+"/start", "start-key-0001", nil, 200)
	again := f.call("POST", "/migrations/"+id+"/start", "start-key-0001", nil, 200)
	if first["status"] != "queued" || again["status"] != "queued" {
		t.Fatal("start was not idempotent", first, again)
	}
	f.call("POST", "/migrations/"+id+"/start", "start-key-0002", nil, 409)
	if n := f.count("SELECT count(*) FROM audit_events WHERE action='migration.start' AND resource=$1", id); n != 1 {
		t.Fatal("start audited", n)
	}

	if !f.s.runNextPlatformMigration(ctx) {
		t.Fatal("nothing claimed")
	}
	m = f.call("GET", "/migrations/"+id, "", nil, 200)
	if m["status"] != "succeeded" || m["has_token"] != false || m["application_id"] == "" {
		t.Fatal("migration did not succeed", m)
	}
	for _, st := range m["steps"].([]any) {
		step := st.(map[string]any)
		if step["status"] != "succeeded" && step["status"] != "skipped" {
			t.Fatal("step", step)
		}
	}
	token, variables, err := f.db.PlatformMigrationSecrets(ctx, id)
	if err != nil || token != nil || variables != nil {
		t.Fatal("token kept after finish", err)
	}
	if n := f.count("SELECT count(*) FROM api_keys WHERE id=$1 AND revoked_at IS NULL", id); n != 0 {
		t.Fatal("migration grant still active")
	}
	if n := f.count("SELECT count(*) FROM managed_databases WHERE project='demo' AND environment='development'"); n != 2 {
		t.Fatal("databases", n)
	}
	if len(f.cluster.copies) != 2 || f.cluster.copies[0].SourceURL == "" {
		t.Fatal("copies", len(f.cluster.copies))
	}
	stripe := false
	for _, v := range f.cluster.secrets {
		stripe = stripe || v == "sk_live_supplied"
	}
	if !stripe {
		t.Fatal("supplied secret was not written", f.cluster.secrets)
	}
	appID := m["application_id"].(string)
	app, err := f.db.Application(ctx, appID)
	if err != nil || app.Name != "shop" || !app.Spec.Services["shop"].Suspended || len(app.Spec.Services["shop"].Bindings) != 2 {
		t.Fatal("application", app.Spec.Services["shop"], err)
	}
	if n := f.count("SELECT count(*) FROM build_configs WHERE application_id=$1 AND service='shop' AND installed_revision=revision", appID); n != 1 || f.pulls != 1 {
		t.Fatal("build configs", n, f.pulls)
	}
	if n := f.count("SELECT count(*) FROM domain_verifications WHERE application_id=$1", appID); n != 2 {
		t.Fatal("domains", n)
	}

	// The service's first built image starts it.
	var c buildConfig
	if err = f.db.Pool.QueryRow(ctx, "SELECT id FROM build_configs WHERE application_id=$1", appID).Scan(&c.ID); err != nil {
		t.Fatal(err)
	}
	if c, err = f.s.readBuild(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, "UPDATE deployments SET resolved_spec=spec WHERE application_id=$1", appID); err != nil {
		t.Fatal(err)
	}
	built, _, err := f.s.prepareBuildSpec(ctx, c, buildRun{Status: "completed", Conclusion: "success", Image: "ghcr.io/acme/shop@sha256:" + strings.Repeat("c", 64), ConfigRevision: c.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if built.Services["shop"].Suspended || built.Services["shop"].Image == migrate.PlaceholderImage {
		t.Fatal("first build did not start the migrated service")
	}
}

func TestPlatformMigrationCancel(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	queued := f.plan("queued-app")
	f.call("POST", "/migrations/"+queued+"/start", "start-key-0001", nil, 200)
	m := f.call("POST", "/migrations/"+queued+"/cancel", "", nil, 200)
	if m["status"] != "cancelled" || m["has_token"] != false {
		t.Fatal("cancel", m)
	}
	f.call("POST", "/migrations/"+queued+"/cancel", "", nil, 409)

	// A running migration stops its copy at once and finishes as cancelled.
	running := f.plan("running-app")
	f.call("POST", "/migrations/"+running+"/start", "start-key-0002", nil, 200)
	claimed, err := f.db.ClaimPlatformMigration(ctx, "lease-a")
	if err != nil || claimed.ID != running {
		t.Fatal(claimed.ID, err)
	}
	steps := []migrationStep{{Name: "data", Status: "running", Items: []migrationItem{{Name: "db", Status: "running", Ref: "abc/copy"}}}}
	if err = f.db.RecordPlatformMigrationSteps(ctx, running, "lease-a", store.JSON(steps), ""); err != nil {
		t.Fatal(err)
	}
	if m = f.call("POST", "/migrations/"+running+"/cancel", "", nil, 200); m["cancel_requested"] != true {
		t.Fatal("cancel request", m)
	}
	if len(f.cluster.cancelled) != 1 || f.cluster.cancelled[0] != "abc/copy" {
		t.Fatal("copy not cancelled", f.cluster.cancelled)
	}
	f.s.runPlatformMigration(ctx, claimed, "lease-a")
	if m = f.call("GET", "/migrations/"+running, "", nil, 200); m["status"] != "cancelled" || m["has_token"] != false {
		t.Fatal("running cancel", m)
	}
}

func TestPlatformMigrationResumesWithoutDuplicates(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	id := f.plan("resumed")
	f.call("POST", "/migrations/"+id+"/start", "start-key-0001", nil, 200)

	// The first worker stops (as on shutdown) while the first copy runs.
	f.cluster.phase = "running"
	first, stop := context.WithCancel(ctx)
	f.cluster.onCopy = stop
	if !f.s.runNextPlatformMigration(first) {
		t.Fatal("nothing claimed")
	}
	if m := f.call("GET", "/migrations/"+id, "", nil, 200); m["status"] != "running" {
		t.Fatal("first run", m["status"])
	}
	if _, err := f.db.Pool.Exec(ctx, "UPDATE platform_migrations SET lease_until=now()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}

	f.cluster.phase, f.cluster.onCopy = "succeeded", nil
	if !f.s.runNextPlatformMigration(ctx) {
		t.Fatal("lapsed lease was not reclaimed")
	}
	if m := f.call("GET", "/migrations/"+id, "", nil, 200); m["status"] != "succeeded" {
		t.Fatal("resume", m)
	}
	if n := f.count("SELECT count(*) FROM managed_databases"); n != 2 {
		t.Fatal("databases duplicated", n)
	}
	if len(f.cluster.copies) != 2 {
		t.Fatal("copy restarted", len(f.cluster.copies))
	}
	if n := f.count("SELECT count(*) FROM build_configs"); n != 1 {
		t.Fatal("builds duplicated", n)
	}
}
