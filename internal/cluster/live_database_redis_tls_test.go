package cluster

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestManagedRedisTLSRenewalLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_TLS_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_TLS_TEST=1")
	}
	for _, mode := range []string{"standalone", "cluster"} {
		t.Run(mode, func(t *testing.T) { testManagedRedisTLSRenewal(t, mode == "cluster") })
	}
}

func testManagedRedisTLSRenewal(t *testing.T, clustered bool) {
	c, ctx := liveRecoveryClient(t)
	d, o := newRecoveryFixture(t, ctx, c, "redis", "8", clustered)
	if o.TLS == nil || !o.TLS.Verified || !o.TLS.PlaintextRejected {
		t.Fatal("native TLS enforcement not verified")
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	host := o.Endpoints[0].Host
	member := o.Members[0]
	for _, kind := range []string{"hostname", "issuer"} {
		config, err := redisTLSConfig(trust, host)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "hostname" {
			config.ServerName = "wrong-hostname.invalid"
		} else {
			wrong, _, err := database.ParsePublicTrust([]byte(trustFixtureCA(t)), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			config, err = redisTLSConfig(wrong, host)
			if err != nil {
				t.Fatal(err)
			}
		}
		raw, closeStream, err := c.databaseRedisStream(ctx, DatabaseNamespace(d.ID), member.Name, types.UID(member.UID), host)
		if err != nil {
			t.Fatal(err)
		}
		secure := tls.Client(raw, config)
		step, stop := context.WithTimeout(ctx, 4*time.Second)
		err = secure.HandshakeContext(step)
		stop()
		closeStream()
		var mismatch x509.HostnameError
		var issuer x509.UnknownAuthorityError
		if kind == "hostname" && !errors.As(err, &mismatch) {
			t.Fatal("wrong hostname was not specifically rejected", err)
		}
		if kind == "issuer" && !errors.As(err, &issuer) {
			t.Fatal("wrong issuer was not specifically rejected", err)
		}
	}
	api := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID))
	root, err := api.Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Expiry is injected only into this newly created development fixture. Private
	// keys stay in memory and never enter logs, commands or persisted evidence.
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal("fixture issuer key invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer := pair.PrivateKey.(crypto.Signer)
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("fixture expiry injection failed")
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err = api.Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		step, stop := context.WithTimeout(ctx, 25*time.Second)
		err = c.RenewDatabaseIdentity(step, d, func() error { return step.Err() })
		if err == nil {
			o, err = c.ObserveDatabase(step, d)
		}
		stop()
		if err == nil && o.Status == "ready" && o.TLS.Verified && o.TLS.CAFingerprint != trust.Fingerprint {
			// An existing application's old CA must trust the new certificate until its
			// immutable CA mount rolls forward; a new private key must not break it.
			secure, closeStream, err := c.databaseRedisTLSStream(ctx, d, member, host, trust)
			if err != nil {
				t.Fatal("CA overlap broke existing application trust", err)
			}
			_ = secure
			closeStream()
			t.Log("Verified Redis TLS, issuer/hostname rejection, plaintext rejection, certificate reload and old-CA overlap")
			return
		}
		if sleepContext(ctx, 3*time.Second) != nil {
			break
		}
	}
	t.Fatal("Redis automatic renewal was not verified", err)
}
