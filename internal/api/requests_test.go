package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/requestlog"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestRequestsAPIRegisteredAndScoped(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	token, err := db.Bootstrap(ctx, "request-api-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := db.Authenticate(ctx, token)
	app, _ := spec.Parse([]byte("name='request-api'\n[services.api]\nimage='nginx:alpine'\nport=80"))
	dep, err := db.Accept(ctx, p, "demo", "development", app, 0, "request-api-deployment")
	if err != nil {
		t.Fatal(err)
	}
	e := requestlog.Entry{ID: strings.Repeat("a", 64), Timestamp: time.Now(), ApplicationID: dep.ApplicationID, Service: "api", Method: "GET", Path: "/test", Status: 404}
	if err = db.SaveRequests(ctx, []requestlog.Entry{e}, nil, "collecting", "Collecting", 0); err != nil {
		t.Fatal(err)
	}
	_, reader, err := db.CreateKey(ctx, p, store.KeyInput{Name: "request-reader", Project: "demo", Environment: "development", Permissions: []string{"logs:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	apiServer := &Server{Store: db}
	bindings, truncated, err := apiServer.requestBindings(ctx)
	if err != nil || truncated || bindings[cluster.Namespace(dep.ApplicationID)+"_svc_api_service"].ApplicationID != dep.ApplicationID {
		t.Fatalf("backend mapping failed: %+v %v", bindings, err)
	}
	server := apiServer.Handler()
	call := func(path, key string, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1"+path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if !strings.Contains(call("/requests?status=4", reader, 200), "/test") {
		t.Fatal("missing scoped request")
	}
	if strings.Contains(call("/requests?project=other", reader, 200), "/test") {
		t.Fatal("cross-scope request")
	}
	call("/requests?limit=invalid", reader, 400)
	call("/applications/"+dep.ApplicationID+"/services/api/requests/routing", reader, 503)
	call("/requests", "invalid", 401)
}
