package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

// A separate temporary database makes these real transactional checks safe to
// run alongside a live development platform. Unit-only CI can skip explicitly.
func database(t *testing.T) (*store.Store, string) {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HAKOPOD_TEST_DATABASE_URL for real PostgreSQL acceptance")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_test_" + store.NewID()
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	testDSN := u.String()
	db, err := store.Open(ctx, testDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); conn.Close(ctx) })
	return db, testDSN
}
func TestDurabilityAuthorizationAndConcurrency(t *testing.T) {
	db, dsn := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "integration-operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Bootstrap(ctx, "intruder"); err == nil {
		t.Fatal("bootstrap must close permanently after initial owner")
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: db}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any, idem string) (int, map[string]any) {
		t.Helper()
		var data []byte
		if body != nil {
			data = store.JSON(body)
		}
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(data))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var out map[string]any
		if err = json.Unmarshal(b, &out); err != nil {
			t.Fatalf("non-JSON response status%d: %s", res.StatusCode, b)
		}
		return res.StatusCode, out
	}
	if status, _ := request("GET", "/projects", "", nil, ""); status != 401 {
		t.Fatalf("unauthorized got%d", status)
	}
	if status, _ := request("GET", "/projects", raw+"bad", nil, ""); status != 401 {
		t.Fatalf("corrupt key got%d", status)
	}
	input := store.KeyInput{Name: "ci", Project: "demo", Environment: "development", Permissions: []string{"deployments:write", "deployments:read", "logs:read"}, ExpiresAt: time.Now().Add(time.Hour)}
	key, ci, err := db.CreateKey(ctx, p, input)
	if err != nil {
		t.Fatal(err)
	}
	ciPrincipal, err := db.Authenticate(ctx, ci)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := request("GET", "/nodes", ci, nil, ""); status != 403 {
		t.Fatalf("CI node access got%d", status)
	}
	if status, _ := request("GET", "/keys", ci, nil, ""); status != 403 {
		t.Fatalf("CI key list got%d", status)
	}
	if status, _ := request("POST", "/keys", ci, input, ""); status != 403 {
		t.Fatalf("CI key escalation got%d", status)
	}
	if status, _ := request("GET", "/applications?project=other&environment=production", ci, nil, ""); status != 403 {
		t.Fatalf("cross-project read got%d", status)
	}
	if status, me := request("GET", "/me", ci, nil, ""); status != 200 || me["admin"] != false {
		t.Fatalf("effective admin leaked: %v", me)
	}
	var digest []byte
	var prefix string
	if err = db.Pool.QueryRow(ctx, "SELECT digest,prefix FROM api_keys WHERE id=$1", key.ID).Scan(&digest, &prefix); err != nil {
		t.Fatal(err)
	}
	if len(digest) != 32 || bytes.Contains(digest, []byte(ci)) || strings.Contains(prefix, ci) {
		t.Fatal("plaintext key stored")
	}
	next, err := spec.Parse([]byte("schema_version=1\nname='test'\n[services.worker]\nimage='python:3.13-alpine'\n"))
	if err != nil {
		t.Fatal(err)
	}
	plan := map[string]any{"project": "demo", "environment": "development", "spec": next}
	if status, _ := request("POST", "/deployments", ci, plan, "missing-revision"); status != 400 {
		t.Fatalf("missing revision got%d", status)
	}
	invalid := map[string]any{"project": "demo", "environment": "development", "toml": "name='bad'\n[services.web]\nimage='nginx:alpine'\nprivileged=true"}
	if status, _ := request("POST", "/plan", ci, invalid, ""); status != 400 {
		t.Fatalf("unknown TOML accepted: %d", status)
	}
	const callers = 8
	results := make(chan store.Deployment, callers)
	failures := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := db.Accept(ctx, ciPrincipal, "demo", "development", next, 0, "concurrent-same-key")
			if e != nil {
				failures <- e
			} else {
				results <- d
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	first := ""
	for d := range results {
		if first == "" {
			first = d.ID
		}
		if d.ID != first || d.Revision != 1 {
			t.Fatal("duplicate request created another revision")
		}
	}
	if _, err = db.Accept(ctx, ciPrincipal, "demo", "development", next, 0, "stale-other-key"); err == nil {
		t.Fatal("stale revision accepted")
	}
	altered := next
	altered.Name = "changed"
	if _, err = db.Accept(ctx, ciPrincipal, "demo", "development", altered, 0, "concurrent-same-key"); err == nil {
		t.Fatal("idempotency key reused for different input")
	}
	app, err := db.FindApplication(ctx, "demo", "development", "test")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := db.Accept(ctx, ciPrincipal, "demo", "development", next, 1, "queued-second-release")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	if claim.Deployment.ID != first {
		t.Fatal("out-of-order application work")
	}
	if competing, err := db.Claim(ctx); err != nil || competing != nil {
		if competing != nil {
			competing.Release()
		}
		t.Fatal("same application claimed concurrently")
	}
	claim.Release()
	// New pool emulates reconnect after process interruption. The durable running
	// operation must be reclaimed before the later queued operation.
	reopened, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed, err := reopened.Claim(ctx)
	if err != nil || resumed == nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Deployment.ID != first {
		t.Fatal("lost running operation on restart")
	}
	if err = resumed.Finish(ctx, "succeeded", "", map[string]any{"status": "healthy"}); err != nil {
		t.Fatal(err)
	}
	resumed.Release()
	second, err := db.Claim(ctx)
	if err != nil || second == nil || second.Deployment.ID != d2.ID {
		t.Fatalf("queued followup lost: %v", err)
	}
	second.Release()
	if status, _ := request("POST", "/deployments/"+d2.ID+"/cancel", ci, map[string]any{}, ""); status != 202 {
		t.Fatalf("cancel got%d", status)
	}
	d, err := db.Deployment(ctx, d2.ID)
	if err != nil || !d.CancelRequested {
		t.Fatal("cancel request not durable")
	}
	// Application restrictions apply to direct resource reads as well as lists.
	in2 := input
	in2.Application = "different-app"
	in2.Name = "app-restricted"
	_, restricted, err := db.CreateKey(ctx, p, in2)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := request("GET", "/applications/"+app.ID, restricted, nil, ""); status != 403 {
		t.Fatalf("application boundary got%d", status)
	}
	if status, _ := request("GET", "/deployments/"+d2.ID, restricted, nil, ""); status != 403 {
		t.Fatalf("deployment resource boundary got%d", status)
	}
	// Running operations consult current owner rights, key expiration and revoke
	// state; they do not rely on acceptance-time snapshots.
	if err = db.Reauthorize(ctx, key.ID, "demo", "development", "test"); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Pool.Exec(ctx, "UPDATE identities SET disabled=true WHERE id=$1", p.ID)
	if _, err = db.Authenticate(ctx, ci); err == nil {
		t.Fatal("disabled identity key still authenticates")
	}
	if err = db.Reauthorize(ctx, key.ID, "demo", "development", "test"); err == nil {
		t.Fatal("running operation ignores disabled identity")
	}
	_, _ = db.Pool.Exec(ctx, "UPDATE identities SET disabled=false,admin=false,permissions=ARRAY['deployments:read'],project='demo',environment='development' WHERE id=$1", p.ID)
	if err = db.Reauthorize(ctx, key.ID, "demo", "development", "test"); err == nil {
		t.Fatal("owner permission reduction ignored")
	}
	_, _ = db.Pool.Exec(ctx, "UPDATE identities SET admin=true,project='',environment='' WHERE id=$1", p.ID)
	_, _ = db.Pool.Exec(ctx, "UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE id=$1", key.ID)
	if _, err = db.Authenticate(ctx, ci); err == nil {
		t.Fatal("expired key accepted")
	}
	_, _ = db.Pool.Exec(ctx, "UPDATE api_keys SET expires_at=now()+interval '1 hour',revoked_at=now() WHERE id=$1", key.ID)
	if _, err = db.Authenticate(ctx, ci); err == nil {
		t.Fatal("revoked key accepted")
	}
	if err = db.Reauthorize(ctx, key.ID, "demo", "development", "test"); err == nil {
		t.Fatal("revoked key still authorizes running work")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployments WHERE application_id=$1", app.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("immutable release count =%d err%v", count, err)
	}
	t.Log(fmt.Sprintf("verified real PostgreSQL: %d duplicate callers, serialized releases, recovery, scoped/expired/revoked/disabled credentials", callers))
}
