package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func myduckFixture() database.Resource {
	return database.Resource{ID: strings.Repeat("a", 32), Revision: 1, Project: "demo", Environment: "development", Status: "ready", Spec: database.Spec{SchemaVersion: 1, Name: "myduck-fixture", Engine: "duckdb", Version: database.MyDuckVersion, Mode: "standalone", Shards: 1, CPU: "1", Memory: "1Gi", StorageGiB: 2, TLS: &database.TLSConfig{Mode: "required"}}}
}

func myduckFixtureSet(t *testing.T) (database.Resource, *appsv1.StatefulSet) {
	t.Helper()
	d := myduckFixture()
	resources := map[string]any{"requests": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}, "limits": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}}
	set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "set-uid"}}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(myduckDatabaseSpec(d, resources), &set.Spec); err != nil {
		t.Fatal(err)
	}
	return d, set
}

func TestMyDuckTemplateHasOnePersistentDualProtocolMember(t *testing.T) {
	d, set := myduckFixtureSet(t)
	if set.Spec.Replicas == nil || *set.Spec.Replicas != 1 || len(set.Spec.VolumeClaimTemplates) != 1 || set.Spec.VolumeClaimTemplates[0].Name != "data" {
		t.Fatal("MyDuck requires one persistent instance")
	}
	pod := set.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.EnableServiceLinks == nil || *pod.EnableServiceLinks || len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
		t.Fatal("MyDuck received unintended process or credentials")
	}
	c := pod.Containers[0]
	if len(c.Ports) != 2 || c.Ports[0].ContainerPort != 3306 || c.Ports[1].ContainerPort != 5432 {
		t.Fatal("MyDuck must expose both reviewed wire protocols")
	}
	if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("MyDuck runtime is not restricted")
	}
	var config struct {
		Memory         int64  `json:"memory_limit_bytes"`
		Threads        int64  `json:"threads"`
		MaxConnections int    `json:"max_connections"`
		DataDir        string `json:"data_dir"`
		Name           string `json:"server_name"`
	}
	if err := json.Unmarshal([]byte(myduckConfiguration(d)["managed.json"]), &config); err != nil {
		t.Fatal(err)
	}
	if config.Memory != 751619276 || config.Threads != 1 || config.MaxConnections != 64 || config.DataDir != "/var/lib/myduck" || config.Name != "database."+DatabaseNamespace(d.ID)+".svc" {
		t.Fatal("unexpected MyDuck limits or identity", config)
	}
}

func TestMyDuckStorageHelperHasNoDatabaseIngressOrSecrets(t *testing.T) {
	d, set := myduckFixtureSet(t)
	record := myduckColdRecord{JobID: strings.Repeat("b", 32), Revision: 1, NamespaceUID: "namespace-uid", SetUID: set.UID, ClaimUID: "claim-uid", VolumeUID: "volume-uid", MemberUID: "member-uid", Node: "development-node"}
	helper := myduckStoragePod(d, set, record)
	helper.UID = "helper-uid"
	if helper.Labels[myduckMemberLabel] != "" || helper.Labels[databaseRecoveryHelper] != "true" {
		t.Fatal("helper could receive database traffic")
	}
	if len(helper.Spec.Volumes) != 1 || helper.Spec.Volumes[0].PersistentVolumeClaim == nil || len(helper.Spec.Containers) != 1 || len(helper.Spec.Containers[0].Ports) != 0 || len(helper.Spec.Containers[0].Env) != 0 {
		t.Fatal("helper received excess volumes, listeners or environment")
	}
	if !myduckStoragePodMatches(helper, myduckStoragePod(d, set, record), d, record) {
		t.Fatal("exact helper was rejected")
	}
	defaulted := helper.DeepCopy()
	defaulted.Spec.ServiceAccountName, defaulted.Spec.DeprecatedServiceAccount = "default", "default"
	defaulted.Spec.SchedulerName, defaulted.Spec.DNSPolicy = "default-scheduler", corev1.DNSClusterFirst
	seconds := int64(300)
	defaulted.Spec.Tolerations = append(defaulted.Spec.Tolerations, corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds})
	defaulted.Spec.Containers[0].TerminationMessagePath, defaulted.Spec.Containers[0].TerminationMessagePolicy = "/dev/termination-log", corev1.TerminationMessageReadFile
	if !myduckStoragePodMatches(defaulted, myduckStoragePod(d, set, record), d, record) {
		t.Fatal("harmless Kubernetes defaults were rejected")
	}
	terminating := helper.DeepCopy()
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	if !myduckStoragePodMatches(terminating, myduckStoragePod(d, set, record), d, record) {
		t.Fatal("cleanup cannot recognize its terminating helper")
	}
	changes := map[string]func(*corev1.Pod){
		"host network":   func(p *corev1.Pod) { p.Spec.HostNetwork = true },
		"node":           func(p *corev1.Pod) { p.Spec.NodeName = "other-node" },
		"image":          func(p *corev1.Pod) { p.Spec.Containers[0].Image = "other:image" },
		"member ingress": func(p *corev1.Pod) { p.Labels[myduckMemberLabel] = "true" },
		"token":          func(p *corev1.Pod) { yes := true; p.Spec.AutomountServiceAccountToken = &yes },
		"secret": func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "database-credentials"}}})
		},
		"capability": func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
		},
		"foreign owner":    func(p *corev1.Pod) { p.OwnerReferences[0].UID = "other-namespace" },
		"foreign job":      func(p *corev1.Pod) { p.Annotations[myduckColdAnnotation] = strings.Repeat("c", 32) },
		"termination data": func(p *corev1.Pod) { p.Spec.Containers[0].TerminationMessagePath = "/var/lib/myduck/app.db" },
		"termination logs": func(p *corev1.Pod) {
			p.Spec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError
		},
		"scheduler": func(p *corev1.Pod) { p.Spec.SchedulerName = "unexpected-scheduler" },
		"host DNS":  func(p *corev1.Pod) { p.Spec.DNSPolicy = corev1.DNSDefault },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			altered := helper.DeepCopy()
			change(altered)
			if myduckStoragePodMatches(altered, myduckStoragePod(d, set, record), d, record) {
				t.Fatal("unsafe helper was accepted")
			}
		})
	}
}

func TestMyDuckColdReceiptPinsEveryPersistentIdentity(t *testing.T) {
	d, set := myduckFixtureSet(t)
	record := myduckColdRecord{JobID: strings.Repeat("b", 32), Revision: 1, NamespaceUID: "namespace-uid", SetUID: set.UID, ClaimUID: "claim-uid", VolumeUID: "volume-uid", MemberUID: "member-uid", Node: "development-node"}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	set.Annotations = map[string]string{myduckColdAnnotation: string(encoded)}
	if _, err := myduckReadColdRecord(set, d, record.JobID); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{strings.Replace(string(encoded), `"revision":1`, `"revision":2`, 1), strings.Replace(string(encoded), `"set_uid":"set-uid"`, `"set_uid":"other"`, 1), string(encoded) + `{}`, strings.Repeat(" ", 2049)} {
		set.Annotations[myduckColdAnnotation] = changed
		if _, err := myduckReadColdRecord(set, d, record.JobID); err == nil {
			t.Fatal("changed cold-storage receipt was accepted")
		}
	}
}
