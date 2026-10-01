package cluster

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestClickHouseTopologyPolicyAndSecurity(t *testing.T) {
	d := clickhouseFixture()
	d.Spec.Mode = "cluster"
	d.Spec.Replicas = 1
	d.Spec.Placement = database.Placement{Spread: "nodes", NodeNames: []string{"worker-a", "worker-b", "worker-c"}}
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	object = object.DeepCopy()
	applyDatabasePolicy(object, d.Spec, DatabasePolicy{Pool: "paid", RuntimeClass: "runsc", StorageClass: "block", NodeNames: d.Spec.Placement.NodeNames})
	applyDatabasePlacement(object, d.Spec, d.Spec.Placement.NodeNames)
	if object.GetKind() != "ClickHouseInstallation" || d.Spec.KeeperInstances() != 3 || d.Spec.PlacementDomains() != 3 {
		t.Fatal("cluster or Keeper allocation is missing")
	}
	templates, _, err := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
	if err != nil || len(templates) != 1 {
		t.Fatal(err)
	}
	pod := templates[0].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false || pod["affinity"] == nil || pod["nodeSelector"].(map[string]any)[DatabaseDefaultRuntimeLabel] != "runsc" {
		t.Fatal("ClickHouse escaped its workload policy")
	}
	claims, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "volumeClaimTemplates")
	if len(claims) != 2 {
		t.Fatal("data and backup staging volumes are required")
	}
	for _, raw := range claims {
		if raw.(map[string]any)["spec"].(map[string]any)["storageClassName"] != "block" {
			t.Fatal("ClickHouse volume escaped its storage policy")
		}
	}
	var root any
	if err = xml.Unmarshal([]byte(clickhouseServerConfiguration(d)), &root); err != nil {
		t.Fatal("invalid ClickHouse configuration", err)
	}
	if err = xml.Unmarshal([]byte(clickhouseKeeperConfiguration(d)), &root); err != nil {
		t.Fatal("invalid Keeper configuration", err)
	}
	raw, err := json.Marshal(object.Object)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "rootCASecretRef") || strings.Contains(string(raw), "InsecureSkipVerify") || !strings.Contains(string(raw), "password_sha256_hex") {
		t.Fatal("ClickHouse trust or credential references are missing")
	}
	for _, mutate := range []func(*database.Spec){func(s *database.Spec) { s.TLS = nil }, func(s *database.Spec) { s.Memory = "1Gi" }, func(s *database.Spec) { s.Replicas = 0 }, func(s *database.Spec) { s.Shards = 9 }, func(s *database.Spec) { s.Version = "latest" }} {
		bad := d.Spec
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid ClickHouse configuration accepted", bad)
		}
	}
}

func TestClickHouseOwnedFieldsAllowDefaultsAndRejectDrift(t *testing.T) {
	expected := corev1.PodSpec{AutomountServiceAccountToken: ptr(false), Containers: []corev1.Container{{Name: "keeper", Image: clickhouseKeeperImage, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false)}, ReadinessProbe: &corev1.Probe{TimeoutSeconds: 3}}}}
	actual := *expected.DeepCopy()
	actual.DNSPolicy = corev1.DNSClusterFirst
	actual.Containers[0].ReadinessProbe.FailureThreshold = 3
	if !safeClickHouseKeeperTemplate(actual, expected) {
		t.Fatal("API defaults were treated as workload drift")
	}
	actual.Containers[0].SecurityContext.AllowPrivilegeEscalation = ptr(true)
	if safeClickHouseKeeperTemplate(actual, expected) {
		t.Fatal("explicit security policy drift was accepted")
	}
	actual = *expected.DeepCopy()
	actual.HostNetwork = true
	if safeClickHouseKeeperTemplate(actual, expected) {
		t.Fatal("host network escape was accepted")
	}
	actual = *expected.DeepCopy()
	actual.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "unrelated"}}}}
	if safeClickHouseKeeperTemplate(actual, expected) {
		t.Fatal("unowned credential injection was accepted")
	}
}

func TestClickHouseKeeperRejectsAmbiguousQuorum(t *testing.T) {
	for _, bad := range []string{"", "zk_server_state\tstandalone", "zk_server_state\tleader\nzk_server_state\tfollower"} {
		if _, err := clickhouseKeeperRole(bad); err == nil {
			t.Fatal("invalid Keeper quorum accepted")
		}
	}
	if role, err := clickhouseKeeperRole("zk_server_state\tleader\nzk_followers\t2"); err != nil || role != "leader" {
		t.Fatal(role, err)
	}
}
