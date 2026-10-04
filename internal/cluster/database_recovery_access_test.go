package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func recoveryApplicationIngress(t *testing.T, c *Client, d database.Resource) bool {
	t.Helper()
	policy, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+d.ID] == "true" {
				return true
			}
		}
	}
	return false
}

func TestDatabaseRecoveryNetworkRemainsClosedUntilInspection(t *testing.T) {
	stamp := time.Now().UTC()
	for _, engine := range []string{"postgresql", "redis", "mysql", "mongodb", "clickhouse", "oracle"} {
		t.Run(engine, func(t *testing.T) {
			c := &Client{kube: fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.43.0.1"}}})}
			d := mongodbUnitFixture()
			d.Spec.Engine = engine
			if engine == "oracle" {
				d = oracleFixture()
			}
			if _, err := c.kube.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), Labels: databaseLabels(d)}}, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name, status string
				recovery     *database.Recovery
				open         bool
			}{
				{"ordinary", "ready", nil, true},
				{"active restore", "restoring", nil, false},
				{"pending", "ready", &database.Recovery{JobID: "job"}, false},
				{"restored", "ready", &database.Recovery{JobID: "job", RestoredAt: &stamp}, false},
				{"inspection alone", "ready", &database.Recovery{JobID: "job", InspectedAt: &stamp}, false},
				{"unfinished lifecycle", "restoring", &database.Recovery{JobID: "job", RestoredAt: &stamp, InspectedAt: &stamp}, false},
				{"complete", "ready", &database.Recovery{JobID: "job", RestoredAt: &stamp, InspectedAt: &stamp}, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					d.Status, d.Recovery = tc.status, tc.recovery
					// Exercise ordinary reconciliation as well as the maintenance hook:
					// neither path may bypass the durable inspection gate.
					if err := c.databaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
						t.Fatal(err)
					}
					if err := c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
						t.Fatal(err)
					}
					if recoveryApplicationIngress(t, c, d) != tc.open {
						t.Fatal("application ingress disagrees with durable recovery and inspection state")
					}
				})
			}
			d.Status, d.Recovery = "restoring", &database.Recovery{JobID: "job"}
			if err := c.databaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			d.Status, d.Recovery.RestoredAt, d.Recovery.InspectedAt = "ready", &stamp, &stamp
			lostLease := errors.New("maintenance lease ended")
			if err := c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { return lostLease }); !errors.Is(err, lostLease) {
				t.Fatal("recovery access ignored its maintenance lease")
			}
			if recoveryApplicationIngress(t, c, d) {
				t.Fatal("expired maintenance lease reopened recovery access")
			}
			// A target restored by an older version may already have an open
			// policy. Maintenance must close it without waiting for inspection.
			d.Recovery = nil
			if err := c.databaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			d.Recovery = &database.Recovery{JobID: "older-job", RestoredAt: &stamp}
			if err := c.ReconcileDatabaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if recoveryApplicationIngress(t, c, d) {
				t.Fatal("maintenance left an uninspected recovery target accessible")
			}
		})
	}
}
