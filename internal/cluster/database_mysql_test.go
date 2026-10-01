package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func mysqlUnitFixture() database.Resource {
	return database.Resource{ID: strings.Repeat("a", 32), Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "mysql", Engine: "mysql", Version: "8.4", Mode: "cluster", Shards: 1, Replicas: 2, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}}}
}

func TestMySQLDeleteFinalizesControllerBeforeNamespace(t *testing.T) {
	d := mysqlUnitFixture()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	object.SetUID("owned-controller")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "owned-namespace", Labels: databaseLabels(d)}}
	c := &Client{kube: kubefake.NewClientset(ns), dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme(), object)}
	ctx := context.Background()
	done, err := c.DeleteDatabase(ctx, d, func() error { return nil })
	if err != nil || done {
		t.Fatal("controller deletion must remain pending", err)
	}
	if _, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("namespace was deleted before controller finalization")
	}
	if _, err := c.dynamic.Resource(mysqlDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("owned MySQL controller was not deleted")
	}
	if _, err = c.DeleteDatabase(ctx, d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("namespace retained after controller finalization")
	}
}

func TestMySQLObjectPinsAllContainersAndSeparatesCredentials(t *testing.T) {
	d := mysqlUnitFixture()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	name, _, _ := unstructured.NestedString(object.Object, "spec", "secretName")
	if name != "database-admin" {
		t.Fatal("application credentials used as MySQL administrator")
	}
	for _, path := range [][]string{{"spec", "podSpec", "containers"}, {"spec", "podSpec", "initContainers"}, {"spec", "router", "podSpec", "containers"}} {
		containers, found, err := unstructured.NestedSlice(object.Object, path...)
		if !found || err != nil {
			t.Fatal("missing container policy")
		}
		for _, raw := range containers {
			container := raw.(map[string]any)
			if !strings.Contains(container["image"].(string), "@sha256:") || container["resources"] == nil {
				t.Fatal("unbounded or unpinned MySQL container")
			}
		}
	}
	data, _ := json.Marshal(object.Object)
	if strings.Contains(string(data), "rootPassword\":\"") || strings.Contains(string(data), "password\":") {
		t.Fatal("credential material serialized into controller spec")
	}
	for _, host := range databaseIdentityNames(d) {
		if strings.Contains(host, "redis") {
			t.Fatal("wrong engine identity")
		}
	}
	options, found, err := unstructured.NestedSlice(object.Object, "spec", "router", "bootstrapOptions")
	if err != nil || !found {
		t.Fatal("Router bootstrap policy is unavailable")
	}
	logger := ""
	for _, option := range options {
		value, ok := option.(string)
		if ok && strings.HasPrefix(value, "--conf-set-option=logger.level=") {
			if logger != "" {
				t.Fatal("Router logger policy is ambiguous")
			}
			logger = strings.TrimPrefix(value, "--conf-set-option=logger.level=")
		}
	}
	if logger != "WARNING" {
		t.Fatal("Router hides certificate failures or enables verbose metadata logging")
	}
}

func TestMySQLHealthRejectsUnknownMembersAndUnsafeTransport(t *testing.T) {
	d := mysqlUnitFixture()
	members := []database.Member{{Name: "database-0"}, {Name: "database-1"}, {Name: "database-2"}}
	view := map[string]any{"uuid": "id-0", "read_only": 0, "secure": 1, "tls": "TLSv1.2,TLSv1.3", "group_tls": "VERIFY_IDENTITY", "recovery_tls": 1}
	rows := []map[string]any{}
	for i, m := range members {
		role := "SECONDARY"
		if i == 0 {
			role = "PRIMARY"
		}
		rows = append(rows, map[string]any{"id": "id-" + string(rune('0'+i)), "host": m.Name + ".database-instances." + DatabaseNamespace(d.ID) + ".svc.cluster.local", "role": role, "state": "ONLINE"})
	}
	view["members"] = rows
	raw, _ := json.Marshal(view)
	if primary, role, err := verifyMySQLGroupView(raw, d, members[0], members); err != nil || primary != "database-0" || role != "primary" {
		t.Fatal("valid MySQL view rejected", err)
	}
	for _, change := range []func(map[string]any){func(v map[string]any) { v["group_tls"] = "REQUIRED" }, func(v map[string]any) { v["secure"] = 0 }, func(v map[string]any) { v["read_only"] = 1 }, func(v map[string]any) { v["uuid"] = "outside" }, func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["state"] = "RECOVERING" }, func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["role"] = "PRIMARY" }, func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["host"] = "outside.example" }} {
		var changed map[string]any
		_ = json.Unmarshal(raw, &changed)
		change(changed)
		invalid, _ := json.Marshal(changed)
		if _, _, err := verifyMySQLGroupView(invalid, d, members[0], members); err == nil {
			t.Fatal("unsafe MySQL replication view accepted")
		}
	}
}
