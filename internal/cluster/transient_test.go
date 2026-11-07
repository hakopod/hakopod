package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"syscall"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func TestTransientAPIErrorClassification(t *testing.T) {
	limiter := fmt.Errorf("client rate limiter Wait returned an error: %w", errors.New("rate: Wait(n=1) would exceed context deadline"))
	pods := schema.GroupResource{Resource: "pods"}
	for name, err := range map[string]error{
		"limiter":            limiter,
		"wrapped limiter":    fmt.Errorf("database namespace is unavailable: %w", limiter),
		"deadline":           fmt.Errorf("observe: %w", context.DeadlineExceeded),
		"429":                apierrors.NewTooManyRequests("slow down", 1),
		"server timeout":     apierrors.NewServerTimeout(pods, "list", 1),
		"timeout":            apierrors.NewTimeoutError("timed out", 1),
		"unavailable":        apierrors.NewServiceUnavailable("unavailable"),
		"internal":           apierrors.NewInternalError(errors.New("etcd")),
		"502":                apierrors.NewGenericServerResponse(502, "get", pods, "x", "bad gateway", 0, true),
		"connection reset":   &url.Error{Op: "Get", URL: "https://127.0.0.1:6443/api", Err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}},
		"connection refused": fmt.Errorf("dial: %w", syscall.ECONNREFUSED),
	} {
		if !TransientAPIError(err) {
			t.Errorf("%s: %v was not transient", name, err)
		}
	}
	for name, err := range map[string]error{
		"nil":       nil,
		"cancelled": fmt.Errorf("observe: %w", context.Canceled),
		"not found": apierrors.NewNotFound(pods, "x"),
		"forbidden": apierrors.NewForbidden(pods, "x", errors.New("no")),
		"ownership": errors.New("database namespace ownership changed"),
	} {
		if TransientAPIError(err) {
			t.Errorf("%s: %v was transient", name, err)
		}
	}
	if !ThrottledAPIError(limiter) || ThrottledAPIError(context.DeadlineExceeded) || ThrottledAPIError(nil) {
		t.Fatal("throttling classification is wrong")
	}
}

func TestBackgroundClientUsesItsOwnKubernetesClient(t *testing.T) {
	main, background := fake.NewClientset(), fake.NewClientset()
	c := &Client{kube: main, backgroundKube: background, options: Options{AppDomain: "apps.example.test"}}
	b := c.Background()
	if b.kube != background || !b.background || b.options.AppDomain != "apps.example.test" {
		t.Fatal("background view does not use the background client with the same settings")
	}
	if b.Background() != b || c.ForContext(context.Background()) != c {
		t.Fatal("background selection is not stable")
	}
	marked := context.WithValue(context.Background(), backgroundContext{}, true)
	if c.ForContext(marked).kube != background {
		t.Fatal("background context did not select the background client")
	}
	plain := &Client{kube: main}
	if plain.Background() != plain {
		t.Fatal("a client without a background limiter must return itself")
	}
	// Background copies fields by name. A new Client field needs a decision
	// there; update this count afterwards.
	if n := reflect.TypeOf(Client{}).NumField(); n != 15 {
		t.Fatalf("Client has %d fields; review Client.Background before changing this count", n)
	}
}
