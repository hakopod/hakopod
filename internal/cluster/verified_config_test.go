package cluster

import (
	"bytes"
	"encoding/pem"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func TestNewWithConfigUsesIndependentVerifiedTrustWithoutKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	config := &rest.Config{Host: server.URL, QPS: 3, Burst: 4, Timeout: time.Second, TLSClientConfig: rest.TLSClientConfig{CAData: append([]byte(nil), ca...)}}
	client, err := NewWithConfig(config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if config.QPS != 3 || config.Burst != 4 || config.Timeout != time.Second || config.UserAgent != "" {
		t.Fatal("client construction changed the verified caller configuration")
	}
	config.Host = "https://changed.invalid"
	config.CAData[0] ^= 1
	if client.execConfig.Host != server.URL || !bytes.Equal(client.execConfig.CAData, ca) || !bytes.Equal(client.clusterCA, ca) {
		t.Fatal("client trust aliases mutable caller configuration")
	}
	if _, err = NewWithConfig(nil, Options{}); err == nil {
		t.Fatal("missing verified configuration accepted")
	}
}
