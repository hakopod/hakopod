package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestVitessReseedFixtureBindsNumericNativeTabletTypes(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("a", 32), Spec: database.Spec{Replicas: 2}}
	member := database.Member{Name: "tablet-replica", Shard: "-", Role: "replica"}
	hostname := member.Name + "." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	// The native GetTablets response uses JSON numbers for protobuf enums.
	value := fmt.Sprintf(`[
		{"alias":{"cell":"local","uid":11},"hostname":"tablet-primary","keyspace":"app","shard":"-","type":1},
		{"alias":{"cell":"local","uid":22},"hostname":%q,"keyspace":"app","shard":"-","type":2},
		{"alias":{"cell":"local","uid":33},"hostname":"another-replica","keyspace":"app","shard":"-","type":2}
	]`, hostname)
	alias, err := vitessFixtureReplicaAliasFromJSON(d, member, value)
	if err != nil || alias != "local-0000000022" {
		t.Fatalf("native replica binding = %q: %v", alias, err)
	}
	for name, invalid := range map[string]string{
		"string enum":  strings.ReplaceAll(value, `"type":2`, `"type":"REPLICA"`),
		"primary":      strings.ReplaceAll(value, `"type":2`, `"type":1`),
		"unknown enum": strings.ReplaceAll(value, `"type":1`, `"type":7`),
		"wrong shard":  strings.ReplaceAll(value, `"shard":"-"`, `"shard":"-80"`),
		"wrong host":   strings.ReplaceAll(value, hostname, "unobserved-tablet"),
		"duplicate":    strings.ReplaceAll(value, "another-replica", hostname),
		"incomplete":   `[]`,
		"invalid":      `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if alias, err := vitessFixtureReplicaAliasFromJSON(d, member, invalid); err == nil || alias != "" {
				t.Fatalf("unsafe native replica binding = %q: %v", alias, err)
			}
		})
	}
}

func TestVitessReseedFixtureRequiresOwnedCompleteBackups(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("b", 32)}
	ns := DatabaseNamespace(d.ID)
	stamp := time.Now().UTC().Truncate(time.Second)
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessCluster"}}
	root.SetName("database")
	root.SetNamespace(ns)
	root.SetUID("root-uid")
	root.SetLabels(map[string]string{databaseOwner: d.ID, managedBy: "hakopod"})
	storage := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessBackupStorage"}}
	storage.SetName("storage")
	storage.SetNamespace(ns)
	storage.SetUID("storage-uid")
	storage.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessCluster", Name: root.GetName(), UID: root.GetUID()}})
	copy := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "planetscale.com/v2", "kind": "VitessBackup", "status": map[string]any{"complete": true, "startTime": stamp.Format(time.RFC3339)}}}
	copy.SetName("copy")
	copy.SetNamespace(ns)
	copy.SetUID("copy-uid")
	copy.SetLabels(map[string]string{"planetscale.com/cluster": "database", "planetscale.com/keyspace": "app", "planetscale.com/shard": "x-x"})
	copy.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessBackupStorage", Name: storage.GetName(), UID: storage.GetUID()}})
	for _, name := range []string{"complete", "unfinished", "deleting", "foreign owner", "wrong shard", "invalid completion", "future timestamp", "invalid timestamp", "overflow"} {
		t.Run(name, func(t *testing.T) {
			item := copy.DeepCopy()
			wantError, wantCount := false, 1
			switch name {
			case "unfinished":
				_ = unstructured.SetNestedField(item.Object, false, "status", "complete")
				wantCount = 0
			case "deleting":
				at := metav1.NewTime(stamp)
				item.SetDeletionTimestamp(&at)
				wantCount = 0
			case "foreign owner":
				item.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "planetscale.com/v2", Kind: "VitessBackupStorage", Name: storage.GetName(), UID: "foreign"}})
				wantError = true
			case "wrong shard":
				labels := item.GetLabels()
				labels["planetscale.com/shard"] = "x-80"
				item.SetLabels(labels)
				wantError = true
			case "invalid completion":
				_ = unstructured.SetNestedField(item.Object, "true", "status", "complete")
				wantError = true
			case "future timestamp":
				_ = unstructured.SetNestedField(item.Object, stamp.Add(time.Hour).Format(time.RFC3339), "status", "startTime")
				wantError = true
			case "invalid timestamp":
				_ = unstructured.SetNestedField(item.Object, "unknown", "status", "startTime")
				wantError = true
			case "overflow":
				wantError = true
			}
			objects := []runtime.Object{root.DeepCopy(), storage.DeepCopy(), item}
			if name == "overflow" {
				for i := 0; i < 64; i++ {
					extra := copy.DeepCopy()
					extra.SetName(fmt.Sprintf("extra-%d", i))
					extra.SetUID(types.UID(extra.GetName()))
					objects = append(objects, extra)
				}
			}
			gvr := schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessbackups"}
			c := &Client{dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "VitessBackupList"}, objects...)}
			backups, err := vitessFixtureCompleteBackups(context.Background(), c, d)
			if (err != nil) != wantError || !wantError && len(backups) != wantCount {
				t.Fatalf("complete backup inventory = %d, err = %v", len(backups), err)
			}
		})
	}
}

func TestVitessReseedFixtureRejectsRediscoveredOldCopies(t *testing.T) {
	started := time.Now().UTC()
	before := map[types.UID]time.Time{"existing": started.Add(-time.Minute)}
	for _, test := range []struct {
		name  string
		after map[types.UID]time.Time
		want  bool
	}{
		{"new complete copy", map[types.UID]time.Time{"new": started.Truncate(time.Second)}, true},
		{"known identity", map[types.UID]time.Time{"existing": started}, false},
		{"rediscovered old copy", map[types.UID]time.Time{"new": started.Add(-time.Minute)}, false},
		{"none complete", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := vitessFixtureHasNewBackup(before, test.after, started); got != test.want {
				t.Fatalf("new completed backup = %t, want %t", got, test.want)
			}
		})
	}
}
