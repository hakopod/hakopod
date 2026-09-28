package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/management"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/clientcmd"
)

// Opt-in acceptance runs the actual built SDK against the real Go API, durable
// PostgreSQL state and named development cluster. No synthetic runtime success.
func TestSDKLiveLifecycle(t *testing.T) {
	if os.Getenv("HAKOPOD_SDK_LIVE_TEST") != "1" {
		t.Skip("requires built SDK, Node and named development cluster")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires k3d-hakopod-dev")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	db, _ := database(t)
	token, err := db.Bootstrap(ctx, "sdk-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := db.CreateKey(ctx, p, store.KeyInput{Name: "sdk-development-fixture", Project: "demo", Environment: "development", Permissions: []string{"deployments:read", "deployments:write", "logs:read", "networks:write", "applications:manage"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	kube, err := cluster.New(path, cluster.Options{AppDomain: "127.0.0.1.sslip.io", RolloutTimeout: 120 * time.Second, VirtualNetworks: db.ResolveVirtualNetworks})
	if err != nil {
		t.Fatal(err)
	}
	server := &api.Server{Store: db, Cluster: kube, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{27}, 32))}}
	handler, wait := management.Start(ctx, server, "127.0.0.1.sslip.io", 120*time.Second)
	defer func() { cancel(); wait() }()
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	node := os.Getenv("HAKOPOD_SDK_NODE")
	if node == "" {
		node = "node"
	}
	script, err := filepath.Abs("../../packages/sdk/test/live.mjs")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, node, script)
	command.Env = append(os.Environ(), "HAKOPOD_API_URL="+httpServer.URL, "HAKOPOD_API_KEY="+raw, "HAKOPOD_PROJECT=demo", "HAKOPOD_ENVIRONMENT=development", "HAKOPOD_SDK_LIVE_TEST=1")
	output, err := command.CombinedOutput()
	// The script prints only operation IDs and statuses, never credential values.
	if err != nil {
		t.Fatalf("SDK live acceptance failed: %v\n%s", err, output)
	}
	t.Log(string(output))
}
