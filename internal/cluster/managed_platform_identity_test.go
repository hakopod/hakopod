package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func platformTLSFixture(t *testing.T) (*Client, *fakeSupabaseOperationStore, store.ManagedPlatformOperation, *corev1.Namespace, map[string]store.PlatformResourceClaim, map[string]store.PlatformResourceClaim) {
	t.Helper()
	op := supabaseTestOperation(1)
	op.Kind = "create"
	op.Spec = managedplatform.Spec{Kind: "supabase", TLSMode: "managed", Secrets: map[string]managedplatform.SecretReference{}, Supabase: &managedplatform.SupabaseConfig{PublicURL: "https://data.example.test"}}
	ns := supabaseTestNamespace(op)
	kube := fake.NewSimpleClientset(ns)
	kube.PrependReactor("create", "secrets", func(action kubetesting.Action) (bool, runtime.Object, error) {
		object := action.(kubetesting.CreateAction).GetObject().(*corev1.Secret)
		object.UID = types.UID(object.Name + "-uid")
		return false, nil, nil
	})
	return &Client{kube: kube}, newFakeSupabaseStore(), op, ns, map[string]store.PlatformResourceClaim{}, map[string]store.PlatformResourceClaim{}
}

func TestManagedPlatformTLSCreatesOwnedIdentityAndResumesWithoutNewKeys(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	input := op.Spec
	values := map[string]map[string][]byte{}
	before := func() error { return state.HeartbeatManagedPlatformOperation(context.Background(), op) }
	if err := c.prepareManagedPlatformTLS(context.Background(), state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	first := map[string]map[string][]byte{}
	for name, data := range values {
		first[name] = copySecretData(data)
		if len(data["ca.key"]) > 0 {
			t.Fatal("issuer private key leaked into a runtime snapshot")
		}
	}
	db := values[secretSnapshotNameForCluster(input.Secrets["database-tls-certificate"])]
	gw := values[secretSnapshotNameForCluster(input.Secrets["gateway-tls-certificate"])]
	if bytes.Equal(db["tls.key"], gw["tls.key"]) {
		t.Fatal("gateway and database share a private key")
	}
	if err := validateSupabaseDatabaseTLS(db, ns.Name); err != nil {
		t.Fatal(err)
	}
	if err := validateSupabaseGatewayTLS(gw, op.Spec.Supabase.PublicURL); err != nil {
		t.Fatal(err)
	}
	if err := validateSupabaseDatabaseTLS(db, "managed-platform-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err == nil {
		t.Fatal("certificate accepted a different recovery namespace")
	}
	input = op.Spec
	values = map[string]map[string][]byte{}
	restarted := &Client{kube: c.kube}
	if err := restarted.prepareManagedPlatformTLS(context.Background(), state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	if len(first) != len(values) {
		t.Fatal("restart changed snapshot inventory")
	}
	for name, data := range first {
		if !equalSecretData(data, values[name]) {
			t.Fatal("restart generated a replacement identity")
		}
	}
	root, err := c.kube.CoreV1().Secrets(ns.Name).Get(context.Background(), managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	if err != nil || len(root.Data) != 2 || len(root.Data["ca.key"]) == 0 {
		t.Fatal("private issuer was not retained separately")
	}
}

func TestManagedPlatformTLSRenewalPreservesIssuerAndChangesLeaf(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	ctx := context.Background()
	before := func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }
	root, ca, signer, err := c.prepareManagedPlatformIssuer(ctx, state, op, ns, prior, current, before, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	names, ips, err := platformTLSNames(op.Spec, op.PlatformID, "database-tls-certificate")
	if err != nil {
		t.Fatal(err)
	}
	expiredSoon, err := issuePlatformTLSLeaf(map[string][]byte{}, ca, signer, root.Data["ca.crt"], names, ips, time.Now().Add(-25*24*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if validPlatformTLSLeaf(expiredSoon, map[string][]byte{}, ca, root.Data["ca.crt"], names, ips, time.Now(), false) {
		t.Fatal("leaf in renewal window was reused")
	}
	if err = c.applySupabaseSecret(ctx, state, op, ns, platformTLSSecretName("database-tls-certificate", expiredSoon), expiredSoon, prior, current, before); err != nil {
		t.Fatal(err)
	}
	input := op.Spec
	values := map[string]map[string][]byte{}
	if err = c.prepareManagedPlatformTLS(ctx, state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	renewed := values[secretSnapshotNameForCluster(input.Secrets["database-tls-certificate"])]
	if bytes.Equal(expiredSoon["tls.key"], renewed["tls.key"]) || !bytes.Equal(expiredSoon["ca.crt"], renewed["ca.crt"]) {
		t.Fatal("leaf renewal did not preserve trust while replacing its key")
	}
	if !validPlatformTLSLeaf(renewed, map[string][]byte{}, ca, root.Data["ca.crt"], names, ips, time.Now(), false) {
		t.Fatal("renewed leaf is invalid")
	}
	// Renewal is not complete merely because a Secret changed: a peer serving
	// the old certificate must fail the connection probe.
	oldPair, err := tls.X509KeyPair(expiredSoon["tls.crt"], expiredSoon["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	newPair, err := tls.X509KeyPair(renewed["tls.crt"], renewed["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, test := range []struct {
		name      string
		pair      tls.Certificate
		wantError bool
	}{{"old", oldPair, true}, {"renewed", newPair, false}} {
		t.Run(test.name, func(t *testing.T) {
			server, client := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			defer server.Close()
			defer client.Close()
			go func() {
				_ = tls.Server(server, &tls.Config{Certificates: []tls.Certificate{test.pair}, MinVersion: tls.VersionTLS12}).HandshakeContext(ctx)
			}()
			_, err := verifyPlatformTLSConnection(ctx, client, platformTLSProbeTarget{serverName: "db." + ns.Name + ".svc"}, newPair.Certificate[0], roots)
			if (err != nil) != test.wantError {
				t.Fatalf("served rotation probe error=%v", err)
			}
		})
	}
}

func TestManagedPlatformTLSStopsOnLostLeaseAndForeignIssuer(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	input := op.Spec
	values := map[string]map[string][]byte{}
	lost := errors.New("lease lost")
	err := c.prepareManagedPlatformTLS(context.Background(), state, op, ns, &input, nil, &values, prior, current, func() error { return lost })
	if !errors.Is(err, lost) {
		t.Fatal("lost lease did not fence issuance")
	}
	objects, _ := c.kube.CoreV1().Secrets(ns.Name).List(context.Background(), metav1.ListOptions{})
	if len(objects.Items) != 0 {
		t.Fatal("lost lease still created secret material")
	}
	foreign := &corev1.Secret{ObjectMeta: supabaseTestMeta(op, ns, managedplatform.ManagedTLSIssuerSecret)}
	foreign.OwnerReferences[0].UID = "other-namespace"
	if _, err = c.kube.CoreV1().Secrets(ns.Name).Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareManagedPlatformTLS(context.Background(), state, op, ns, &input, nil, &values, prior, current, func() error { return nil }); err == nil {
		t.Fatal("foreign issuer was adopted")
	}
}

func TestManagedPlatformTLSModeCannotHideNeonCredentialRotation(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	op.Spec = managedplatform.Spec{Kind: "neon", TLSMode: "managed", Secrets: map[string]managedplatform.SecretReference{"compute-auth": {Name: "new", Revision: 2}}}
	previous := op.Spec
	previous.Secrets = map[string]managedplatform.SecretReference{"compute-auth": {Name: "old", Revision: 1}}
	input := op.Spec
	values := map[string]map[string][]byte{}
	previousPointer := &previous
	if err := c.prepareManagedPlatformTLS(context.Background(), state, op, ns, &input, &previousPointer, &values, prior, current, func() error { return nil }); err == nil {
		t.Fatal("certificate management hid an unsupported credential rotation")
	}
}

func TestManagedPlatformRecoveryKeepsSeparateIssuerAndRefusesUnclaimedMaterial(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	ctx := context.Background()
	input := op.Spec
	values := map[string]map[string][]byte{}
	before := func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }
	if err := c.prepareManagedPlatformTLS(ctx, state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	current["namespace."+ns.Name] = store.PlatformResourceClaim{ResourceID: string(ns.UID)}
	restored := op.Spec
	restoredValues := map[string]map[string][]byte{}
	if err := c.readManagedPlatformTLS(ctx, op.PlatformID, &restored, nil, &restoredValues, current); err != nil {
		t.Fatal(err)
	}
	for name, data := range values {
		if !equalSecretData(data, restoredValues[name]) {
			t.Fatal("recovery did not use the existing target identity")
		}
	}
	for name := range values {
		delete(current, "secret."+name)
		break
	}
	restored = op.Spec
	restoredValues = map[string]map[string][]byte{}
	if err := c.readManagedPlatformTLS(ctx, op.PlatformID, &restored, nil, &restoredValues, current); err == nil {
		t.Fatal("recovery accepted a certificate without its durable claim")
	}
	other, otherState, otherOp, otherNS, otherPrior, otherCurrent := platformTLSFixture(t)
	otherOp.PlatformID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherNS = supabaseTestNamespace(otherOp)
	if _, err := other.kube.CoreV1().Namespaces().Create(ctx, otherNS, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	otherInput := otherOp.Spec
	otherValues := map[string]map[string][]byte{}
	if err := other.prepareManagedPlatformTLS(ctx, otherState, otherOp, otherNS, &otherInput, nil, &otherValues, otherPrior, otherCurrent, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	originalRoot, _ := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	separateRoot, _ := other.kube.CoreV1().Secrets(otherNS.Name).Get(ctx, managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	if bytes.Equal(originalRoot.Data["ca.key"], separateRoot.Data["ca.key"]) || bytes.Equal(originalRoot.Data["ca.crt"], separateRoot.Data["ca.crt"]) {
		t.Fatal("recovery target reused source issuer material")
	}
}

func TestManagedPlatformRecoveryAcceptsValidLeafWithinRenewalWindow(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	ctx := context.Background()
	before := func() error { return nil }
	root, ca, signer, err := c.prepareManagedPlatformIssuer(ctx, state, op, ns, prior, current, before, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	current["namespace."+ns.Name] = store.PlatformResourceClaim{ResourceID: string(ns.UID)}
	for _, logical := range managedplatform.ManagedTLSLogicalNames(op.Spec.Kind) {
		names, ips, err := platformTLSNames(op.Spec, op.PlatformID, logical)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := issuePlatformTLSLeaf(map[string][]byte{}, ca, signer, root.Data["ca.crt"], names, ips, time.Now().Add(-24*24*time.Hour), false)
		if err != nil {
			t.Fatal(err)
		}
		if validPlatformTLSLeaf(leaf, map[string][]byte{}, ca, root.Data["ca.crt"], names, ips, time.Now(), false) {
			t.Fatal("six-day leaf was not due for renewal")
		}
		if err = c.applySupabaseSecret(ctx, state, op, ns, platformTLSSecretName(logical, leaf), leaf, prior, current, before); err != nil {
			t.Fatal(err)
		}
	}
	spec := op.Spec
	snapshots := map[string]map[string][]byte{}
	if err = c.readManagedPlatformTLS(ctx, op.PlatformID, &spec, nil, &snapshots, current); err != nil {
		t.Fatal("recovery rejected still-valid TLS material", err)
	}
	if len(snapshots) != 2 {
		t.Fatal("recovery did not resolve its owned TLS snapshots")
	}
}
