package cluster

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testClickHouseCertificateRefusal(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range o.Members {
		for _, port := range []int{8443, 9440} {
			for _, wrongIssuer := range []bool{false, true} {
				config, err := redisTLSConfig(trust, clickhouseMemberHost(d, member))
				if err != nil {
					t.Fatal(err)
				}
				if wrongIssuer {
					config.RootCAs = x509.NewCertPool()
				} else {
					config.ServerName = "incorrect.database.example"
				}
				step, stop := context.WithTimeout(ctx, 8*time.Second)
				raw, err := c.clickhouseStream(step, d, member, port, false)
				if err != nil {
					stop()
					t.Fatal(err)
				}
				secured := tls.Client(raw, config)
				err = secured.HandshakeContext(step)
				_ = secured.Close()
				stop()
				var certificate *tls.CertificateVerificationError
				if !errors.As(err, &certificate) {
					t.Fatal("ClickHouse refusal was not a certificate verification failure")
				}
			}
		}
	}
	t.Log("ClickHouse HTTPS and native clients rejected an untrusted issuer and mismatched hostname on every member")
}

func TestManagedClickHouseTLSRenewalLive(t *testing.T) {
	c, ctx := liveClickHouseClient(t)
	d, observed := newClickHouseFixture(t, ctx, c, "cluster", 1)
	testClickHouseCertificateRefusal(t, ctx, c, d, observed)
	testClickHouseRenewal(t, ctx, c, d, observed)
}

func testClickHouseRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation) database.Observation {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	root, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal("ClickHouse fixture issuer is invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("ClickHouse fixture issuer expiry injection failed")
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = c.kube.CoreV1().Secrets(root.Namespace).Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 6*time.Minute)
	defer stop()
	var next database.Observation
	for wait.Err() == nil {
		step, cancel := context.WithTimeout(wait, 35*time.Second)
		next, err = c.ObserveDatabase(step, d)
		cancel()
		if err == nil && next.Status == "ready" && next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != observed.TLS.Fingerprint && next.TLS.CAFingerprint != trust.Fingerprint {
			break
		}
		t.Log("Waiting for ClickHouse certificate renewal", next.Message, err)
		if sleepContext(wait, 5*time.Second) != nil {
			break
		}
	}
	if next.Status != "ready" || next.TLS == nil || !next.TLS.Verified || next.TLS.Fingerprint == observed.TLS.Fingerprint || next.TLS.CAFingerprint == trust.Fingerprint {
		t.Fatal("ClickHouse did not load renewed server and issuer certificates", err)
	}
	for _, member := range next.Members {
		config, err := redisTLSConfig(trust, clickhouseMemberHost(d, member))
		if err != nil {
			t.Fatal(err)
		}
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		raw, err := c.clickhouseStream(step, d, member, 9440, false)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		secured := tls.Client(raw, config)
		err = secured.HandshakeContext(step)
		_ = secured.Close()
		cancel()
		if err != nil {
			t.Fatal("old ClickHouse CA no longer trusts renewed member identity")
		}
	}
	if next.Coordination == nil || !next.Coordination.Ready {
		t.Fatal("Keeper did not recover after certificate renewal")
	}
	t.Log("Every ClickHouse and Keeper member served its renewed identity, with old application CA overlap")
	return next
}
