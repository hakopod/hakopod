package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type actionsPoolReadinessFixture struct {
	*actionsFake
	check func(cluster.Target, spec.Service) error
}

func (*actionsPoolReadinessFixture) ActionsAvailable(context.Context) error {
	return errors.New("global node inventory is unavailable")
}

func (f *actionsPoolReadinessFixture) ActionsPoolAvailable(_ context.Context, target cluster.Target, config spec.Service) error {
	return f.check(target, config)
}

func (f *actionsPoolReadinessFixture) ActionsScopeAvailable(_ context.Context, target cluster.Target) error {
	return f.check(target, spec.Service{})
}

func TestActionsRegistrationUsesPoolReadinessBeforeCreatingIntent(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "available"}[ready], func(t *testing.T) {
			server, fake, pool, _ := actionsHarness(t)
			pool.Config.NodeName = "selected-node"
			pool.Config.Architecture = "arm64"
			checks := 0
			server.actionsTestRuntime = &actionsPoolReadinessFixture{actionsFake: fake, check: func(target cluster.Target, config spec.Service) error {
				checks++
				if target.ApplicationID != pool.ApplicationID || target.Project != pool.Project || target.Environment != pool.Environment || target.Spec.Name != pool.ApplicationName || config.NodeName != pool.Config.NodeName || config.Architecture != pool.Config.Architecture {
					t.Error("pool readiness lost the selected scope or placement")
				}
				if !ready {
					return errors.New("selected node is unavailable")
				}
				return nil
			}}
			ctx := context.Background()
			err := server.reconcileActionsPool(ctx, actionsTarget(pool), pool)
			if (err == nil) != ready || checks != 1 {
				t.Fatal("registration did not use exactly one pool readiness check", ready, checks, err)
			}
			slots, err := server.Store.ActionsSlots(ctx, pool.ApplicationID, pool.Service)
			if err != nil {
				t.Fatal(err)
			}
			if !ready {
				if len(slots) != 0 || fake.next != 0 || len(fake.pods) != 0 || len(fake.configs) != 0 {
					t.Fatal("unavailable placement created an intent, registration or runtime resource")
				}
				return
			}
			if len(slots) != 1 || fake.next != 1 || len(fake.pods) != 1 {
				t.Fatal("global inventory failure blocked an eligible selected node")
			}
			if err = server.reconcileActionsPool(ctx, actionsTarget(pool), pool); err != nil || checks != 2 {
				t.Fatal("settled reconciliation returned to global readiness", checks, err)
			}
		})
	}
}

func TestActionsCapabilitiesUseAuthorizedEnvironmentReadiness(t *testing.T) {
	server, fake, _, _ := actionsHarness(t)
	server.Auth.DeploymentMode = cluster.DeploymentManagedCloud
	ctx := context.Background()
	ownerKey, err := server.Store.Bootstrap(ctx, "capabilities-fixture")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := server.Store.Authenticate(ctx, ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	_, readerKey, err := server.Store.CreateKey(ctx, owner, store.KeyInput{Name: "capabilities-reader", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ready, checks := true, 0
	server.actionsTestRuntime = &actionsPoolReadinessFixture{actionsFake: fake, check: func(target cluster.Target, config spec.Service) error {
		checks++
		if target.Project != "demo" || target.Environment != "development" || config.NodeName != "" || config.Architecture != "" {
			t.Error("capability check changed its authorized environment or invented placement")
		}
		if !ready {
			return errors.New("this environment has no eligible allocation")
		}
		return nil
	}}
	handler := server.Handler()
	request := func(environment string, status int, wantReady bool) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/actions/capabilities?project=demo&environment="+environment, nil)
		req.Header.Set("Authorization", "Bearer "+readerKey)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("capabilities returned %d, want %d", res.Code, status)
		}
		if status == 200 {
			var body struct {
				Licensed bool `json:"licensed"`
				Ready    bool `json:"runtime_ready"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || !body.Licensed || body.Ready != wantReady {
				t.Fatal("capabilities did not report scoped runtime readiness", body, err)
			}
		}
	}
	request("development", 200, true)
	ready = false
	request("development", 200, false)
	request("production", 403, false)
	if checks != 2 {
		t.Fatal("unauthorized scope reached runtime discovery", checks)
	}
}
