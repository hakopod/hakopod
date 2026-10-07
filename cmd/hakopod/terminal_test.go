package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTerminalPollingCursorAndExit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/terminal/poll" || r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-Hakopod-Workspace") != "workspace" {
			t.Error("incorrect polling request")
		}
		if calls == 1 {
			if r.URL.Query().Get("cursor") != "0" {
				t.Error("initial cursor")
			}
			_, _ = w.Write([]byte(`{"frames":[{"cursor":1,"data":{"type":"output","data":"aGk="}}],"next_cursor":"1"}`))
		} else {
			if r.URL.Query().Get("cursor") != "1" {
				t.Error("cursor did not advance")
			}
			_, _ = w.Write([]byte(`{"frames":[{"cursor":2,"data":{"type":"exit","code":7}}],"next_cursor":"2","done":true}`))
		}
	}))
	defer server.Close()
	var out bytes.Buffer
	err := pollTerminalOutput(context.Background(), &client{url: server.URL, key: "fixture", workspace: "workspace", http: server.Client()}, "/terminal", &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 7 || out.String() != "hi" || calls != 2 {
		t.Fatalf("output=%q calls=%d error=%v", out.String(), calls, err)
	}
}
func TestTerminalPollingRejectsInvalidOutput(t *testing.T) {
	for _, body := range []string{
		`{"frames":[],"next_cursor":"0","truncated":true}`,
		`{"frames":[{"cursor":2,"data":{"type":"output","data":"aA=="}}],"next_cursor":"2"}`,
		`{"frames":[{"cursor":1,"data":{"type":"output","data":"!"}}],"next_cursor":"1"}`,
		`{"frames":[],"next_cursor":"1"}`,
		`{"frames":[],"next_cursor":"0","done":true}`,
		`{"frames":[{"cursor":1,"data":{"type":"exit"}}],"next_cursor":"1"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			if err := pollTerminalOutput(context.Background(), &client{url: server.URL, http: server.Client()}, "/terminal", &bytes.Buffer{}); err == nil {
				t.Fatal("invalid polling response accepted")
			}
		})
	}
}
func TestTerminalPollingCancellation(t *testing.T) {
	requested := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(requested); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- pollTerminalOutput(ctx, &client{url: server.URL, http: server.Client()}, "/terminal", &bytes.Buffer{})
	}()
	<-requested
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled polling request did not stop")
	}
}

func TestTerminalWaitsForPollAndCleansFailedInput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, _ = writer.Write([]byte("hello"))
	writer.Close()
	previous := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = previous }()
	var ready, deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			deleted.Store(true)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/terminal"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
		case strings.HasSuffix(r.URL.Path, "/poll"):
			if !ready.Load() {
				time.Sleep(30 * time.Millisecond)
				ready.Store(true)
				_, _ = w.Write([]byte(`{"frames":[],"next_cursor":"0"}`))
				return
			}
			<-r.Context().Done()
		case strings.HasSuffix(r.URL.Path, "/input"):
			if !ready.Load() {
				t.Error("input arrived before poll attachment")
			}
			w.WriteHeader(409)
		default:
			t.Error("unexpected route")
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = terminal(ctx, &client{url: server.URL, http: server.Client()}, "app", "service", "pod", "container", "")
	if err == nil || !ready.Load() || !deleted.Load() {
		t.Fatalf("error=%v ready=%v deleted=%v", err, ready.Load(), deleted.Load())
	}
}
