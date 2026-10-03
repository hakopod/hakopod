//go:build hakopod_native_acceptance && linux

package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
)

type nativeProbeBindingFixture struct {
	binding    store.NeonRecoveryBinding
	err        error
	platformID string
	revision   int64
	reads      int
}

func (s *nativeProbeBindingFixture) NeonRecoveryBindingForTarget(_ context.Context, platformID string, revision int64) (store.NeonRecoveryBinding, error) {
	s.platformID, s.revision = platformID, revision
	s.reads++
	return s.binding, s.err
}

func (*nativeProbeBindingFixture) NeonRecoveryBindingForLifecycle(context.Context, store.ManagedPlatformOperation) (store.NeonRecoveryBinding, error) {
	panic("native probe requested a live mutation lease")
}

func TestPrepareNativeNeonProbeReadsCompletedRevisionBindings(t *testing.T) {
	platformID := strings.Repeat("4", 32)
	restored := store.NeonRecoveryBinding{TargetPlatformID: platformID, TargetRevision: 1, TenantID: strings.Repeat("6", 32), TimelineID: strings.Repeat("7", 32), TenantGeneration: 3, TimelineGeneration: 9}
	ca := supabaseGatewayCertificateFixture(t, []string{"neon.test"}, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))["ca.crt"]
	for _, test := range []struct {
		name, kind string
		revision   int64
		binding    store.NeonRecoveryBinding
		err        error
	}{
		{name: "created", kind: "create", revision: 1, err: pgx.ErrNoRows},
		{name: "restored", kind: "create", revision: 1, binding: restored},
		{name: "updated after restore", kind: "update", revision: 2, binding: restored},
	} {
		t.Run(test.name, func(t *testing.T) {
			op := store.ManagedPlatformOperation{ID: strings.Repeat("5", 32), PlatformID: platformID, Revision: test.revision, Kind: test.kind, Status: "succeeded"}
			spec := managedplatform.Spec{Kind: "neon", Neon: &managedplatform.NeonConfig{ComputeReplicas: 2, Pageservers: 2, Safekeepers: 3}, Secrets: map[string]managedplatform.SecretReference{
				"controller-auth": {Name: "controller-auth", Revision: 1},
				"compute-auth":    {Name: "compute-auth", Revision: 1},
				"safekeeper-auth": {Name: "safekeeper-auth", Revision: 1},
				"pageserver-auth": {Name: "pageserver-auth", Revision: 1},
			}}
			op.Spec = spec
			request := NeonRuntimeRequest{
				Operation: op,
				Render:    managedplatform.NeonRenderInput{Spec: spec, PlatformID: platformID, Revision: op.Revision},
				SecretSnapshots: map[string]map[string][]byte{
					"controller-auth-r1": {"token": []byte("controller-secret-token"), "ca.crt": ca},
					"compute-auth-r1":    {"token": []byte("compute-secret-token"), "ca.crt": ca, "config.json": []byte(`{"spec":{"cluster":{"settings":[]}},"compute_ctl_config":{}}`)},
					"safekeeper-auth-r1": {"token": []byte("safekeeper-secret-token"), "ca.crt": ca},
					"pageserver-auth-r1": {"token": []byte("pageserver-secret-token"), "ca.crt": ca},
				},
				ProxyEndpoint: managedplatform.NeonProxyBootstrapState{EndpointID: platformID, Enabled: true, Roles: map[string]managedplatform.NeonProxyRoleState{"cloud_admin": {}}},
			}
			reader := &nativeProbeBindingFixture{binding: test.binding, err: test.err}
			runtime, observed, route, err := prepareNeonNativeProbeRuntime(context.Background(), request, reader, bytes.Repeat([]byte{1}, 32), []string{"zone-a", "zone-b", "zone-c"})
			if err != nil || runtime == nil {
				t.Fatalf("completed revision required lifecycle authority: %v", err)
			}
			if reader.reads != 1 || reader.platformID != platformID || reader.revision != op.Revision {
				t.Fatal("native probe did not read the exact accepted revision")
			}
			tenantID, timelineID := test.binding.TenantID, test.binding.TimelineID
			if errors.Is(test.err, pgx.ErrNoRows) {
				tenantID, timelineID = neonDeterministicID(platformID, "tenant"), neonDeterministicID(platformID, "timeline")
			}
			if observed.TenantID != tenantID || observed.TimelineID != timelineID || route.BranchID != timelineID {
				t.Fatal("native probe lost the accepted restored identity")
			}
			for compute, mode := range map[string]string{"compute-0": "Primary", "compute-1": "Replica"} {
				var config struct {
					Spec struct {
						TenantID   string `json:"tenant_id"`
						TimelineID string `json:"timeline_id"`
						Mode       string `json:"mode"`
					} `json:"spec"`
				}
				if err = json.Unmarshal(observed.ComputeConfig[compute], &config); err != nil || config.Spec.TenantID != tenantID || config.Spec.TimelineID != timelineID || config.Spec.Mode != mode {
					t.Fatalf("native probe reconstructed the wrong compute binding: %v", err)
				}
			}
		})
	}
}

func TestNativeNeonProbeBindingsRejectChangedSnapshotBeforeRead(t *testing.T) {
	op := store.ManagedPlatformOperation{ID: "accepted-operation", PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "update", Status: "succeeded", Spec: managedplatform.Spec{Name: "accepted"}}
	for name, change := range map[string]func(*store.ManagedPlatformOperation){
		"operation": func(value *store.ManagedPlatformOperation) { value.ID = "another" },
		"platform":  func(value *store.ManagedPlatformOperation) { value.PlatformID = strings.Repeat("5", 32) },
		"revision":  func(value *store.ManagedPlatformOperation) { value.Revision++ },
		"running":   func(value *store.ManagedPlatformOperation) { value.Status = "running" },
		"deletion":  func(value *store.ManagedPlatformOperation) { value.Kind = "delete" },
		"spec":      func(value *store.ManagedPlatformOperation) { value.Spec.Name = "another" },
	} {
		t.Run(name, func(t *testing.T) {
			reader := &nativeProbeBindingFixture{}
			bindings := neonNativeProbeBindings{reader: reader, operation: op}
			changed := op
			change(&changed)
			if _, err := bindings.NeonRecoveryBindingForLifecycle(context.Background(), changed); !errors.Is(err, store.ErrConflict) || reader.reads != 0 {
				t.Fatalf("changed snapshot reached the binding reader: %v", err)
			}
		})
	}
	reader := &nativeProbeBindingFixture{err: store.ErrConflict}
	bindings := neonNativeProbeBindings{reader: reader, operation: op}
	if _, err := bindings.NeonRecoveryBindingForLifecycle(context.Background(), op); !errors.Is(err, store.ErrConflict) {
		t.Fatal("binding conflict was hidden")
	}
	reader.err = nil
	reader.binding = store.NeonRecoveryBinding{TargetPlatformID: op.PlatformID, TargetRevision: op.Revision + 1}
	if _, err := bindings.NeonRecoveryBindingForLifecycle(context.Background(), op); !errors.Is(err, store.ErrConflict) {
		t.Fatal("future recovery binding was accepted")
	}
}
