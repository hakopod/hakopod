package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A read-only probe can inspect the source while an independent target boots.
// Its ID must be explicitly selected from the named development cluster.
func TestManagedOracleObservedSecurityLive(t *testing.T) {
	c, ctx := liveOracleClient(t)
	id := os.Getenv("HAKOPOD_ORACLE_FIXTURE_ID")
	if id == "" {
		t.Skip("no owned development fixture selected")
	}
	d := oracleFixture()
	d.ID = id
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(id), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != id || ns.Labels[managedBy] != "hakopod" {
		t.Fatal("Oracle development fixture ownership changed")
	}
	o, err := c.ObserveDatabase(ctx, d)
	if err != nil || o.Status != "ready" {
		t.Fatal("Oracle fixture is not ready", err)
	}
	if o.TLS == nil || !o.TLS.Verified || !o.TLS.PlaintextRejected {
		t.Fatal("Oracle TLS observation is incomplete")
	}
	if o.EngineMetrics == nil || !o.EngineMetrics.Available || o.EngineMetrics.SampledAt == nil {
		t.Fatal("Oracle native metrics are unavailable")
	}
	for _, wrongIssuer := range []bool{false, true} {
		config, err := c.oracleTLSConfig(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		if wrongIssuer {
			config.RootCAs = x509.NewCertPool()
		} else {
			config.ServerName = "incorrect.database.example"
		}
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		raw, err := c.oracleStream(step, d, o.Members[0])
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		connection := tls.Client(raw, config)
		err = connection.HandshakeContext(step)
		_ = connection.Close()
		cancel()
		var certificate *tls.CertificateVerificationError
		if !errors.As(err, &certificate) {
			t.Fatal("Oracle failed without specifically rejecting the wrong certificate identity")
		}
	}
	t.Log("Oracle reported native metrics and rejected plaintext, an untrusted issuer and an incorrect hostname")
}
