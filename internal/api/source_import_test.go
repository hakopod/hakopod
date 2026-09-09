package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestSourceImportPinsReviewAndCreatesAtomicBinding(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			db := sourceDatabase(t)
			ctx := context.Background()
			raw, err := db.Bootstrap(ctx, "source-import")
			if err != nil {
				t.Fatal(err)
			}
			commit := strings.Repeat("b", 40)
			content := "name='source-import'\n[services.web]\nimage='python:3.13-alpine'\nport=8080\n"
			var unavailable atomic.Bool
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if unavailable.Load() {
					w.WriteHeader(503)
					return
				}
				if strings.Contains(r.URL.Path, "/commits/") {
					write(w, 200, map[string]string{"id": commit, "sha": commit})
					return
				}
				if r.URL.Query().Get("ref") != commit || !strings.Contains(r.URL.Path, "config/hakopod.toml") {
					t.Error("file fetch was not pinned to exact commit/path", r.URL.Path, r.URL.RawQuery)
				}
				write(w, 200, map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content)), "size": len(content), "commit_id": commit})
			}))
			defer remote.Close()
			credentials := func(context.Context) (map[string][]byte, error) {
				return map[string][]byte{"webhook-secret": bytes.Repeat([]byte("x"), 32)}, nil
			}
			s := &Server{Store: db, Auth: AuthConfig{EncryptionKey: strings.Repeat("17", 32)}, githubAPIURL: remote.URL, gitlabAPIURL: remote.URL, githubHTTP: remote.Client(), gitlabHTTP: remote.Client(), githubTestCredentials: credentials, gitlabTestCredentials: credentials}
			handler := s.Handler()
			call := func(path string, in any, idem string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/api/v1"+path, bytes.NewReader(store.JSON(in)))
				r.Header.Set("Authorization", "Bearer "+raw)
				r.Header.Set("Idempotency-Key", idem)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			input := sourceImportInput{Project: "demo", Environment: "development", Provider: provider, Repository: "example/repository", Branch: "main", Path: "config/hakopod.toml", AutoDeploy: true}
			plan := call("/sources/plan", input, "")
			if plan.Code != 200 {
				t.Fatal(plan.Code, plan.Body.String())
			}
			var review struct {
				Token  string `json:"review_token"`
				Commit string `json:"commit_sha"`
			}
			if json.Unmarshal(plan.Body.Bytes(), &review) != nil || review.Commit != commit || review.Token == "" {
				t.Fatal("missing pinned review")
			}
			if w := call("/sources/deploy", map[string]string{"review_token": review.Token + "x"}, "source-import-01"); w.Code != 400 {
				t.Fatal("tampered review accepted", w.Code)
			}
			content = strings.ReplaceAll(content, "8080", "8081")
			if w := call("/sources/deploy", map[string]string{"review_token": review.Token}, "source-import-01"); w.Code != 409 {
				t.Fatal("content changed after review", w.Code, w.Body.String())
			}
			content = strings.ReplaceAll(content, "8081", "8080")
			accepted := call("/sources/deploy", map[string]string{"review_token": review.Token}, "source-import-01")
			if accepted.Code != 202 {
				t.Fatal(accepted.Code, accepted.Body.String())
			}
			var d store.Deployment
			if json.Unmarshal(accepted.Body.Bytes(), &d) != nil {
				t.Fatal("invalid deployment")
			}
			bound, err := s.readSource(ctx, d.ApplicationID)
			if err != nil || bound.Repository != input.Repository || bound.Provider != provider || bound.Path != input.Path || bound.LastCommit != commit || bound.LastDeployment != d.ID || !bound.AutoDeploy {
				t.Fatal("source not atomically bound", bound, err)
			}
			unavailable.Store(true)
			replayed := call("/sources/deploy", map[string]string{"review_token": review.Token}, "source-import-01")
			if replayed.Code != 202 {
				t.Fatal("idempotent retry failed", replayed.Code, replayed.Body.String())
			}
			var again store.Deployment
			_ = json.Unmarshal(replayed.Body.Bytes(), &again)
			if again.ID != d.ID {
				t.Fatal("retry made duplicate deployment")
			}
			encoded, _ := base64.RawURLEncoding.DecodeString(strings.Split(review.Token, ".")[0])
			var expired sourceImportReview
			_ = json.Unmarshal(encoded, &expired)
			expired.Expires = time.Now().Add(-time.Hour).Unix()
			expiredToken := s.signSourceReview(store.JSON(expired))
			if w := call("/sources/deploy", map[string]string{"review_token": expiredToken}, "source-import-01"); w.Code != 202 {
				t.Fatal("accepted request became unavailable after review expiry", w.Code, w.Body.String())
			}
			if w := call("/sources/deploy", map[string]string{"review_token": expiredToken}, "source-import-new-key"); w.Code != 409 {
				t.Fatal("expired review authorized a new request", w.Code)
			}
			expired.Input.Repository = "example/different"
			if w := call("/sources/deploy", map[string]string{"review_token": s.signSourceReview(store.JSON(expired))}, "source-import-01"); w.Code != 409 {
				t.Fatal("same idempotency key accepted another repository", w.Code)
			}
			unavailable.Store(false)
			if w := call("/sources/plan", input, ""); w.Code != 409 {
				t.Fatal("initial import accepted existing app", w.Code)
			}
			var grants int
			if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE kind='integration'").Scan(&grants); err != nil || grants != 1 {
				t.Fatal("retry created duplicate source grants", grants, err)
			}
		})
	}
}

func TestSourceImportFailureRollsBackApplicationAndGrant(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "source-import-rollback")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `CREATE FUNCTION refuse_source_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture refused source'; END $$; CREATE TRIGGER refuse_source_fixture BEFORE INSERT ON application_sources FOR EACH ROW EXECUTE FUNCTION refuse_source_fixture()`)
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{Name: "source-atomic-fixture", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine", Port: 8080}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.AcceptSourceImport(ctx, p, "demo", "development", app, store.InitialSource{Provider: "github", Repository: "example/repository", Branch: "main", Path: "hakopod.toml", CommitSHA: strings.Repeat("a", 40)}, "source-atomic-failure")
	if err == nil {
		t.Fatal("fixture should refuse source binding")
	}
	var apps, deployments, grants int
	if err = db.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM applications),(SELECT count(*) FROM deployments),(SELECT count(*) FROM api_keys WHERE kind='integration')").Scan(&apps, &deployments, &grants); err != nil || apps != 0 || deployments != 0 || grants != 0 {
		t.Fatal("failed binding left partial application or credentials", apps, deployments, grants, err)
	}
}
