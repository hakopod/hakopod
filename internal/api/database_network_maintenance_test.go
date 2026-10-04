package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// Kubernetes responses are controlled fixtures. The observation and maintenance
// claims use real isolated PostgreSQL, including loss of a lease before a write.
type databaseNetworkObservationFixture struct {
	t                    *testing.T
	db                   *store.Store
	server               *Server
	d                    database.Resource
	mu                   sync.Mutex
	policy               *networkingv1.NetworkPolicy
	endpoint             string
	checking             bool
	expireLease          bool
	discoveryUnavailable bool
	writes               int
	trace                []string
}

func newDatabaseNetworkObservationFixture(t *testing.T) *databaseNetworkObservationFixture {
	t.Helper()
	f := &databaseNetworkObservationFixture{t: t, db: actionsDatabase(t), endpoint: "192.0.2.10"}
	ctx := context.Background()
	key, err := f.db.Bootstrap(ctx, "database-network-development-fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.db.Authenticate(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	f.d = database.Resource{ID: store.NewID(), Project: "demo", Environment: "development",
		Spec: database.Spec{SchemaVersion: 1, Name: "network-development-fixture", Engine: "postgresql", Version: "17",
			Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}.WithSecureDefaults(),
		EncryptedCredentials: []byte("sealed-development-fixture")}
	if _, err = f.db.AcceptDatabase(ctx, principal, f.d, 0, "network-development-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := f.db.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A completed lifecycle and an unhealthy observation are independent. No
	// fixture Kubernetes readiness or successful native probe is claimed here.
	observation := database.Observation{Status: "unhealthy", Revision: 1, ObservedAt: time.Now().Add(-time.Minute)}
	if err = f.db.RecordDatabaseStep(ctx, op, observation, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	f.d, err = f.db.DatabaseInternal(ctx, f.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	kube := httptest.NewServer(http.HandlerFunc(f.serveKubernetes))
	t.Cleanup(kube.Close)
	c, err := cluster.NewWithConfig(&rest.Config{Host: kube.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}}, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.server = &Server{Store: f.db, Cluster: c}
	if err = c.ReconcileDatabaseNetworkPolicy(ctx, f.d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.endpoint, f.checking, f.writes = "192.0.2.20", true, 0
	f.trace = nil
	f.mu.Unlock()
	return f
}

func (f *databaseNetworkObservationFixture) serveKubernetes(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ns := cluster.DatabaseNamespace(f.d.ID)
	labels := map[string]string{"hakopod.io/database-id": f.d.ID, "app.kubernetes.io/managed-by": "hakopod"}
	policyPath := "/apis/networking.k8s.io/v1/namespaces/" + ns + "/networkpolicies"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/default/services/kubernetes":
		write(w, http.StatusOK, &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.43.0.1"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices":
		if r.URL.Query().Get("limit") != "8" || r.URL.Query().Get("labelSelector") != "kubernetes.io/service-name=kubernetes" {
			f.t.Error("API endpoint discovery lost its bound or service scope")
		}
		if f.discoveryUnavailable {
			http.Error(w, "fixture discovery unavailable", http.StatusServiceUnavailable)
			return
		}
		port := int32(6443)
		write(w, http.StatusOK, &discoveryv1.EndpointSliceList{TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSliceList"}, Items: []discoveryv1.EndpointSlice{{AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{f.endpoint}}}}}})
	case r.Method == http.MethodGet && r.URL.Path == policyPath+"/database":
		if f.policy == nil {
			write(w, http.StatusNotFound, metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound})
			return
		}
		if f.expireLease {
			f.expireLease = false
			if _, err := f.db.Pool.Exec(r.Context(), "UPDATE managed_databases SET maintenance_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", f.d.ID); err != nil {
				f.t.Error(err)
			}
		}
		write(w, http.StatusOK, f.policy)
	case r.Method == http.MethodPost && r.URL.Path == policyPath && !f.checking:
		var policy networkingv1.NetworkPolicy
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			f.t.Error(err)
			http.Error(w, "invalid fixture policy", http.StatusBadRequest)
			return
		}
		policy.UID, policy.ResourceVersion = "network-fixture-policy", "1"
		f.policy = &policy
		write(w, http.StatusCreated, f.policy)
	case r.Method == http.MethodPut && r.URL.Path == policyPath+"/database":
		f.writes++
		f.trace = append(f.trace, "policy-update")
		var leased bool
		if err := f.db.Pool.QueryRow(r.Context(), "SELECT maintenance_lease<>'' AND maintenance_lease_until>clock_timestamp() AND revision=$2 FROM managed_databases WHERE id=$1", f.d.ID, f.d.Revision).Scan(&leased); err != nil || !leased {
			f.t.Error("policy update was not protected by the current maintenance lease", err)
			http.Error(w, "fixture lease required", http.StatusForbidden)
			return
		}
		var policy networkingv1.NetworkPolicy
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			f.t.Error(err)
			http.Error(w, "invalid fixture policy", http.StatusBadRequest)
			return
		}
		if policy.UID != f.policy.UID || policy.ResourceVersion != f.policy.ResourceVersion || policy.Name != f.policy.Name || policy.Namespace != f.policy.Namespace {
			f.t.Error("policy mutation lost object identity or its resource-version precondition")
			http.Error(w, "fixture policy conflict", http.StatusConflict)
			return
		}
		policy.ResourceVersion = "2"
		f.policy = &policy
		write(w, http.StatusOK, f.policy)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/"+ns:
		f.trace = append(f.trace, "namespace-read")
		write(w, http.StatusOK, &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels}})
	case r.Method == http.MethodGet && r.URL.Path == "/apis/postgresql.cnpg.io/v1/namespaces/"+ns+"/clusters/database":
		write(w, http.StatusOK, map[string]any{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "metadata": map[string]any{"name": "database", "namespace": ns, "uid": "controller-fixture", "labels": labels, "annotations": map[string]string{"hakopod.io/database-revision": fmt.Sprint(f.d.Revision)}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/"+ns+"/pods":
		// Empty synthetic inventory makes the independent probe honestly pending.
		write(w, http.StatusOK, &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}})
	default:
		if r.Method != http.MethodGet {
			f.writes++
			f.t.Errorf("unexpected Kubernetes mutation: %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}
}

func (f *databaseNetworkObservationFixture) observed(t *testing.T) database.Resource {
	t.Helper()
	d, err := f.db.DatabaseInternal(context.Background(), f.d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "ready" || d.Revision != f.d.Revision || d.Recovery != nil || !d.Observation.ObservedAt.After(f.d.Observation.ObservedAt) {
		t.Fatal("maintenance changed lifecycle state or failed to record a fresh ordinary observation")
	}
	var operations int
	if err = f.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM managed_database_operations WHERE database_id=$1", d.ID).Scan(&operations); err != nil || operations != 1 {
		t.Fatal("observation introduced a synthetic lifecycle operation", err)
	}
	return d
}

func TestDatabaseObservationRefreshesAPIEgressWhileUnhealthy(t *testing.T) {
	f := newDatabaseNetworkObservationFixture(t)
	if f.d.Recovery != nil || f.d.Observation.Status != "unhealthy" {
		t.Fatal("fixture must start with an unhealthy ordinary database")
	}
	f.server.refreshDatabaseObservation(context.Background())
	d := f.observed(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 || !reflect.DeepEqual(f.trace, []string{"namespace-read", "policy-update", "namespace-read"}) {
		t.Fatal("ordinary observation did not refresh the policy under lease before probing", f.trace)
	}
	peers := map[string]bool{}
	for _, rule := range f.policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				peers[peer.IPBlock.CIDR] = true
			}
		}
	}
	if !reflect.DeepEqual(peers, map[string]bool{"10.43.0.1/32": true, "192.0.2.20/32": true}) || d.Observation.Status != "pending" {
		t.Fatal("ordinary observation retained stale egress or invented healthy fixture members")
	}
}

func TestDatabaseObservationNetworkMaintenanceFailures(t *testing.T) {
	for _, name := range []string{"maintenance busy", "lease expired", "foreign policy", "discovery unavailable"} {
		t.Run(name, func(t *testing.T) {
			f := newDatabaseNetworkObservationFixture(t)
			switch name {
			case "maintenance busy":
				claim, err := f.db.ClaimDatabaseMaintenance(context.Background(), f.d.ID, f.d.Revision)
				if err != nil || claim == nil {
					t.Fatal("could not hold fixture maintenance claim", err)
				}
				defer claim.Release()
			case "lease expired":
				f.expireLease = true
			case "foreign policy":
				f.policy.Labels["hakopod.io/database-id"] = "another-database"
			case "discovery unavailable":
				f.discoveryUnavailable = true
			}
			previous := f.policy.DeepCopy()
			f.server.refreshDatabaseObservation(context.Background())
			d := f.observed(t)
			f.mu.Lock()
			defer f.mu.Unlock()
			trace := []string{"namespace-read", "namespace-read"}
			if name == "maintenance busy" {
				trace = []string{"namespace-read"}
			}
			if f.writes != 0 || !reflect.DeepEqual(f.policy, previous) || !reflect.DeepEqual(f.trace, trace) {
				t.Fatal("failed maintenance mutated policy or skipped the independent health probe")
			}
			if name == "maintenance busy" {
				if d.Observation.Status != "pending" {
					t.Fatal("expected maintenance deferral replaced the independent health result")
				}
			} else if d.Observation.Status != "unknown" || d.Observation.Message != "Database network policy could not be refreshed. Reconciliation will retry." {
				t.Fatal("actual maintenance failure was hidden by the health probe", d.Observation.Status, d.Observation.Message)
			}
		})
	}
}
