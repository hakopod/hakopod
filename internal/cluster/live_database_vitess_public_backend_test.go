package cluster

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This checks native gateway identity and target routing. Internet DNS, source
// filtering, listener publication and session revocation need separate tests.
func TestManagedVitessPublicBackendLive(t *testing.T) {
	if os.Getenv("HAKOPOD_VITESS_PUBLIC_BACKEND_TEST") != "1" {
		t.Skip("set HAKOPOD_VITESS_PUBLIC_BACKEND_TEST=1 for native gateway acceptance")
	}
	for _, scenario := range []struct {
		name   string
		shards int
	}{{"standalone", 1}, {"cluster", 2}} {
		t.Run(scenario.name, func(t *testing.T) {
			fixtures, ctx := newVitessLiveClient(t, 30*time.Minute)
			c := fixtures.c
			d, password := newVitessFixture(t, ctx, fixtures, scenario.name, scenario.shards)
			d.Observation = waitVitessFixture(t, ctx, c, d, password)
			seedVitessFixture(t, ctx, vitessFixtureClient(t, ctx, c, d, d.Observation, password, "app@primary", 0))
			endpoint := database.PublicEndpoint{
				ID: d.ID, DatabaseID: d.ID, Revision: 1,
				Spec:       database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 16},
				Allocation: database.PublicEndpointAllocation{ID: "native-fixture-" + d.ID, Host: "gateway." + d.ID + ".example.test", Address: "203.0.113.10", Port: 15432},
			}
			for _, host := range []string{endpoint.Allocation.Host, "renewed." + d.ID + ".example.test"} {
				endpoint.Allocation.Host = host
				d.PublicEndpointNames = []string{host}
				step, cancel := context.WithTimeout(ctx, 8*time.Minute)
				var last error
				for step.Err() == nil {
					last = c.ReconcileDatabasePublicEndpointAccess(step, d, d.PublicEndpointNames, false, func() error { return step.Err() })
					if last == nil {
						d.Observation, last = c.ObserveDatabase(step, d)
						if last == nil && d.Observation.Status == "ready" {
							last = c.verifyVitessPublicEndpointBackend(step, d, endpoint)
							if last == nil {
								break
							}
						}
					}
					if sleepContext(step, 2*time.Second) != nil {
						break
					}
				}
				ended := step.Err()
				cancel()
				if last != nil || ended != nil || d.Observation.Status != "ready" {
					t.Fatal("native public gateway identity did not converge", last, ended)
				}
				if d.Observation.TLS == nil || !d.Observation.TLS.Verified || d.Observation.Coordination == nil || !d.Observation.Coordination.Ready || d.Observation.Routing == nil || !d.Observation.Routing.Ready || len(d.Observation.Routing.Members) != d.Spec.VitessGateways() {
					t.Fatal("public identity renewal left private transport or topology unverified")
				}
				route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
				if err != nil {
					t.Fatal(err)
				}
				trust, err := c.DatabaseTrust(ctx, d)
				if err != nil {
					t.Fatal(err)
				}
				config, err := redisTLSConfig(trust, host)
				if err != nil {
					t.Fatal(err)
				}
				for _, member := range d.Observation.Routing.Members {
					pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, member.Name, metav1.GetOptions{})
					if err != nil || string(pod.UID) != member.UID {
						t.Fatal("native gateway ownership changed", err)
					}
					client, err := c.vitessPublicEndpointClient(ctx, d, member, route, host, pod.Status.PodIP, password, config, true)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = client.Close() })
					checkVitessFixtureData(t, ctx, client)
					_ = client.Close()
				}
				d.Observation, err = c.ObserveDatabase(ctx, d)
				if err != nil || d.Observation.Status != "ready" {
					t.Fatal("native gateway observation did not refresh", err)
				}
				wrong := endpoint
				wrong.Allocation.Host = "unissued.example.test"
				if err = c.verifyVitessPublicEndpointBackend(ctx, d, wrong); err == nil {
					t.Fatal("public backend accepted an unissued hostname")
				}
				changed := d
				observation := d.Observation
				routing := *d.Observation.Routing
				routing.Members = append([]database.Member(nil), routing.Members...)
				routing.Members[0].UID = "foreign-gateway"
				observation.Routing = &routing
				changed.Observation = observation
				if err = c.verifyVitessPublicEndpointBackend(ctx, changed, endpoint); err == nil {
					t.Fatal("public backend accepted a changed gateway UID")
				}
			}
			d.PublicEndpointNames = nil
			step, cancel := context.WithTimeout(ctx, 8*time.Minute)
			defer cancel()
			var last error
			for step.Err() == nil {
				last = c.ReconcileDatabasePublicEndpointAccess(step, d, nil, false, func() error { return step.Err() })
				if last == nil {
					d.Observation, last = c.ObserveDatabase(step, d)
					if last == nil && d.Observation.Status == "ready" {
						break
					}
				}
				if sleepContext(step, 2*time.Second) != nil {
					break
				}
			}
			if last != nil || step.Err() != nil || d.Observation.Status != "ready" {
				t.Fatal("public hostname withdrawal did not restore private readiness", last)
			}
			if err := c.verifyVitessPublicEndpointBackend(ctx, d, endpoint); err == nil {
				t.Fatal("withdrawn public certificate hostname still verified")
			}
			checkVitessFixtureData(t, ctx, vitessFixtureClient(t, ctx, c, d, d.Observation, password, "app@primary", 0))
			if err := database.PublicEndpointAvailability(d.Spec); err == nil {
				t.Fatal("partial native backend acceptance opened the public gate")
			}
		})
	}
}
