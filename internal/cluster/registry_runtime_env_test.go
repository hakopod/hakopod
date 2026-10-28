package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func digestOf(data string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data))) }

func TestImageRuntimeEnv(t *testing.T) {
	config := `{"architecture":"arm64","os":"linux","config":{"Env":["PATH=/usr/bin","NODE_VERSION=22.3.0","API_TOKEN=secret=x","YARN_VERSION=1.22"]}}`
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"` + digestOf(config) + `"}}`
	index := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:` + strings.Repeat("0", 64) + `","platform":{"architecture":"amd64","os":"linux"}},{"digest":"` + digestOf(manifest) + `","platform":{"architecture":"arm64","os":"linux"}}]}`
	bodies := map[string]string{
		"/v2/library/node/manifests/22":                    index,
		"/v2/library/node/manifests/" + digestOf(index):    index,
		"/v2/library/node/manifests/" + digestOf(manifest): manifest,
		"/v2/library/node/blobs/" + digestOf(config):       config,
	}
	kube := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64"}}})
	client := &Client{kube: kube, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := bodies[r.URL.Path]
		if r.URL.Host != "registry-1.docker.io" || !ok {
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	env, err := client.ImageRuntimeEnv(context.Background(), "node:22", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env, map[string]string{"NODE_VERSION": "22.3.0"}) {
		t.Fatalf("env %v", env)
	}
	// A tampered config blob must fail digest verification.
	bodies["/v2/library/node/blobs/"+digestOf(config)] = strings.Replace(config, "22.3.0", "22.9.9", 1)
	if _, err := client.ImageRuntimeEnv(context.Background(), "node:22", "", ""); err == nil {
		t.Fatal("tampered config accepted")
	}
}

// HAKOPOD_LIVE_REGISTRY=1 go test ./internal/cluster -run LiveRuntimeEnv -v
func TestLiveRuntimeEnv(t *testing.T) {
	if os.Getenv("HAKOPOD_LIVE_REGISTRY") != "1" {
		t.Skip("set HAKOPOD_LIVE_REGISTRY=1 to query public registries")
	}
	kube := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64"}}})
	client := &Client{kube: kube, http: &http.Client{Transport: registryTransport(), Timeout: 30 * time.Second}}
	for _, image := range []string{"node:22-alpine", "python:3.12-slim", "golang:1.23", "php:8.3-cli", "ruby:3.3", "oven/bun:1", "eclipse-temurin:21"} {
		env, err := client.ImageRuntimeEnv(context.Background(), image, "", "")
		t.Logf("%s => %v err=%v", image, env, err)
		// oven/bun images carry no BUN_VERSION variable (checked 2026-10-25).
		if err != nil || (len(env) == 0 && image != "oven/bun:1") {
			t.Errorf("%s: no runtime variable", image)
		}
	}
}
