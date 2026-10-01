package cluster

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func databasePublicEndpointSecurityFixture(t *testing.T, claimState string, baseline string) (*Client, database.Resource, database.PublicEndpoint) {
	t.Helper()
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "postgresql"}}
	endpoint := database.PublicEndpoint{ID: "fedcba9876543210fedcba9876543210", Revision: 2, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database.public.example.test", Address: "192.0.2.10", Port: 15432}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: map[string]string{managedBy: "hakopod", databaseOwner: d.ID}}}
	claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: publicTCPClaimName(endpoint.Allocation.Port), Namespace: "haproxy-controller", Labels: map[string]string{managedBy: "hakopod", databaseOwner: d.ID, databasePublicEndpointClaimLabel: "true", databasePublicEndpointLabel: endpoint.ID}}, Data: map[string]string{"database_id": d.ID, "endpoint_id": endpoint.ID, "allocation_id": endpoint.Allocation.ID, databasePublicEndpointClaimStateKey: claimState}}
	if baseline != "" {
		claim.Data[databasePublicEndpointClaimBaselineKey] = baseline
		claim.Data[databasePublicEndpointClaimRevisionKey] = "2"
	}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{publicTCPResource: "TCPList"})
	c := &Client{kube: kubefake.NewClientset(namespace, claim), dynamic: dynamic, options: Options{ProxyNamespace: claim.Namespace}, databasePublicTCPAck: func(context.Context, database.Resource, database.PublicEndpoint, []any, map[string]string) error {
		return nil
	}}
	return c, d, endpoint
}

func TestDatabasePublicEndpointMissingRouteRequiresDurableClosureBaseline(t *testing.T) {
	c, d, endpoint := databasePublicEndpointSecurityFixture(t, databasePublicEndpointPublished, "")
	err := c.closeDatabasePublicEndpointRoute(context.Background(), d, endpoint, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "without a durable closure baseline") {
		t.Fatal("missing route bypassed closure proof", err)
	}
}

func TestDatabasePublicEndpointMissingRouteCompletesPersistedClosure(t *testing.T) {
	c, d, endpoint := databasePublicEndpointSecurityFixture(t, databasePublicEndpointPublished, `{"ingress-pod":"52"}`)
	if err := c.closeDatabasePublicEndpointRoute(context.Background(), d, endpoint, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	claim, err := c.databasePublicEndpointClaim(context.Background(), d, endpoint)
	if err != nil || claim.Data[databasePublicEndpointClaimStateKey] != databasePublicEndpointClosed || claim.Data[databasePublicEndpointClaimAckKey] != publicTCPHash([]any{}) {
		t.Fatal("closure acknowledgement was not persisted", err, claim)
	}
}

func TestDatabasePublicEndpointBackendRequiresOwnedNumericServiceAddress(t *testing.T) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "postgresql"}}
	valid := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "database-rw", Labels: map[string]string{managedBy: "hakopod", databaseOwner: d.ID}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.17", Ports: []corev1.ServicePort{{Name: "postgresql", Protocol: corev1.ProtocolTCP, Port: 5432}}}}
	if address, err := databasePublicEndpointBackendServiceAddress(valid, d, database.PublicEndpointRoute{BackendService: "database-rw", BackendPort: 5432, BackendPortName: "postgresql"}); err != nil || address != "10.43.0.17" {
		t.Fatal(address, err)
	}
	for _, change := range []func(*corev1.Service){
		func(service *corev1.Service) { service.Spec.ClusterIP = "database-rw.namespace.svc" },
		func(service *corev1.Service) { service.Spec.ClusterIP = corev1.ClusterIPNone },
		func(service *corev1.Service) { service.Spec.Ports[0].Port = 6432 },
		func(service *corev1.Service) { service.Labels[databaseOwner] = "another-database" },
	} {
		candidate := valid.DeepCopy()
		change(candidate)
		if _, err := databasePublicEndpointBackendServiceAddress(candidate, d, database.PublicEndpointRoute{BackendService: "database-rw", BackendPort: 5432, BackendPortName: "postgresql"}); err == nil {
			t.Fatal("unsafe backend service address accepted")
		}
	}
}

func TestDatabasePublicEndpointPublishRetryReusesPendingReloadBaseline(t *testing.T) {
	d := database.Resource{ID: "0123456789abcdef0123456789abcdef", Spec: database.Spec{Engine: "postgresql"}}
	endpoint := database.PublicEndpoint{
		ID:       "fedcba9876543210fedcba9876543210",
		Revision: 2,
		Spec:     database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32},
		Allocation: database.PublicEndpointAllocation{
			ID: "allocation", Host: "database.public.example.test", Address: "192.0.2.10", Port: 15432,
		},
	}
	desired, err := databasePublicEndpointObject(d, endpoint, "namespace-uid", "haproxy")
	if err != nil {
		t.Fatal(err)
	}
	current := desired.DeepCopy()
	baseline := map[string]string{"ingress-a": "41", "ingress-b": "77"}
	publicTCPSetPending(current, baseline)
	got, pending, err := databasePublicEndpointPendingReload(current, desired, mustDatabasePublicEndpointModels(t, d, endpoint))
	if err != nil || !pending || len(got) != 2 || got["ingress-a"] != "41" || got["ingress-b"] != "77" {
		t.Fatal("accepted reload baseline was not resumed", got, pending, err)
	}
	changed := desired.DeepCopy()
	changed.Object["spec"] = []any{}
	if _, pending, err = databasePublicEndpointPendingReload(current, changed, mustDatabasePublicEndpointModels(t, d, endpoint)); err != nil || pending {
		t.Fatal("a different route spec was treated as the accepted pending reload", pending, err)
	}
}

func TestDatabasePublicEndpointRevocationRetainsProofUntilReleasePhase(t *testing.T) {
	c, d, endpoint := databasePublicEndpointSecurityFixture(t, databasePublicEndpointPublished, `{"ingress-pod":"52"}`)
	ctx := context.Background()
	if err := c.closeDatabasePublicEndpointRoute(ctx, d, endpoint, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	claim, err := c.databasePublicEndpointClaim(ctx, d, endpoint)
	if err != nil || claim.Data[databasePublicEndpointClaimStateKey] != databasePublicEndpointClosed || claim.Data[databasePublicEndpointClaimAckKey] != publicTCPHash([]any{}) {
		t.Fatal("revocation did not retain durable closure proof", err, claim)
	}
	if err = c.ReleaseDatabasePublicEndpoint(ctx, d, endpoint, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = c.ReleaseDatabasePublicEndpoint(ctx, d, endpoint, func() error { return nil }); err != nil {
		t.Fatal("release retry did not accept an already removed claim", err)
	}
}

func TestDatabasePublicEndpointAcknowledgementRechecksAuthorityImmediatelyBeforeWrite(t *testing.T) {
	c, d, endpoint := databasePublicEndpointSecurityFixture(t, databasePublicEndpointClosed, `{"ingress-pod":"52"}`)
	endpoint.Spec = database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}
	desired, err := databasePublicEndpointObject(d, endpoint, "namespace-uid", "haproxy")
	if err != nil {
		t.Fatal(err)
	}
	desired.SetUID("route-uid")
	wanted := mustDatabasePublicEndpointModels(t, d, endpoint)
	baseline := map[string]string{"ingress-pod": "52"}
	publicTCPSetPending(desired, baseline)
	api := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID))
	if _, err = api.Create(context.Background(), desired, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	lostAuthority := errors.New("operation lease expired")
	err = c.persistDatabasePublicEndpointAcknowledgement(context.Background(), d, endpoint, wanted, baseline, desired.GetUID(), func() error { return lostAuthority })
	if !errors.Is(err, lostAuthority) {
		t.Fatal("acknowledgement ignored the final authority fence", err)
	}
	current, err := api.Get(context.Background(), desired.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current.GetAnnotations()["hakopod.io/tcp-acknowledged"] != "" {
		t.Fatal("acknowledgement was written after authority expired")
	}
}

func TestDatabasePublicEndpointCleanupClosesCreatedRouteWithSystemFence(t *testing.T) {
	c, d, endpoint := databasePublicEndpointSecurityFixture(t, databasePublicEndpointPublished, `{"ingress-pod":"52"}`)
	endpoint.Spec = database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}
	endpoint.Allocation.Host = "database-15432.example.test"
	endpoint.Allocation.Address = "192.0.2.10"
	route, err := databasePublicEndpointObject(d, endpoint, "namespace-uid", "haproxy")
	if err != nil {
		t.Fatal(err)
	}
	route.SetUID("created-route")
	if _, err = c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID)).Create(context.Background(), route, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	if err = c.closeDatabasePublicEndpointRoute(context.Background(), d, endpoint, func() error {
		checks++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	current, err := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID)).Get(context.Background(), route.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	models, _, err := unstructured.NestedSlice(current.Object, "spec")
	if err != nil || len(models) != 0 || checks < 2 {
		t.Fatal("fenced cleanup did not close and acknowledge the created route", len(models), checks, err)
	}
	claim, err := c.databasePublicEndpointClaim(context.Background(), d, endpoint)
	if err != nil || claim.Data[databasePublicEndpointClaimStateKey] != databasePublicEndpointClosed || claim.Data[databasePublicEndpointClaimAckKey] != publicTCPHash([]any{}) {
		t.Fatal("fenced cleanup did not preserve durable closure proof", err, claim)
	}
}

func TestDatabasePublicEndpointSessionClosureScriptFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		socat   string
		success bool
	}{
		{
			name:  "malformed master inventory",
			socat: "#!/bin/sh\ncat >/dev/null\nprintf 'not-a-master-inventory\\n'\n",
		},
		{
			name: "oversized session inventory",
			socat: `#!/bin/sh
input=$(cat)
case "$input" in
  "show proc"*) printf '#<PID> <type>\n1 master\n42 worker\n' ;;
  "@!42 show info"*) printf 'Pid: 42\n' ;;
  "@!42 show sess"*) yes '0x1:' | head -n 220000 ;;
  *) exit 1 ;;
esac
`,
		},
		{
			name:    "worker exits during inventory",
			success: true,
			socat: `#!/bin/sh
input=$(cat)
case "$input" in
  "show proc"*)
    count=0
    [ ! -f "$MOCK_STATE" ] || count=$(cat "$MOCK_STATE")
    count=$((count+1)); printf '%s' "$count" > "$MOCK_STATE"
    if [ "$count" -eq 1 ]; then worker=41; else worker=52; fi
    printf '#<PID> <type>\n1 master\n%s worker\n' "$worker"
    ;;
  "@!41 show info"*) exit 1 ;;
  "@!52 show info"*) printf 'Pid: 52\n' ;;
  "@!52 show sess"*) : ;;
  *) exit 1 ;;
esac
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := runDatabasePublicEndpointSessionClosureScript(t, test.socat)
			if test.success && (err != nil || string(output) != "CLOSED\n") {
				t.Fatal("valid master-worker race did not close", string(output), err)
			}
			if !test.success && err == nil {
				t.Fatal("unsafe master CLI output was accepted", string(output))
			}
		})
	}
}

func runDatabasePublicEndpointSessionClosureScript(t *testing.T, socat string) ([]byte, error) {
	t.Helper()
	// TempDir includes the subtest name, which can exceed Unix socket path limits.
	dir, err := os.MkdirTemp("", "hdb-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "socat"), []byte(socat), 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "master.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	command := exec.Command("sh", "-c", databasePublicEndpointSessionClosureScript, "test-session-closure", "tcpcr_fixture", socket)
	command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "MOCK_STATE="+filepath.Join(dir, "state"))
	return command.CombinedOutput()
}

func mustDatabasePublicEndpointModels(t *testing.T, d database.Resource, endpoint database.PublicEndpoint) []any {
	t.Helper()
	models, err := databasePublicEndpointModels(d, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return models
}
