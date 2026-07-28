package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/remotecommand"
)

type terminalFlushFailure struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *terminalFlushFailure) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func (w *terminalFlushFailure) FlushError() error {
	return errors.New("fixture output disconnected during initial flush")
}

func TestTerminalInputAuthorityAndBackpressure(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Terminal operator", "terminal@example.test", "disposable-password-123", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	app, _ := spec.Normalize(spec.Application{Name: "terminal-test", Services: map[string]spec.Service{"web": {Image: "python:3.13-alpine"}}})
	deployment, err := db.Accept(ctx, p, "demo", "development", app, 0, "terminal-test-initial")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	handler := s.Handler()
	defer s.CloseTerminals()
	xctx, cancel := context.WithCancel(ctx)
	defer cancel()
	x := &terminalSession{id: store.NewID(), app: deployment.ApplicationID, service: "web", owner: p.ID, key: p.KeyID, ctx: xctx, cancel: cancel, input: make(chan []byte, 1), sizes: make(chan remotecommand.TerminalSize, 1), started: true}
	s.terminals[x.id] = x
	base := "/api/v1/applications/" + deployment.ApplicationID + "/services/web/terminal/" + x.id
	call := func(token, path string, input any) int {
		r := httptest.NewRequest("POST", path, bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	input := map[string]string{"data": base64.StdEncoding.EncodeToString([]byte("hello\r"))}
	if status := call(session.Token, base+"/input", input); status != 204 {
		t.Fatal("input rejected", status)
	}
	if status := call(session.Token, base+"/input", input); status != 429 {
		t.Fatal("unbounded terminal input", status)
	}
	<-x.input
	other, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if status := call(other.Token, base+"/input", input); status != 404 {
		t.Fatal("another login session attached to terminal", status)
	}
	if status := call(session.Token, base+"/input", map[string]int{"cols": 1000, "rows": 30}); status != 400 {
		t.Fatal("invalid size accepted", status)
	}
	t.Run("initial-flush-failure", func(t *testing.T) {
		failedCtx, failedCancel := context.WithCancel(ctx)
		defer failedCancel()
		failed := &terminalSession{id: store.NewID(), app: deployment.ApplicationID, service: "web", owner: p.ID, key: p.KeyID, ctx: failedCtx, cancel: failedCancel}
		s.terminals[failed.id] = failed
		r := httptest.NewRequest("GET", "/api/v1/applications/"+deployment.ApplicationID+"/services/web/terminal/"+failed.id+"/output", nil)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		w := &terminalFlushFailure{ResponseRecorder: httptest.NewRecorder()}
		// There is deliberately no cluster: failed initial output must return
		// before launching any exec transport.
		handler.ServeHTTP(w, r)
		if failedCtx.Err() == nil || w.deadline.IsZero() || len(s.streams) != 0 {
			t.Fatal("failed output retained its session/stream or lacked an initial write deadline")
		}
		if _, exists := s.terminals[failed.id]; exists {
			t.Fatal("failed output session was not removed")
		}
	})
	if err := db.RevokeSession(ctx, p, p.KeyID); err != nil {
		t.Fatal(err)
	}
	if status := call(session.Token, base+"/input", input); status != 401 {
		t.Fatal("revoked session accepted", status)
	}
	guardCtx, stop := context.WithTimeout(ctx, 7*time.Second)
	defer stop()
	go s.guardStream(guardCtx, cancel, p.KeyID, store.Application{ID: deployment.ApplicationID, Name: app.Name, Project: "demo", Environment: "development"}, "deployments:write")
	select {
	case <-xctx.Done():
	case <-guardCtx.Done():
		t.Fatal("terminal authority guard did not close revoked session")
	}
}

func TestTerminalPipesStopWithoutUnboundedBuffers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input := make(chan []byte, 1)
	r := terminalReader{ctx: ctx, input: input}
	input <- []byte("abcd")
	b := make([]byte, 2)
	if n, _ := r.Read(b); n != 2 || string(b) != "ab" {
		t.Fatal("split read")
	}
	if n, _ := r.Read(b); n != 2 || string(b) != "cd" {
		t.Fatal("pending read")
	}
	output := make(chan []byte, 1)
	writer := terminalWriter{ctx: ctx, output: output}
	if n, err := writer.Write([]byte("chunk")); err != nil || n != 5 {
		t.Fatal(err)
	}
	cancel()
	if _, err := writer.Write([]byte("blocked")); err == nil {
		t.Fatal("writer ignored cancellation")
	}
	if _, err := r.Read(b); err == nil {
		t.Fatal("reader ignored cancellation")
	}
}
