package cluster

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testOracleFreeLoseController(t *testing.T, ctx context.Context, c *Client, d database.Resource) types.UID {
	t.Helper()
	if err := c.oracleFreeControllerReady(ctx, d); err != nil {
		t.Fatal("Oracle Free controller was not ready before the loss test", err)
	}
	api := c.kube.AppsV1().Deployments(DatabaseNamespace(d.ID))
	current, err := api.Get(ctx, "oracle-free-operator", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	foreground := metav1.DeletePropagationForeground
	if err = api.Delete(ctx, current.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion}, PropagationPolicy: &foreground}); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for wait.Err() == nil {
		_, err = api.Get(wait, current.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return current.UID
		}
		if err != nil {
			t.Fatal(err)
		}
		if sleepContext(wait, time.Second) != nil {
			break
		}
	}
	t.Fatal("Oracle Free controller did not terminate gracefully")
	return ""
}

func testOracleFreeExpireIssuer(t *testing.T, ctx context.Context, c *Client, d database.Resource) {
	t.Helper()
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	issuer, err := api.Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil || databaseIdentityOwned(issuer, d, ns.UID) != nil {
		t.Fatal("Oracle Free issuer ownership changed")
	}
	pair, err := tls.X509KeyPair(issuer.Data["ca.crt"], issuer.Data["ca.key"])
	if err != nil {
		t.Fatal("Oracle Free issuer is invalid")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal("Oracle Free issuer is invalid")
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatal("Oracle Free issuer signing key is invalid")
	}
	certificate.NotAfter = time.Now().Add(24 * time.Hour)
	encoded, err := x509.CreateCertificate(rand.Reader, certificate, certificate, signer.Public(), signer)
	if err != nil {
		t.Fatal("Oracle Free issuer expiry injection failed")
	}
	issuer.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded})
	if _, err = api.Update(ctx, issuer, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Run only after other native database cases release the shared development
// lane. This uses the same scoped controller and replacement paths as the API.
func TestManagedOracleFreeControllerLossLive(t *testing.T) {
	c, ctx := liveOracleClient(t)
	d, observed := newOracleFixture(t, ctx, c, "")
	connection := oracleFixtureConnection(t, ctx, c, d, observed)
	for _, statement := range []string{"CREATE TABLE controller_fixture (id NUMBER PRIMARY KEY)", "INSERT INTO controller_fixture VALUES (42)"} {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			t.Fatal("Oracle Free controller fixture write failed", err)
		}
	}
	oldController := testOracleFreeLoseController(t, ctx, c, d)
	testOracleFreeExpireIssuer(t, ctx, c, d)
	if err := c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err == nil {
		t.Fatal("Oracle Free replaced a member without its controller")
	}
	member := observed.Members[0]
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || string(pod.UID) != member.UID || pod.DeletionTimestamp != nil {
		t.Fatal("Oracle Free removed its singleton while its controller was unavailable")
	}
	var value int
	if err = connection.QueryRowContext(ctx, "SELECT id FROM controller_fixture").Scan(&value); err != nil || value != 42 {
		t.Fatal("Oracle Free lost its existing application connection during controller loss", err)
	}
	if err = c.prepareOracleFreeController(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 3*time.Minute)
	for wait.Err() == nil && c.oracleFreeControllerReady(wait, d) != nil {
		if sleepContext(wait, time.Second) != nil {
			break
		}
	}
	if err = c.oracleFreeControllerReady(wait, d); err != nil {
		cancel()
		t.Fatal("Oracle Free controller did not recover", err)
	}
	cancel()
	controller, err := c.kube.AppsV1().Deployments(DatabaseNamespace(d.ID)).Get(ctx, "oracle-free-operator", metav1.GetOptions{})
	if err != nil || controller.UID == oldController {
		t.Fatal("Oracle Free controller was not recreated")
	}
	if err = c.RenewDatabaseIdentity(ctx, d, func() error { return ctx.Err() }); err != nil {
		t.Fatal("Oracle Free renewal did not resume after controller recovery", err)
	}
	wait, cancel = context.WithTimeout(ctx, 8*time.Minute)
	var renewed database.Observation
	for wait.Err() == nil {
		renewed, err = c.ObserveDatabase(wait, d)
		if err == nil && renewed.Status == "ready" && renewed.TLS != nil && renewed.TLS.Verified && renewed.TLS.Fingerprint != observed.TLS.Fingerprint && len(renewed.Members) == 1 && renewed.Members[0].UID != member.UID {
			break
		}
		if sleepContext(wait, 2*time.Second) != nil {
			break
		}
	}
	cancel()
	if renewed.Status != "ready" || renewed.TLS == nil || !renewed.TLS.Verified || renewed.TLS.Fingerprint == observed.TLS.Fingerprint || len(renewed.Members) != 1 || renewed.Members[0].UID == member.UID {
		t.Fatal("Oracle Free renewal did not replace and verify the singleton", err)
	}
	renewedConnection := oracleFixtureConnection(t, ctx, c, d, renewed)
	if err = renewedConnection.QueryRowContext(ctx, "SELECT id FROM controller_fixture").Scan(&value); err != nil || value != 42 {
		t.Fatal("Oracle Free renewal did not preserve data after controller recovery", err)
	}
	oldController = testOracleFreeLoseController(t, ctx, c, d)
	wait, cancel = context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	recreated := false
	for wait.Err() == nil {
		controller, observedErr := c.kube.AppsV1().Deployments(DatabaseNamespace(d.ID)).Get(wait, "oracle-free-operator", metav1.GetOptions{})
		if observedErr == nil && controller.UID != oldController && c.oracleFreeControllerReady(wait, d) == nil {
			recreated = true
		}
		done, deleteErr := c.DeleteDatabase(wait, d, func() error { return wait.Err() })
		if done {
			if !recreated {
				t.Fatal("Oracle Free deletion did not repair its missing finalizer controller")
			}
			t.Log("Oracle Free preserved its singleton while the controller was absent, resumed verified renewal after controller recovery, and repaired its finalizer controller before reclaiming the database and both volumes")
			return
		}
		err = deleteErr
		if sleepContext(wait, 2*time.Second) != nil {
			break
		}
	}
	t.Fatal("Oracle Free controller-loss deletion did not reclaim owned resources", err)
}
