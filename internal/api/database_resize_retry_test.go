package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

type resizeRetryRuntimeFixture struct {
	applied    bool
	appliedErr error
	observe    database.Observation
	observeErr error
}

func (f resizeRetryRuntimeFixture) DatabaseControllerAvailable(context.Context, database.Spec) error {
	return nil
}
func (f resizeRetryRuntimeFixture) DatabaseRevisionApplied(context.Context, database.Resource) (bool, error) {
	return f.applied, f.appliedErr
}
func (f resizeRetryRuntimeFixture) ObserveDatabase(_ context.Context, d database.Resource) (database.Observation, error) {
	o := f.observe
	o.Revision = d.Revision
	if o.ObservedAt.IsZero() {
		o.ObservedAt = time.Now().UTC()
	}
	return o, f.observeErr
}
func (f resizeRetryRuntimeFixture) ValidateDatabaseResize(context.Context, database.Resource, database.Spec) error {
	return nil
}

func failedResizeAPIResource(t *testing.T) (*store.Store, string, store.Principal, database.Resource, database.Operation) {
	t.Helper()
	s := notificationTestDB(t)
	ctx := context.Background()
	token, err := s.Bootstrap(ctx, "resize-retry-api")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "mysql-retry-api", Engine: "mysql", Version: "8.4", Mode: "cluster", Replicas: 2, Shards: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 5}, EncryptedCredentials: []byte("sealed-api-fixture")}
	d.Spec = d.Spec.WithSecureDefaults()
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "api-retry-create", "create"); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ready := database.Observation{Revision: 1, ObservedAt: time.Now().UTC(), Status: "ready", TopologyFingerprint: "prior-api-topology"}
	if err = s.RecordDatabaseStep(ctx, claim, ready, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	next := d.Spec
	next.Replicas = 4
	plan, err := database.PlanResize(d, next, nil, time.Now().UTC())
	if err != nil || len(plan.BlockedReasons) != 0 {
		t.Fatal("plan", err)
	}
	reviewID, err := s.SaveDatabaseReview(ctx, p, d, "resize", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	d.Spec = next
	if _, err = s.AcceptDatabaseResize(ctx, p, d, 1, "api-resize-failure", reviewID); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claim, ready, "failed", "review", "expired"); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, token, p, d, claim
}

func retryRequest(t *testing.T, handler http.Handler, token, method, path, body, idem string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	if idem != "" {
		r.Header.Set("Idempotency-Key", idem)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDatabaseResizeRetryAPIReviewsAcceptedIncompleteAndReplays(t *testing.T) {
	s, token, _, d, source := failedResizeAPIResource(t)
	runtime := resizeRetryRuntimeFixture{applied: true, observe: database.Observation{Status: "pending"}, observeErr: errors.New("rollout incomplete")}
	handler := (&Server{Store: s, databaseResizeRetryTestRuntime: runtime}).Handler()
	path := "/api/v1/databases/" + d.ID
	w := retryRequest(t, handler, token, http.MethodPost, path+"/resize-retry-plan", fmt.Sprintf(`{"operation_id":%q,"expected_revision":%d}`, source.ID, d.Revision), "")
	if w.Code != http.StatusOK {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	var reviewed struct {
		ID   string                     `json:"id"`
		Plan database.ResizeRetryReview `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reviewed); err != nil {
		t.Fatal(err)
	}
	if reviewed.Plan.State != "accepted" || len(reviewed.Plan.Resize.Warnings) == 0 {
		t.Fatal("incomplete accepted revision was not disclosed", reviewed.Plan)
	}
	body := fmt.Sprintf(`{"review_id":%q,"operation_id":%q,"expected_revision":%d,"confirm_name":%q}`, reviewed.ID, source.ID, d.Revision, d.Spec.Name)
	badConfirmation := fmt.Sprintf(`{"review_id":%q,"operation_id":%q,"expected_revision":%d,"confirm_name":"wrong"}`, reviewed.ID, source.ID, d.Revision)
	w = retryRequest(t, handler, token, http.MethodPost, path+"/resize-retry", badConfirmation, "api-reviewed-retry")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad confirmation admitted: %d %s", w.Code, w.Body.String())
	}
	w = retryRequest(t, handler, token, http.MethodPost, path+"/resize-retry", body, "api-reviewed-retry")
	if w.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var accepted database.Operation
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	w = retryRequest(t, handler, token, http.MethodPost, path+"/resize-retry", body, "api-reviewed-retry")
	var replay database.Operation
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil || w.Code != http.StatusAccepted || replay.ID != accepted.ID {
		t.Fatalf("replay: %d %+v %v", w.Code, replay, err)
	}
}

func TestDatabaseResizeRetryAPIRequiresExactSourceAndPriorHealth(t *testing.T) {
	s, token, _, d, source := failedResizeAPIResource(t)
	path := "/api/v1/databases/" + d.ID + "/resize-retry-plan"
	request := fmt.Sprintf(`{"operation_id":%q,"expected_revision":%d}`, source.ID, d.Revision)
	for _, test := range []struct {
		name    string
		runtime resizeRetryRuntimeFixture
		body    string
	}{
		{"missing or unowned controller", resizeRetryRuntimeFixture{appliedErr: errors.New("database ownership changed")}, request},
		{"unhealthy prior revision", resizeRetryRuntimeFixture{observe: database.Observation{Status: "pending"}}, request},
		{"stale operation", resizeRetryRuntimeFixture{observe: database.Observation{Status: "ready", TopologyFingerprint: "fresh"}}, fmt.Sprintf(`{"operation_id":%q,"expected_revision":%d}`, store.NewID(), d.Revision)},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := (&Server{Store: s, databaseResizeRetryTestRuntime: test.runtime}).Handler()
			w := retryRequest(t, handler, token, http.MethodPost, path, test.body, "")
			if w.Code != http.StatusConflict {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	healthyFingerprint := strings.Repeat("c", 64)
	healthy := resizeRetryRuntimeFixture{observe: database.Observation{Status: "ready", TopologyFingerprint: healthyFingerprint}}
	handler := (&Server{Store: s, databaseResizeRetryTestRuntime: healthy}).Handler()
	w := retryRequest(t, handler, token, http.MethodPost, path, request, "")
	if w.Code != http.StatusOK {
		t.Fatalf("healthy prior review: %d %s", w.Code, w.Body.String())
	}
	var reviewed struct {
		Plan database.ResizeRetryReview `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reviewed); err != nil || reviewed.Plan.State != "prior" || reviewed.Plan.Resize.TopologyFingerprint != healthyFingerprint {
		t.Fatalf("prior review was not source-bound: %+v %v", reviewed.Plan, err)
	}
}

func TestDatabaseResizeRetryAPIHandlesMissingController(t *testing.T) {
	s, token, _, d, source := failedResizeAPIResource(t)
	handler := (&Server{Store: s}).Handler()
	body := fmt.Sprintf(`{"operation_id":%q,"expected_revision":%d}`, source.ID, d.Revision)
	w := retryRequest(t, handler, token, http.MethodPost, "/api/v1/databases/"+d.ID+"/resize-retry-plan", body, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing controller: %d %s", w.Code, w.Body.String())
	}
}
