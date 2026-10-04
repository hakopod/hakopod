package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"strings"
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

func TestPlatformTLSSecretLogicalNamesDoNotOverlap(t *testing.T) {
	data := map[string][]byte{"tls.crt": []byte("certificate"), "tls.key": []byte("key")}
	controller := platformTLSSecretName("controller-auth", data)
	database := platformTLSSecretName("controller-database-password", data)
	if !platformTLSSecretMatchesLogical(controller, "controller-auth") || !platformTLSSecretMatchesLogical(database, "controller-database-password") {
		t.Fatal("exact managed TLS snapshot name was rejected")
	}
	if platformTLSSecretMatchesLogical(database, "controller-auth") || platformTLSSecretMatchesLogical(controller, "controller-database-password") {
		t.Fatal("overlapping managed TLS logical name was accepted")
	}
	for _, invalid := range []string{controller + "0", strings.Replace(controller, "-r1", "-r2", 1), strings.Replace(controller, "a", "z", 1)} {
		if platformTLSSecretMatchesLogical(invalid, "controller-auth") {
			t.Fatal("malformed managed TLS snapshot name was accepted")
		}
	}
}

func TestManagedNeonTLSIssuesAndReplaysSEC1ComputeIdentity(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	op.Spec = managedplatform.Spec{Kind: "neon", TLSMode: "managed", Secrets: map[string]managedplatform.SecretReference{}, Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3}}
	values := map[string]map[string][]byte{}
	for _, logical := range managedplatform.ManagedTLSLogicalNames("neon") {
		op.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		values[logical+"-r1"] = map[string][]byte{}
	}
	before := func() error { return state.HeartbeatManagedPlatformOperation(context.Background(), op) }
	input := op.Spec
	if err := c.prepareManagedPlatformTLS(context.Background(), state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	computeName := secretSnapshotNameForCluster(input.Secrets["compute-auth"])
	compute := copySecretData(values[computeName])
	block, rest := pem.Decode(compute["tls.key"])
	if block == nil || block.Type != "EC PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("managed Neon compute identity was not issued with a SEC1 key")
	}
	if _, err := tls.X509KeyPair(compute["tls.crt"], compute["tls.key"]); err != nil {
		t.Fatal("managed Neon compute certificate and SEC1 key do not match", err)
	}
	replayedInput := op.Spec
	replayedValues := map[string]map[string][]byte{}
	for _, logical := range managedplatform.ManagedTLSLogicalNames("neon") {
		replayedValues[logical+"-r1"] = map[string][]byte{}
	}
	if err := c.prepareManagedPlatformTLS(context.Background(), state, op, ns, &replayedInput, nil, &replayedValues, prior, current, before); err != nil {
		t.Fatal(err)
	}
	if replayedName := secretSnapshotNameForCluster(replayedInput.Secrets["compute-auth"]); replayedName != computeName || !equalSecretData(replayedValues[replayedName], compute) {
		t.Fatal("managed Neon compute identity changed during replay")
	}
}

func TestManagedNeonTLSRenewalKeepsIssuerAndReplaysSEC1Leaf(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	op.Spec = managedplatform.Spec{Kind: "neon", TLSMode: "managed", Secrets: map[string]managedplatform.SecretReference{}, Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3}}
	values := map[string]map[string][]byte{}
	for _, logical := range managedplatform.ManagedTLSLogicalNames("neon") {
		op.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		values[logical+"-r1"] = map[string][]byte{}
	}
	ctx := context.Background()
	before := func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }
	root, ca, signer, err := c.prepareManagedPlatformIssuer(ctx, state, op, ns, prior, current, before, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	names, ips, err := platformTLSNames(op.Spec, op.PlatformID, "compute-auth")
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := issuePlatformTLSLeaf(map[string][]byte{}, ca, signer, root.Data["ca.crt"], names, ips, time.Now().Add(-25*24*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if err = normalizeNeonComputeTLSKey(expiring); err != nil {
		t.Fatal(err)
	}
	if err = c.applySupabaseSecret(ctx, state, op, ns, platformTLSSecretName("compute-auth", expiring), expiring, prior, current, before); err != nil {
		t.Fatal(err)
	}
	input := op.Spec
	if err = c.prepareManagedPlatformTLS(ctx, state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	computeName := secretSnapshotNameForCluster(input.Secrets["compute-auth"])
	renewed := copySecretData(values[computeName])
	block, _ := pem.Decode(renewed["tls.key"])
	if block == nil || block.Type != "EC PRIVATE KEY" || bytes.Equal(renewed["tls.crt"], expiring["tls.crt"]) || !bytes.Equal(renewed["ca.crt"], root.Data["ca.crt"]) {
		t.Fatal("managed Neon renewal did not replace the leaf with SEC1 while preserving its issuer")
	}
	replayedInput := op.Spec
	replayedValues := map[string]map[string][]byte{}
	for _, logical := range managedplatform.ManagedTLSLogicalNames("neon") {
		replayedValues[logical+"-r1"] = map[string][]byte{}
	}
	if err = c.prepareManagedPlatformTLS(ctx, state, op, ns, &replayedInput, nil, &replayedValues, prior, current, before); err != nil {
		t.Fatal(err)
	}
	if replayedName := secretSnapshotNameForCluster(replayedInput.Secrets["compute-auth"]); replayedName != computeName || !equalSecretData(replayedValues[replayedName], renewed) {
		t.Fatal("renewed managed Neon compute identity changed during replay")
	}
}

func TestManagedNeonRecoverySelectsSEC1ComputeSnapshot(t *testing.T) {
	c, state, op, ns, prior, current := platformTLSFixture(t)
	op.Spec = managedplatform.Spec{Kind: "neon", TLSMode: "managed", Secrets: map[string]managedplatform.SecretReference{}, Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3}}
	values := map[string]map[string][]byte{}
	for _, logical := range managedplatform.ManagedTLSLogicalNames("neon") {
		op.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		values[logical+"-r1"] = map[string][]byte{}
	}
	ctx := context.Background()
	before := func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }
	input := op.Spec
	if err := c.prepareManagedPlatformTLS(ctx, state, op, ns, &input, nil, &values, prior, current, before); err != nil {
		t.Fatal(err)
	}
	current["namespace."+ns.Name] = store.PlatformResourceClaim{ResourceID: string(ns.UID)}
	sec1Name := secretSnapshotNameForCluster(input.Secrets["compute-auth"])
	sec1Secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, sec1Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	oldData := copySecretData(sec1Secret.Data)
	block, _ := pem.Decode(oldData["tls.key"])
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	oldData["tls.key"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	oldName := platformTLSSecretName("compute-auth", oldData)
	if err = c.applySupabaseSecret(ctx, state, op, ns, oldName, oldData, prior, current, before); err != nil {
		t.Fatal(err)
	}
	if err = c.kube.CoreV1().Secrets(ns.Name).Delete(ctx, sec1Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	restored, restoredValues := op.Spec, map[string]map[string][]byte{}
	if err = c.readManagedPlatformTLS(ctx, op.PlatformID, &restored, nil, &restoredValues, current); err == nil {
		t.Fatal("recovery accepted only a provider-incompatible compute key")
	}
	if _, err = c.kube.CoreV1().Secrets(ns.Name).Create(ctx, sec1Secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	restored, restoredValues = op.Spec, map[string]map[string][]byte{}
	if err = c.readManagedPlatformTLS(ctx, op.PlatformID, &restored, nil, &restoredValues, current); err != nil {
		t.Fatal("recovery did not select the compatible compute snapshot", err)
	}
	selected := restoredValues[secretSnapshotNameForCluster(restored.Secrets["compute-auth"])]
	if !equalSecretData(selected, sec1Secret.Data) || bytes.Equal(selected["tls.key"], oldData["tls.key"]) {
		t.Fatal("recovery selected the provider-incompatible compute snapshot")
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
