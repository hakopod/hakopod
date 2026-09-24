package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestVolumeResizeAPIReplayAndAuthority(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "resize-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := spec.Normalize(spec.Application{Name: "resize", Services: map[string]spec.Service{"db": {Image: "nginx@sha256:" + strings.Repeat("a", 64), Port: 8080, Volume: &spec.Volume{MountPath: "/data", SizeGiB: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "demo", "development", a, 0, "resize-fixture", a)
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.Claim(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = c.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	c.Release()
	operation, err := db.StartVolumeResize(ctx, p, d.ApplicationID, "db-data", 1, 1, "resize-request", json.RawMessage(`{"source_uid":"reviewed-source"}`))
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: db}).Handler()
	call := func(key, method, path string, body any, status int) {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(store.JSON(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Idempotency-Key", "resize-request")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, status, w.Body)
		}
		if strings.Contains(w.Body.String(), "reviewed-source") {
			t.Fatal("private journal leaked")
		}
	}
	base := "/applications/" + d.ApplicationID + "/volume-resizes"
	input := volumeResizeInput{Claim: "db-data", SizeGiB: 1, ExpectedRevision: 1, SourceUID: "reviewed-source", ConfirmDowntime: true}
	call(raw, "POST", base, input, 202)
	input.SourceUID = "replacement"
	call(raw, "POST", base, input, 409)
	input.SourceUID = "reviewed-source"
	input.ConfirmDowntime = false
	call(raw, "POST", base, input, 409)
	_, reader, err := db.CreateKey(ctx, p, store.KeyInput{Name: "reader", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	call(reader, "GET", base, nil, 200)
	for _, path := range []string{base, base + "/plan", base + "/" + operation.ID + "/cancel", base + "/" + operation.ID + "/delete-original"} {
		call(reader, "POST", path, map[string]any{}, 403)
	}
	db.AuthorizeRetainedCleanup = func(context.Context, store.Principal, string, string) error { return store.ErrForbidden }
	call(raw, "POST", base, input, 403)
	call(raw, "POST", base+"/"+operation.ID+"/cancel", map[string]string{}, 403)
}
