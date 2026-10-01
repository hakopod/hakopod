package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestOracleSwitchoverPreflightPreservesHealthyEndpoint(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"stale review", "expired review", "observation interrupted", "prior uncertain request"} {
		t.Run(scenario, func(t *testing.T) {
			d := oracleEnterpriseFixture()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: types.UID("namespace"), Labels: databaseLabels(d)}}
			root := oracleEnterpriseObject(d, 0, "", nil, nil)
			root.SetUID("root")
			root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
			broker := oracleEnterpriseBroker(d, "", nil, nil)
			broker.SetUID("broker")
			broker.SetResourceVersion("1")
			broker.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: "database", UID: root.GetUID(), Controller: ptr(true)}})
			c := &Client{kube: fake.NewClientset(ns), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root, broker)}
			if err := c.reconcileOracleEnterpriseRoute(ctx, d, nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			service, err := c.kube.CoreV1().Services(ns.Name).Get(ctx, "database-rw", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			service.Spec.Selector = map[string]string{databaseOwner: d.ID, "hakopod.io/oracle-pod": "healthy-primary"}
			if _, err = c.kube.CoreV1().Services(ns.Name).Update(ctx, service, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			plan := database.OracleSwitchoverReview{RequestID: strings.Repeat("a", 32), Primary: "primary", TopologyFingerprint: "before", MemberUIDs: []string{"one", "two", "three"}, ExpiresAt: time.Now().Add(time.Minute)}
			health := database.Observation{Primary: "primary", TopologyFingerprint: "before", Members: []database.Member{{UID: "one"}, {UID: "two"}, {UID: "three"}}}
			calls := 0
			if scenario == "expired review" {
				plan.ExpiresAt = time.Now().Add(-time.Minute)
			}
			if scenario == "prior uncertain request" {
				if err := c.guardOracleRoleRoute(ctx, d, plan.RequestID, false, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			observe := func() (database.Observation, error) {
				calls++
				if scenario == "observation interrupted" {
					if calls > 1 {
						return database.Observation{}, errors.New("observation unavailable")
					}
					return health, nil
				}
				changed := health
				changed.TopologyFingerprint = "after"
				return changed, nil
			}
			err = c.prepareOracleSwitchoverRoute(ctx, d, plan, broker, func() error { return nil }, observe)
			want := database.ErrOracleSwitchoverReviewChanged
			if scenario == "prior uncertain request" {
				want = database.ErrOracleSwitchoverFailed
			}
			if !errors.Is(err, want) {
				t.Fatal("unexpected preflight result", err)
			}
			after, err := c.kube.CoreV1().Services(ns.Name).Get(ctx, "database-rw", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "stale review", "expired review":
				if after.Spec.Selector["hakopod.io/oracle-pod"] != "healthy-primary" || after.Annotations[oracleRoleRequest] != "" {
					t.Fatal("a pre-start stale review changed the healthy endpoint")
				}
			case "observation interrupted":
				if after.Annotations[oracleRoleRequest] != "" || after.Spec.Selector["hakopod.io/oracle-pod"] != "unverified" {
					t.Fatal("a known unsubmitted request did not release its guard for native-health reconciliation")
				}
			case "prior uncertain request":
				if after.Annotations[oracleRoleRequest] != plan.RequestID || after.Spec.Selector["hakopod.io/oracle-pod"] != "unverified" {
					t.Fatal("an uncertain earlier request was allowed to reopen routing")
				}
			}
			for _, action := range c.dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
				if action.GetVerb() == "update" {
					t.Fatal("preflight submitted a broker mutation")
				}
			}
		})
	}
}
