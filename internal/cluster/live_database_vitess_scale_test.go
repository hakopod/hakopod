//go:build hakopod_native_acceptance

package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/client-go/rest"
)

type vitessScaleRequestCounts struct {
	core    atomic.Int64
	grouped atomic.Int64
	exec    atomic.Int64
}

type vitessScaleCountingTransport struct {
	next   http.RoundTripper
	counts *vitessScaleRequestCounts
}

func (t vitessScaleCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	path := request.URL.Path
	if strings.HasSuffix(path, "/exec") {
		t.counts.exec.Add(1)
	} else if strings.HasPrefix(path, "/api/") {
		t.counts.core.Add(1)
	} else if strings.HasPrefix(path, "/apis/") {
		t.counts.grouped.Add(1)
	}
	return t.next.RoundTrip(request)
}

func newVitessScaleObservedClient(t *testing.T, source *Client, counts *vitessScaleRequestCounts) *Client {
	t.Helper()
	config := rest.CopyConfig(source.execConfig)
	prior := config.WrapTransport
	config.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		if prior != nil {
			next = prior(next)
		}
		return vitessScaleCountingTransport{next: next, counts: counts}
	}
	client, err := NewWithConfig(config, source.options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestManagedVitessScaleLive(t *testing.T) {
	fixtures, ctx := newVitessLiveClient(t, 30*time.Minute)
	// The scale case runs separately and reuses the protected cluster fixture;
	// it does not require another storage principal or credential set.
	d, password := newVitessFixture(t, ctx, fixtures, "cluster", 8, 5)

	var health database.Observation
	for ctx.Err() == nil {
		apply, cancelApply := context.WithTimeout(ctx, 30*time.Second)
		err := fixtures.c.ApplyDatabase(apply, d, password, func() error { return apply.Err() })
		cancelApply()
		if err == nil {
			observe, cancelObserve := context.WithTimeout(ctx, 25*time.Second)
			health, err = fixtures.c.ObserveDatabase(observe, d)
			cancelObserve()
		}
		if err == nil && health.Status == "ready" {
			break
		}
		t.Log("Waiting for Vitess scale fixture", health.Message, err)
		if sleepContext(ctx, 5*time.Second) != nil {
			break
		}
	}
	if health.Status != "ready" {
		t.Fatal("Vitess scale fixture did not become ready")
	}
	seedVitessFixture(t, ctx, vitessFixtureClient(t, ctx, fixtures.c, d, health, password, "app@primary", 0))
	checkVitessShardRouting(t, ctx, fixtures.c, d, health)
	baselineFingerprint := health.TopologyFingerprint
	baselineUIDs := make(map[string]string, len(health.Members))
	for _, member := range health.Members {
		baselineUIDs[member.Name] = member.UID
	}

	counts := &vitessScaleRequestCounts{}
	observed := newVitessScaleObservedClient(t, fixtures.c, counts)
	for attempt := 1; attempt <= 3; attempt++ {
		beforeCore, beforeGrouped, beforeExec := counts.core.Load(), counts.grouped.Load(), counts.exec.Load()
		started := time.Now()
		step, cancel := context.WithTimeout(ctx, 25*time.Second)
		current, err := observed.ObserveDatabase(step, d)
		cancel()
		elapsed := time.Since(started)
		coreRequests, groupedRequests, execRequests := counts.core.Load()-beforeCore, counts.grouped.Load()-beforeGrouped, counts.exec.Load()-beforeExec
		evidence, _ := json.Marshal(map[string]any{"case": "scale-observation", "attempt": attempt, "elapsed_milliseconds": elapsed.Milliseconds(), "core_requests": coreRequests, "grouped_requests": groupedRequests, "exec_requests": execRequests})
		t.Log(string(evidence))
		primaries, primaryErr := vitessObservedPrimaries(d, current)
		replicas := 0
		stable := current.TopologyFingerprint == baselineFingerprint && len(baselineUIDs) == len(current.Members)
		for _, member := range current.Members {
			if member.Role == "replica" {
				replicas++
			}
			stable = stable && baselineUIDs[member.Name] == member.UID
		}
		if err != nil || elapsed > 25*time.Second || current.Status != "ready" || len(current.Members) != 48 || primaryErr != nil || len(primaries) != 8 || replicas != 40 || !stable || current.Routing == nil || !current.Routing.Ready || len(current.Routing.Members) != 2 || current.Coordination == nil || !current.Coordination.Ready || len(current.Coordination.Members) != 3 || current.TLS == nil || !current.TLS.Verified || !current.TLS.PlaintextRejected {
			t.Fatal("Vitess scale observation did not satisfy its bounded health contract", err, primaryErr)
		}
		if coreRequests <= 0 || groupedRequests <= 0 || execRequests <= 0 || coreRequests > 176 || groupedRequests > 150 {
			t.Fatalf("Vitess scale observation exceeded API request bounds: core=%d grouped=%d exec=%d", coreRequests, groupedRequests, execRequests)
		}
		health = current
	}

	for gateway := 0; gateway < 2; gateway++ {
		checkVitessFixtureData(t, ctx, vitessFixtureClient(t, ctx, observed, d, health, password, "app@primary", gateway))
		checkVitessFixtureData(t, ctx, vitessFixtureClient(t, ctx, observed, d, health, password, "app@replica", gateway))
	}
}
