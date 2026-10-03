package cluster

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testVitessSecurity(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation, password []byte) {
	t.Helper()
	client := vitessFixtureClient(t, ctx, c, d, health, password, "app@primary", 0)
	for _, query := range []string{"CREATE USER forbidden_global_user IDENTIFIED BY 'fixture'", "SELECT authentication_string FROM mysql.user", "SELECT 1 FROM _vt.tables LIMIT 1", "UPDATE _vt.schema_migrations SET migration_status=migration_status WHERE 1=0"} {
		if _, err := client.ExecContext(ctx, query); err == nil {
			t.Fatal("Vitess application account accepted a global administrative operation")
		}
	}
	// The pinned vtgate checks and ignores SET GLOBAL. A successful gateway
	// response must not change MySQL, and the underlying application account
	// must independently reject an actual global variable change.
	globalBefore := make(map[string]int, len(health.Members))
	for _, member := range health.Members {
		globalBefore[member.Name] = vitessFixtureMaxConnections(t, ctx, c, d, member)
	}
	if len(globalBefore) != len(health.Members) || len(globalBefore) == 0 {
		t.Fatal("Vitess tablet inventory cannot verify global variable isolation")
	}
	globalProbe := globalBefore[health.Members[0].Name] + 1
	_, _ = client.ExecContext(ctx, fmt.Sprintf("SET GLOBAL max_connections=%d", globalProbe))
	for _, member := range health.Members {
		if vitessFixtureMaxConnections(t, ctx, c, d, member) != globalBefore[member.Name] {
			t.Fatal("Vitess application gateway changed a MySQL global variable")
		}
	}
	if d.Spec.Replicas > 0 {
		reader := vitessFixtureClient(t, ctx, c, d, health, password, "app@replica", 0)
		wait, stop := context.WithTimeout(ctx, time.Minute)
		defer stop()
		for {
			var count int
			err := reader.QueryRowContext(wait, "SELECT COUNT(*) FROM records").Scan(&count)
			if err == nil && count == 32 {
				break
			}
			if sleepContext(wait, time.Second) != nil {
				t.Fatal("Vitess replica route did not catch up")
			}
		}
		checkVitessFixtureData(t, ctx, reader)
		if _, err := reader.ExecContext(ctx, "INSERT INTO records VALUES (90,0x01,'forbidden')"); err == nil {
			t.Fatal("Vitess replica route accepted a write")
		}
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
	if err != nil {
		t.Fatal(err)
	}
	wrongName := verified.Clone()
	wrongName.ServerName = "wrong-database.invalid"
	wrongCA := verified.Clone()
	wrongCA.RootCAs = x509.NewCertPool()
	for name, config := range map[string]*tls.Config{"hostname": wrongName, "issuer": wrongCA} {
		probe, err := c.vitessGatewayClient(ctx, d, health.Routing.Members[0], "app@primary", password, config)
		if err != nil {
			t.Fatal(err)
		}
		err = probe.PingContext(ctx)
		_ = probe.Close()
		var invalid *tls.CertificateVerificationError
		if !errors.As(err, &invalid) {
			t.Fatal("Vitess did not prove certificate refusal for", name)
		}
	}
	plain, err := c.vitessGatewayClient(ctx, d, health.Routing.Members[0], "app@primary", password, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = plain.PingContext(ctx)
	_ = plain.Close()
	if !vitessPlaintextRefused(err) {
		t.Fatal("Vitess did not explicitly reject plaintext authentication")
	}
	for _, member := range health.Members {
		checkVitessTabletData(t, ctx, c, d, member, false)
		grants := &databaseBoundedWriter{limit: 1024}
		query := "SELECT Table_name,Table_priv FROM mysql.tables_priv WHERE User='vt_app' AND Host='localhost' AND Db='_vt' ORDER BY Table_name"
		if err := c.DatabaseExec(ctx, d, member, vitessLocalCommand("vt_dba", query), nil, grants); err != nil || strings.TrimSpace(grants.String()) != "schema_migrations\tSelect,Update\ntables\tSelect" {
			t.Fatal("Vitess bootstrap did not leave only the required metadata table privileges")
		}
		for _, query := range []string{"SELECT authentication_string FROM mysql.user", "CREATE USER forbidden_global_user IDENTIFIED BY 'fixture'", fmt.Sprintf("SET GLOBAL max_connections=%d", globalBefore[member.Name]+1)} {
			out := &databaseBoundedWriter{limit: 2048}
			command := append([]string{"sh", "-c", `exec "$@" 2>&1`, "vitess-privilege-refusal"}, vitessLocalCommand("vt_app", query)...)
			err := c.DatabaseExec(ctx, d, member, command, nil, out)
			if err == nil || !(strings.Contains(out.String(), "ERROR 1142 ") || strings.Contains(out.String(), "ERROR 1044 ") || strings.Contains(out.String(), "ERROR 1227 ")) {
				t.Fatal("Vitess application SQL account did not prove administrative authorization refusal")
			}
		}
	}
	checkVitessFixtureData(t, ctx, client)
	t.Log("Vitess rejected plaintext, wrong issuer, wrong hostname, replica writes and administrative SQL")
}

func vitessFixtureMaxConnections(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member) int {
	t.Helper()
	out := &databaseBoundedWriter{limit: 128}
	if c.DatabaseExec(ctx, d, member, vitessLocalCommand("vt_app", "SELECT @@global.max_connections"), nil, out) != nil {
		t.Fatal("Vitess tablet global variable inspection failed")
	}
	value, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil || value < 1 || value >= 1<<20 {
		t.Fatal("Vitess tablet global variable exceeded its verification bound")
	}
	return value
}

func testVitessPrimaryLoss(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation, password []byte) database.Observation {
	t.Helper()
	primaries, err := vitessObservedPrimaries(d, health)
	if err != nil {
		t.Fatal(err)
	}
	prior := primaries[0]
	pod, _, err := c.vitessExecTarget(ctx, d, prior)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0)), Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, 7*time.Minute)
	defer stop()
	for {
		next := waitVitessFixture(t, deadline, c, d, password)
		replaced := true
		for _, m := range next.Members {
			if m.UID == prior.UID {
				replaced = false
			}
		}
		if replaced {
			client := vitessFixtureClient(t, ctx, c, d, next, password, "app@primary", 0)
			checkVitessFixtureData(t, ctx, client)
			if _, err := client.ExecContext(ctx, "UPDATE records SET label=? WHERE id=1", "नमस्ते / 東京"); err != nil {
				t.Fatal("Vitess write route did not recover after primary loss", err)
			}
			checkVitessShardRouting(t, ctx, c, d, next)
			t.Log("Vitess retained data and restored writes after an owned primary process was removed")
			return next
		}
		if sleepContext(deadline, time.Second) != nil {
			t.Fatal("Vitess primary process was not replaced")
		}
	}
}

func testVitessRenewal(t *testing.T, ctx context.Context, c *Client, d database.Resource, health database.Observation, password []byte) {
	t.Helper()
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	root, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil || root.Labels[databaseOwner] != d.ID {
		t.Fatal("Vitess fixture issuer ownership changed")
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil {
		t.Fatal("Vitess fixture issuer is invalid")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatal("Vitess fixture issuer cannot sign")
	}
	ca.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, signer.Public(), signer)
	if err != nil {
		t.Fatal("Vitess fixture expiry injection failed")
	}
	root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if _, err := c.kube.CoreV1().Secrets(root.Namespace).Update(ctx, root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, 8*time.Minute)
	defer stop()
	for {
		next := waitVitessFixture(t, wait, c, d, password)
		if next.TLS != nil && next.TLS.Verified && next.TLS.Fingerprint != health.TLS.Fingerprint && next.TLS.CAFingerprint != trust.Fingerprint {
			checkVitessFixtureData(t, ctx, vitessFixtureClient(t, ctx, c, d, next, password, "app@primary", 0))
			oldConfig, err := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
			if err != nil {
				t.Fatal(err)
			}
			for _, gateway := range next.Routing.Members {
				oldClient, err := c.vitessGatewayClient(ctx, d, gateway, "app@primary", password, oldConfig)
				if err != nil {
					t.Fatal(err)
				}
				err = oldClient.PingContext(ctx)
				_ = oldClient.Close()
				if err != nil {
					t.Fatal("Vitess renewal broke the old-CA overlap")
				}
			}
			testVitessSecurity(t, ctx, c, d, next, password)
			return
		}
		if sleepContext(wait, 2*time.Second) != nil {
			t.Fatal("Vitess certificate renewal did not converge")
		}
	}
}
