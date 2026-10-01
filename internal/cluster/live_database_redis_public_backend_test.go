package cluster

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

// This checks native member Services and certificate loading. It does not
// qualify public DNS, ingress ACLs or a client outside the development cluster.
func TestManagedRedisPublicBackendLive(t *testing.T) {
	if os.Getenv("HAKOPOD_REDIS_PUBLIC_BACKEND_TEST") != "1" || os.Getenv("HAKOPOD_DATABASE_TLS_TEST") != "1" {
		t.Skip("set HAKOPOD_REDIS_PUBLIC_BACKEND_TEST=1 and HAKOPOD_DATABASE_TLS_TEST=1")
	}
	for _, clustered := range []bool{false, true} {
		name := "standalone"
		if clustered {
			name = "cluster"
		}
		t.Run(name, func(t *testing.T) {
			c, ctx := liveRecoveryClient(t, 20*time.Minute)
			d, health := newRecoveryFixture(t, ctx, c, "redis", "8", clustered)
			d.Observation = health
			endpoint := database.PublicEndpoint{ID: d.ID, DatabaseID: d.ID, Revision: 1, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 16}}
			allocation := func(i int) database.PublicEndpointAllocation {
				return database.PublicEndpointAllocation{ID: fmt.Sprintf("native-fixture-%s-%d", d.ID, i), Host: fmt.Sprintf("member-%d.%s.example.test", i, d.ID), Address: "203.0.113.10", Port: int32(15432 + i)}
			}
			endpoint.Allocation = allocation(0)
			if clustered {
				endpoint.Spec.Purpose = "cluster"
				members := append([]database.Member(nil), health.Members...)
				slices.SortFunc(members, func(a, b database.Member) int { return strings.Compare(a.Name, b.Name) })
				for i, member := range members {
					endpoint.MemberAllocations = append(endpoint.MemberAllocations, database.PublicEndpointMemberAllocation{MemberName: member.Name, MemberUID: member.UID, Allocation: allocation(i)})
				}
				d.PublicEndpointMembers = endpoint.MemberAllocations
			}
			var err error
			d.PublicEndpointNames, err = database.PublicEndpointAllocationNames(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			for deadline.Err() == nil {
				err = c.RenewDatabaseIdentity(deadline, d, func() error { return deadline.Err() })
				if err != nil {
					if sleepContext(deadline, 2*time.Second) != nil {
						break
					}
					continue
				}
				d.Observation, err = c.ObserveDatabase(deadline, d)
				if err != nil || d.Observation.Status != "ready" {
					if sleepContext(deadline, 2*time.Second) != nil {
						break
					}
					continue
				}
				err = c.reconcileRedisPublicNames(deadline, d, func() error { return deadline.Err() })
				if err == nil {
					err = c.verifyRedisPublicEndpointBackend(deadline, d, endpoint)
					if err == nil {
						break
					}
				}
				if sleepContext(deadline, 2*time.Second) != nil {
					break
				}
			}
			if err != nil || deadline.Err() != nil {
				t.Fatal("native public-name certificate and member backend did not converge", err)
			}
			if clustered {
				// Native certificate and plaintext checks can outlive an observation.
				// Refresh real member state before checking the client address map.
				observe, stop := context.WithTimeout(ctx, 25*time.Second)
				d.Observation, err = c.ObserveDatabase(observe, d)
				stop()
				if err != nil || d.Observation.Status != "ready" {
					t.Fatal("native member observation did not refresh after backend checks", err)
				}
				mapping, err := c.observeRedisPublicAddressMap(ctx, d, endpoint)
				if err != nil || len(mapping) != d.Spec.Members() {
					t.Fatal("native address map differs from owned members", err)
				}
				if err = c.reconcileRedisPublicMemberServices(ctx, d, endpoint, false, func() error { return ctx.Err() }); err != nil {
					t.Fatal(err)
				}
				if err = c.verifyRedisPublicEndpointBackend(ctx, d, endpoint); err == nil {
					t.Fatal("removed member Services still verified")
				}
			}
			if err = database.PublicEndpointAvailability(d.Spec); err == nil {
				t.Fatal("partial native backend acceptance opened the public gate")
			}
			t.Log("Verified native public-name TLS loading, member identity, authentication and plaintext refusal; outside-in public acceptance remains separate")
		})
	}
}
