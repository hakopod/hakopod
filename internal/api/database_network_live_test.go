package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	managed "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type databaseNetworkProbeOutput struct{ bytes.Buffer }

func (w *databaseNetworkProbeOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 4096 {
		return 0, fmt.Errorf("database network fixture output exceeded its bound")
	}
	return w.Buffer.Write(p)
}

// Fault injection changes only a new development database's owned policy. API
// endpoints remain real and unchanged, and the normal server loop repairs it.
func TestLiveDatabaseNetworkPolicyRefresh(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_NETWORK_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_NETWORK_TEST=1 for named development cluster acceptance")
	}
	if os.Getenv("HAKOPOD_TEST_DATABASE_URL") == "" {
		t.Fatal("requires disposable control-plane PostgreSQL")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("database network acceptance requires k3d-hakopod-dev")
	}
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 20 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	const worker = "k3d-hakopod-database-worker-0"
	// Same-node traffic can bypass NetworkPolicy; the fixture must use a
	// separate development worker to prove a real denied API connection.
	node, err := kube.CoreV1().Nodes().Get(ctx, worker, metav1.GetOptions{})
	if err != nil {
		t.Fatal("requires the separate named development database worker", err)
	}
	_, controlPlane := node.Labels["node-role.kubernetes.io/control-plane"]
	ready := false
	for _, condition := range node.Status.Conditions {
		ready = ready || condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
	}
	if node.UID == "" || controlPlane || !ready || node.DeletionTimestamp != nil {
		t.Fatal("requires the separate named development database worker")
	}
	db, _ := database(t)
	token, err := db.Bootstrap(ctx, "database-network-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	server := &api.Server{Store: db, Cluster: c, Auth: api.AuthConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{29}, 32))}}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := backupRequestClient{t, httpServer, token}
	var stop context.CancelFunc
	var done chan struct{}
	start := func() {
		run, cancel := context.WithCancel(ctx)
		stop, done = cancel, make(chan struct{})
		finished := done
		go func() { defer close(finished); server.RunManagedDatabases(run) }()
	}
	halt := func() {
		if stop != nil {
			stop()
			<-done
			stop = nil
		}
	}
	t.Cleanup(halt)
	start()
	spec := managed.Spec{SchemaVersion: 1, Name: "network-development-fixture", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "250m", Memory: "512Mi", StorageGiB: 1, Placement: managed.Placement{NodeNames: []string{worker}}}
	var operation managed.Operation
	if status := client.request("POST", "/databases", map[string]any{"project": "demo", "environment": "development", "spec": spec}, &operation, store.NewID()); status != 202 {
		t.Fatal("create development database", status)
	}
	d, err := db.DatabaseInternal(ctx, operation.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	var originalPolicy *networkingv1.NetworkPolicy
	t.Cleanup(func() {
		halt()
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Minute)
		defer stop()
		// Restore only the exact fixture policy if a failed assertion left it
		// stale, so CNPG finalization does not depend on the injected outage.
		if originalPolicy != nil {
			current, e := kube.NetworkingV1().NetworkPolicies(originalPolicy.Namespace).Get(cleanup, originalPolicy.Name, metav1.GetOptions{})
			if e == nil && current.UID == originalPolicy.UID && current.Labels["hakopod.io/database-id"] == d.ID && current.Labels["app.kubernetes.io/managed-by"] == "hakopod" && !reflect.DeepEqual(current.Spec, originalPolicy.Spec) {
				current.Spec = originalPolicy.Spec
				if _, e = kube.NetworkingV1().NetworkPolicies(current.Namespace).Update(cleanup, current, metav1.UpdateOptions{}); e != nil {
					t.Error("fixture policy cleanup failed", e)
				}
			}
		}
		for cleanup.Err() == nil {
			removed, e := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if e != nil {
				t.Error("fixture cleanup failed", e)
				return
			}
			if removed {
				return
			}
			select {
			case <-cleanup.Done():
			case <-time.After(2 * time.Second):
			}
		}
		t.Error("fixture namespace and volumes were not reclaimed")
	})
	waitReady := func(after time.Time) {
		t.Helper()
		for ctx.Err() == nil {
			var e error
			d, e = db.DatabaseInternal(ctx, operation.DatabaseID)
			if e != nil {
				t.Fatal(e)
			}
			if d.Status == "failed" {
				t.Fatal("fixture lifecycle failed", d.Observation.Message)
			}
			if d.Status == "ready" && d.Observation.Status == "ready" && d.Observation.ObservedAt.After(after) {
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		t.Fatal("normal database loop did not observe ready fixture members")
	}
	waitReady(time.Time{})
	halt()
	if d.Recovery != nil || len(d.Observation.Members) != 1 || d.Observation.Members[0].Node != worker {
		t.Fatal("requires an ordinary standalone database on the separate worker")
	}
	member := d.Observation.Members[0]
	exec := func(command []string) (string, error) {
		step, stop := context.WithTimeout(ctx, 12*time.Second)
		defer stop()
		out := &databaseNetworkProbeOutput{}
		err := c.DatabaseExec(step, d, member, command, nil, out)
		return strings.TrimSpace(out.String()), err
	}
	query := func(sql string) string {
		t.Helper()
		result, e := exec([]string{"psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", sql})
		if e != nil {
			t.Fatal("fixture SQL failed", e)
		}
		return result
	}
	const sentinel = "kept-through-network-refresh"
	if query("CREATE TABLE network_fixture(value text NOT NULL); INSERT INTO network_fixture VALUES('"+sentinel+"'); SELECT value FROM network_fixture") != sentinel {
		t.Fatal("fixture sentinel was not stored")
	}
	ns := cluster.DatabaseNamespace(d.ID)
	policies := kube.NetworkingV1().NetworkPolicies(ns)
	originalPolicy, err = policies.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || originalPolicy.UID == "" || originalPolicy.Labels["hakopod.io/database-id"] != d.ID || originalPolicy.Labels["app.kubernetes.io/managed-by"] != "hakopod" {
		t.Fatal("fixture policy ownership unavailable", err)
	}
	service, err := kube.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slices, err := kube.DiscoveryV1().EndpointSlices("default").List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes", Limit: 8})
	if err != nil || slices.Continue != "" {
		t.Fatal("bounded API discovery unavailable", err)
	}
	serviceCIDRs, endpointCIDRs := map[string]bool{}, map[string]bool{}
	for _, ip := range service.Spec.ClusterIPs {
		address, e := netip.ParseAddr(ip)
		if e != nil {
			t.Fatal(e)
		}
		serviceCIDRs[netip.PrefixFrom(address, address.BitLen()).String()] = true
	}
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			for _, ip := range endpoint.Addresses {
				address, e := netip.ParseAddr(ip)
				if e != nil || !address.Is4() {
					t.Fatal("this development fault fixture requires IPv4 API endpoints")
				}
				endpointCIDRs[netip.PrefixFrom(address, 32).String()] = true
			}
		}
	}
	stale := originalPolicy.DeepCopy()
	target := ""
	for i := range stale.Spec.Egress {
		for j := range stale.Spec.Egress[i].To {
			peer := &stale.Spec.Egress[i].To[j]
			if peer.IPBlock == nil || serviceCIDRs[peer.IPBlock.CIDR] {
				continue
			}
			rule := stale.Spec.Egress[i]
			if !endpointCIDRs[peer.IPBlock.CIDR] || len(rule.Ports) != 1 || rule.Ports[0].Port == nil || rule.Ports[0].Port.IntVal == 0 {
				t.Fatal("fixture policy has an unexpected non-Service API peer")
			}
			if target == "" {
				prefix, e := netip.ParsePrefix(peer.IPBlock.CIDR)
				if e != nil {
					t.Fatal(e)
				}
				target = net.JoinHostPort(prefix.Addr().String(), strconv.Itoa(int(rule.Ports[0].Port.IntVal)))
			}
			peer.IPBlock.CIDR = "192.0.2.254/32"
		}
	}
	if target == "" || endpointCIDRs["192.0.2.254/32"] || serviceCIDRs["192.0.2.254/32"] {
		t.Fatal("fixture API endpoint selection is unsafe or empty")
	}
	probe := func() string {
		t.Helper()
		// CA verification and a fresh TCP/TLS connection need no token or key.
		const command = `if timeout 5 openssl s_client -connect "$1" -servername kubernetes.default.svc -verify_hostname kubernetes.default.svc -CAfile /var/run/secrets/kubernetes.io/serviceaccount/ca.crt -verify_return_error -brief </dev/null >/dev/null 2>&1; then printf 'connected\n'; else code=$?; if [ "$code" = 124 ]; then printf 'blocked\n'; else printf 'probe-failed\n'; fi; fi`
		result, e := exec([]string{"sh", "-c", command, "database-network-development-fixture", target})
		if e != nil {
			t.Fatal("fresh API probe execution failed", e)
		}
		return result
	}
	if probe() != "connected" {
		t.Fatal("fixture cannot establish a verified fresh API connection before fault injection")
	}
	before := databaseNetworkLiveIdentities(t, ctx, kube, objects, d)
	claim, err := db.ClaimDatabaseMaintenance(ctx, d.ID, d.Revision)
	if err != nil || claim == nil {
		t.Fatal("could not exclusively fence fixture fault injection", err)
	}
	if err = claim.Check(ctx); err == nil {
		_, err = policies.Update(ctx, stale, metav1.UpdateOptions{})
	}
	claim.Release()
	if err != nil {
		t.Fatal("could not inject the exact stale peer", err)
	}
	blocked := false
	for attempt := 0; attempt < 4; attempt++ {
		result := probe()
		if result == "blocked" {
			blocked = true
			break
		}
		if result != "connected" {
			t.Fatal("API fault probe failed for a reason other than blocked traffic")
		}
		time.Sleep(time.Second)
	}
	if !blocked {
		t.Fatal("stale API endpoint did not block a fresh connection; fixture cannot qualify NetworkPolicy recovery")
	}
	broken, err := policies.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || broken.UID != originalPolicy.UID || !reflect.DeepEqual(broken.Spec, stale.Spec) {
		t.Fatal("fault changed before normal-loop recovery", err)
	}
	resumed := time.Now().UTC()
	start()
	for ctx.Err() == nil {
		current, e := policies.Get(ctx, "database", metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		if current.UID != originalPolicy.UID {
			t.Fatal("normal observation replaced the fixture policy identity")
		}
		if reflect.DeepEqual(current.Spec, originalPolicy.Spec) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	if ctx.Err() != nil {
		t.Fatal("normal observation loop did not repair the stale API peer")
	}
	if probe() != "connected" {
		t.Fatal("repaired policy did not restore a verified fresh API connection")
	}
	waitReady(resumed)
	halt()
	if query("SELECT value FROM network_fixture") != sentinel {
		t.Fatal("fixture database content changed")
	}
	if after := databaseNetworkLiveIdentities(t, ctx, kube, objects, d); !reflect.DeepEqual(before, after) {
		t.Fatal("network repair changed fixture controller, pod, storage, unrelated policies or actual API discovery")
	}
	var operations int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM managed_database_operations WHERE database_id=$1", d.ID).Scan(&operations); err != nil || operations != 1 || d.Recovery != nil || d.Revision != 1 {
		t.Fatal("network repair relied on another lifecycle operation or recovery record", err)
	}
	t.Log("Real stale API peer blocked a fresh verified connection; the normal observation loop restored exact policy, fresh API connectivity and PostgreSQL readiness with sentinel data and recorded identities preserved.")
}

func databaseNetworkLiveIdentities(t *testing.T, ctx context.Context, kube kubernetes.Interface, objects dynamic.Interface, d managed.Resource) map[string]string {
	t.Helper()
	result := map[string]string{}
	record := func(name string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		result[name] = hex.EncodeToString(sum[:])
	}
	ns := cluster.DatabaseNamespace(d.ID)
	controller, err := objects.Resource(schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}).Namespace(ns).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || controller.GetUID() == "" {
		t.Fatal("fixture controller identity unavailable", err)
	}
	record("controller", []any{controller.GetUID(), controller.GetGeneration(), controller.Object["spec"]})
	pods, err := kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=database", FieldSelector: "status.phase!=Failed,status.phase!=Succeeded", Limit: 8})
	if err != nil || pods.Continue != "" || len(pods.Items) != 1 {
		t.Fatal("fixture member inventory changed", err)
	}
	for _, pod := range pods.Items {
		record("pod/"+pod.Name, []any{pod.UID, pod.Spec, pod.Status.ContainerStatuses})
	}
	claims, err := kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{Limit: 8})
	if err != nil || claims.Continue != "" || len(claims.Items) == 0 || len(claims.Items) > 8 {
		t.Fatal("fixture claim inventory unavailable", err)
	}
	for _, claim := range claims.Items {
		if claim.Status.Phase != corev1.ClaimBound || claim.Spec.VolumeName == "" {
			t.Fatal("fixture claim is not bound")
		}
		record("claim/"+claim.Name, []any{claim.UID, claim.Spec, claim.Status.Phase, claim.Status.Capacity})
		volume, e := kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if e != nil || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.UID != claim.UID {
			t.Fatal("fixture volume identity unavailable", e)
		}
		record("volume/"+volume.Name, []any{volume.UID, volume.Spec, volume.Status.Phase})
	}
	policies, err := kube.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{Limit: 256})
	if err != nil || policies.Continue != "" || len(policies.Items) > 256 {
		t.Fatal("bounded unrelated policy inventory unavailable", err)
	}
	for _, policy := range policies.Items {
		record("policy/"+policy.Namespace+"/"+policy.Name, []any{policy.UID, policy.Labels, policy.Annotations, policy.Spec})
	}
	service, err := kube.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record("api-service", []any{service.UID, service.Spec})
	slices, err := kube.DiscoveryV1().EndpointSlices("default").List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes", Limit: 8})
	if err != nil || slices.Continue != "" || len(slices.Items) > 8 {
		t.Fatal("bounded API discovery unavailable", err)
	}
	for _, slice := range slices.Items {
		record("api-slice/"+slice.Name, []any{slice.UID, slice.AddressType, slice.Endpoints, slice.Ports})
	}
	return result
}
