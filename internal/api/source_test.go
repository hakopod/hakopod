package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sourceDatabase(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HAKOPOD_TEST_DATABASE_URL for isolated PostgreSQL source-inbox tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "hakopod_source_test_" + store.NewID()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(cleanup)
	})
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSourceInboxReviewAuthorityAndRecovery(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "source-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	application, _ := spec.Normalize(spec.Application{Name: "source-test", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Port: 8080}}})
	first, err := db.Accept(ctx, p, "demo", "development", application, 0, "source-initial-01")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	contents := `schema_version=1
name="source-test"
[services.web]
image="python:3.13-alpine"
port=8080
`
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			write(w, 200, map[string]string{"sha": commit})
			return
		}
		if r.URL.Query().Get("ref") != commit {
			t.Error("source content was not fetched at the reviewed immutable commit")
		}
		write(w, 200, map[string]any{"type": "file", "encoding": "base64", "size": len(contents), "content": base64.StdEncoding.EncodeToString([]byte(contents))})
	}))
	defer github.Close()
	secret := bytes.Repeat([]byte{9}, 32)
	server := &Server{Store: db, githubAPIURL: github.URL, githubHTTP: github.Client(), githubTestCredentials: func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"webhook-secret": secret}, nil
	}}
	handler := server.Handler()
	call := func(method, path string, input any, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "source-review-test")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("%s %s got %d: %s", method, path, out.Code, out.Body.String())
		}
		var body map[string]any
		_ = json.Unmarshal(out.Body.Bytes(), &body)
		return body
	}
	sourcePath := "/applications/" + first.ApplicationID + "/source"
	input := map[string]any{"repository": "example/source", "branch": "main", "path": "hakopod.toml", "auto_deploy": true, "expected_source_revision": 0}
	call("PUT", sourcePath, input, 200)
	call("PUT", sourcePath, input, 409)
	var grants int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE kind='integration'").Scan(&grants)
	if grants != 1 {
		t.Fatal("conflicted source save leaked a deployment grant")
	}
	plan := call("POST", sourcePath+"/plan", map[string]any{}, 200)
	if plan["commit_sha"] != commit || plan["expected_source_revision"] != float64(1) {
		t.Fatal("plan did not pin both source revision and commit")
	}
	webhook := func(delivery string, valid bool) {
		t.Helper()
		body := store.JSON(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]string{"full_name": "example/source"}})
		signature := hmac.New(sha256.New, secret)
		signature.Write(body)
		sig := hex.EncodeToString(signature.Sum(nil))
		if !valid {
			sig = strings.Repeat("0", 64)
		}
		r := httptest.NewRequest("POST", "http://localhost/api/v1/webhooks/github", bytes.NewReader(body))
		r.Header.Set("X-Hub-Signature-256", "sha256="+sig)
		r.Header.Set("X-GitHub-Event", "push")
		r.Header.Set("X-GitHub-Delivery", delivery)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		want := 202
		if !valid {
			want = 401
		}
		if out.Code != want {
			t.Fatalf("webhook got %d: %s", out.Code, out.Body.String())
		}
	}
	webhook("invalid-delivery", false)
	webhook("first-delivery-01", true)
	webhook("first-delivery-01", true)
	var count int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_jobs").Scan(&count)
	if count != 1 {
		t.Fatal("webhook duplicate not suppressed")
	}
	server.runSource(ctx)
	var status string
	_ = db.Pool.QueryRow(ctx, "SELECT status FROM source_jobs").Scan(&status)
	if status != "accepted" {
		var errorText string
		_ = db.Pool.QueryRow(ctx, "SELECT error FROM source_jobs").Scan(&errorText)
		t.Fatalf("inbox did not accept: %s %s", status, errorText)
	}
	// Simulate the crash window after Accept commits but before source job finish.
	_, _ = db.Pool.Exec(ctx, "UPDATE source_jobs SET status='running',claimed_at=now()-interval '3 minutes'")
	server.runSource(ctx)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployments").Scan(&count)
	if count != 2 {
		t.Fatal("crash recovery duplicated the accepted release")
	}
	webhook("second-delivery-02", true)
	_, _ = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE kind='integration'")
	server.runSource(ctx)
	_ = db.Pool.QueryRow(ctx, "SELECT status FROM source_jobs WHERE delivery_id='second-delivery-02'").Scan(&status)
	if status != "failed" {
		t.Fatal("revoked deployment grant retained automatic authority")
	}
	call("PATCH", "/settings/appearance", map[string]string{"accent_color": "#ff"}, 400)
	call("PATCH", "/settings/appearance", map[string]string{"accent_color": "#ABCDEF"}, 200)
	if call("GET", "/settings/appearance", nil, 200)["accent_color"] != "#abcdef" {
		t.Fatal("appearance setting did not persist")
	}
}

func TestSourceValidation(t *testing.T) {
	for _, path := range []string{"../hakopod.toml", "/hakopod.toml", "a/../hakopod.toml", "a\\hakopod.toml", "hakopod.yaml"} {
		if validSource(sourceBinding{Repository: "owner/repo", Branch: "main", Path: path}) {
			t.Fatalf("unsafe source path accepted: %s", path)
		}
	}
}

func TestSourceRepositoryApprovalAndBuildInstallationBoundary(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Repository Test Owner", "repository-owner@example.test", "repository test owner password", "")
	if err != nil {
		t.Fatal(err)
	}
	adminSession, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, invite, err := db.CreateInvite(ctx, adminSession.User, "repository-deployer@example.test", "", "demo", "developer")
	if err != nil {
		t.Fatal(err)
	}
	deployerID, err := db.AcceptInvite(ctx, invite, "Repository Test Deployer", "repository test deployer password", nil)
	if err != nil {
		t.Fatal(err)
	}
	deployerSession, err := db.NewSession(ctx, deployerID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if deployerSession.User.IsAdmin() || !deployerSession.User.Allows("deployments:write", "demo", "development", "repository-test") {
		t.Fatal("fixture did not create a project deployer without global authority")
	}
	application, err := spec.Normalize(spec.Application{Name: "repository-test", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Port: 8080}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.Accept(ctx, adminSession.User, "demo", "development", application, 0, "repository-initial-release")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	contents := "schema_version=1\nname=\"repository-test\"\n[services.web]\nimage=\"python:3.13-alpine\"\nport=8080\n"
	var credentialReads, repositoryReads atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repositoryReads.Add(1)
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/repos/private/approved/") || r.Header.Get("Authorization") != "Bearer local-private-repository-token" {
			t.Error("request escaped the approved private repository or attempted a write")
			http.Error(w, "forbidden fixture request", 403)
			return
		}
		if r.URL.Path == "/repos/private/approved/commits/release" {
			write(w, 200, map[string]string{"sha": commit})
			return
		}
		if r.URL.Path != "/repos/private/approved/contents/config/release.toml" || r.URL.Query().Get("ref") != commit {
			t.Error("approved repository plan did not use the chosen branch/path and immutable commit")
		}
		write(w, 200, map[string]any{"type": "file", "encoding": "base64", "size": len(contents), "content": base64.StdEncoding.EncodeToString([]byte(contents))})
	}))
	defer github.Close()
	server := &Server{Store: db, githubAPIURL: github.URL, githubHTTP: github.Client(), githubTestCredentials: func(context.Context) (map[string][]byte, error) {
		credentialReads.Add(1)
		return map[string][]byte{"token": []byte("local-private-repository-token"), "webhook-secret": bytes.Repeat([]byte{9}, 32)}, nil
	}}
	handler := server.Handler()
	call := func(method, path, token string, input any, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "repository-approval-test")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, out.Code, want, out.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	binding := func(repository, branch, path string, automatic bool, revision int64) map[string]any {
		return map[string]any{"repository": repository, "branch": branch, "path": path, "auto_deploy": automatic, "expected_source_revision": revision}
	}
	sourcePath := "/applications/" + first.ApplicationID + "/source"
	for _, automatic := range []bool{false, true} {
		denied := call("PUT", sourcePath, deployerSession.Token, binding("private/unapproved", "main", "hakopod.toml", automatic, 0), 403)
		if !strings.Contains(string(store.JSON(denied)), "repository_approval_required") {
			t.Fatal("first project binding did not explain the missing repository approval")
		}
	}
	var grants int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE kind='integration'").Scan(&grants); err != nil || grants != 0 || credentialReads.Load() != 0 {
		t.Fatal("unauthorized first binding accessed integration credentials or created a grant")
	}
	call("PUT", sourcePath, adminSession.Token, binding("Private/Approved", "main", "hakopod.toml", true, 0), 200)
	approved, err := server.readSource(ctx, first.ApplicationID)
	if err != nil || approved.Repository != "private/approved" || approved.Revision != 1 {
		t.Fatalf("admin approval was not normalized and persisted: %+v %v", approved, err)
	}
	call("PUT", sourcePath, deployerSession.Token, binding("PRIVATE/APPROVED", "release", "config/release.toml", false, 1), 200)
	current, err := server.readSource(ctx, first.ApplicationID)
	if err != nil || current.Repository != "private/approved" || current.Revision != 2 || current.Branch != "release" || current.Path != "config/release.toml" || current.AutoDeploy {
		t.Fatalf("project deployer could not change approved branch/path/automatic settings: %+v %v", current, err)
	}
	beforeDenied := credentialReads.Load()
	call("PUT", sourcePath, deployerSession.Token, binding("private/unapproved", "main", "hakopod.toml", true, 2), 403)
	afterDenied, err := server.readSource(ctx, first.ApplicationID)
	if err != nil || afterDenied.Repository != current.Repository || afterDenied.Revision != current.Revision || afterDenied.GrantID != current.GrantID || credentialReads.Load() != beforeDenied {
		t.Fatal("rejected repository switch changed the approval or accessed integration credentials")
	}
	plan := call("POST", sourcePath+"/plan", deployerSession.Token, map[string]any{}, 200)
	if plan["commit_sha"] != commit || plan["expected_source_revision"] != float64(2) || repositoryReads.Load() != 2 {
		t.Fatal("project deployer could not read the reviewed private repository plan")
	}
	call("PUT", sourcePath, adminSession.Token, binding("Private/Replacement", "main", "hakopod.toml", false, 2), 200)
	call("PUT", sourcePath, deployerSession.Token, binding("private/approved", "release", "config/release.toml", false, 2), 409)
	call("PUT", sourcePath, deployerSession.Token, binding("private/approved", "release", "config/release.toml", false, 3), 403)
	call("PUT", sourcePath, deployerSession.Token, binding("PRIVATE/REPLACEMENT", "production", "config/prod.toml", true, 3), 200)
	var approvals, activeGrants int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='source.repository.approve'").Scan(&approvals); err != nil || approvals != 2 {
		t.Fatal("repository approval audit did not distinguish admin binding from project edits")
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE kind='integration' AND revoked_at IS NULL").Scan(&activeGrants); err != nil || activeGrants != 1 {
		t.Fatal("binding CAS leaked active source grants")
	}
	// Build configuration editing alone must not lend the global token's read
	// authority. Only a browser admin can install the exact current revision.
	created := call("POST", "/builds", deployerSession.Token, map[string]any{"project": "demo", "environment": "development", "name": "repository-build", "repository": "private/approved", "architecture": "arm64"}, 201)
	buildID := created["id"].(string)
	if _, err = db.Pool.Exec(ctx, "UPDATE build_configs SET installed_revision=revision WHERE id=$1", buildID); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/builds/"+buildID, deployerSession.Token, map[string]any{"project": "demo", "environment": "development", "name": "repository-build", "service": "web", "repository": "private/unapproved", "architecture": "arm64", "expected_config_revision": 1}, 200)
	beforeDenied = credentialReads.Load()
	call("POST", "/builds/"+buildID+"/run", deployerSession.Token, map[string]int64{"expected_config_revision": 2}, 409)
	call("POST", "/builds/"+buildID+"/install", deployerSession.Token, map[string]int64{"expected_config_revision": 2}, 403)
	if credentialReads.Load() != beforeDenied || repositoryReads.Load() != 2 {
		t.Fatal("unapproved build revision accessed the shared private repository token")
	}
	t.Log("project deployer first/switch denied before token access; admin approval is normalized and transactional with revision/grant CAS; same-repository branch/path/auto edits and private source plan succeed; build edits invalidate admin installation approval; all GitHub traffic used the local read-only fixture")
}
