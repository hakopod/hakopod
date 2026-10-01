package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func oracleEnterpriseFixture() database.Resource {
	d := oracleFixture()
	d.Spec.Version = "19"
	d.Spec.Mode = "cluster"
	d.Spec.Replicas = 2
	d.Spec.Oracle = &database.OracleConfig{Edition: "enterprise", Image: "registry.example.com/customer/oracle:19@sha256:" + strings.Repeat("a", 64), RegistryCredential: "licensed", LicenseConfirmed: true}
	return d
}

func TestOracleEnterpriseSwitchoverReviewScopeAndTarget(t *testing.T) {
	d := oracleEnterpriseFixture()
	plan := database.OracleSwitchoverReview{RequestID: strings.Repeat("a", 32), DatabaseID: d.ID, Project: d.Project, Environment: d.Environment, Revision: d.Revision, BrokerUID: "broker", TopologyFingerprint: "fingerprint", Primary: "database-pod", Target: "database-1-pod", TargetController: "database-1", TargetUniqueName: "HPDB1", MemberUIDs: []string{"one", "two", "three"}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := oracleSwitchoverIdentity(d, plan); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*database.OracleSwitchoverReview){
		func(p *database.OracleSwitchoverReview) { p.Project = "other" },
		func(p *database.OracleSwitchoverReview) { p.Environment = "other" },
		func(p *database.OracleSwitchoverReview) { p.Revision++ },
		func(p *database.OracleSwitchoverReview) { p.Target = p.Primary },
		func(p *database.OracleSwitchoverReview) { p.TargetUniqueName = "HPDB0" },
		func(p *database.OracleSwitchoverReview) { p.TargetController = "foreign" },
		func(p *database.OracleSwitchoverReview) { p.RequestID = "reused-arbitrary-token" },
		func(p *database.OracleSwitchoverReview) { p.MemberUIDs = []string{"one"} },
	} {
		changed := plan
		change(&changed)
		if oracleSwitchoverIdentity(d, changed) == nil {
			t.Fatal("invalid role review admitted")
		}
	}
}

func TestOracleEnterpriseRouteGuardKeepsMaintenanceClosed(t *testing.T) {
	ctx := context.Background()
	d := oracleEnterpriseFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: types.UID("owned"), Labels: databaseLabels(d)}}
	c := &Client{kube: fake.NewClientset(ns)}
	if err := c.reconcileOracleEnterpriseRoute(ctx, d, nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	blocked := errors.New("lost durable claim")
	if err := c.guardOracleRoleRoute(ctx, d, "request-one", false, func() error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal("guard ignored durable fence")
	}
	if err := c.guardOracleRoleRoute(ctx, d, "request-one", false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	service, err := c.kube.CoreV1().Services(ns.Name).Get(ctx, "database-rw", metav1.GetOptions{})
	if err != nil || service.Spec.Selector["hakopod.io/oracle-pod"] != "unverified" || service.Annotations[oracleRoleRequest] != "request-one" {
		t.Fatal("role guard did not close route atomically")
	}
	if err = c.guardOracleRoleRoute(ctx, d, "request-two", true, func() error { return nil }); err == nil {
		t.Fatal("another request released the route guard")
	}
	if err = c.guardOracleRoleRoute(ctx, d, "request-one", true, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestOracleEnterpriseBrokerAndStandbyOwnershipAreRequired(t *testing.T) {
	ctx := context.Background()
	d := oracleEnterpriseFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: types.UID("namespace"), Labels: databaseLabels(d)}}
	root := oracleEnterpriseObject(d, 0, "", nil, nil)
	root.SetUID("root")
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	broker := oracleEnterpriseBroker(d, "", nil, nil)
	broker.SetUID("broker")
	broker.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: "database", UID: root.GetUID(), Controller: ptr(true)}})
	c := &Client{kube: fake.NewClientset(ns), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)}
	if err := c.oracleEnterpriseObjectOwned(ctx, d, broker); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*unstructured.Unstructured){
		func(o *unstructured.Unstructured) { o.SetOwnerReferences(nil) },
		func(o *unstructured.Unstructured) {
			owners := o.GetOwnerReferences()
			owners[0].UID = "foreign"
			o.SetOwnerReferences(owners)
		},
		func(o *unstructured.Unstructured) {
			labels := o.GetLabels()
			labels["hakopod.io/project"] = "other"
			o.SetLabels(labels)
		},
		func(o *unstructured.Unstructured) {
			annotations := o.GetAnnotations()
			annotations["hakopod.io/database-revision"] = "999"
			o.SetAnnotations(annotations)
		},
	} {
		changed := broker.DeepCopy()
		change(changed)
		if c.oracleEnterpriseObjectOwned(ctx, d, changed) == nil {
			t.Fatal("detached or stale broker was accepted")
		}
	}
}

func TestOracleEnterpriseAdmissionRemainsClosed(t *testing.T) {
	d := oracleEnterpriseFixture()
	kube := fake.NewClientset()
	c := &Client{kube: kube}
	if err := c.applyOracleEnterpriseDatabase(context.Background(), d, []byte(strings.Repeat("a", 64)), func() error { t.Fatal("unqualified runtime reached mutation fence"); return nil }); err == nil {
		t.Fatal("unqualified Oracle Enterprise was admitted")
	}
	if len(kube.Actions()) != 0 {
		t.Fatal("qualification refusal must precede cluster effects")
	}
	object, err := DatabaseObject(d)
	if err != nil || object.GetKind() != "SingleInstanceDatabase" {
		t.Fatalf("a review should remain renderable: %v", err)
	}
}

func TestOracleEnterpriseResourcesAndPlacement(t *testing.T) {
	d := oracleEnterpriseFixture()
	d.Spec.Placement.Spread = "zones"
	p := &DatabasePolicy{Pool: "paid", RuntimeClass: "runsc", StorageClass: "block"}
	member := oracleEnterpriseObject(d, 2, "scoped-registry", p, []string{"worker-a", "worker-b", "worker-c"})
	if member.GetName() != "database-2" {
		t.Fatal("member identity is unstable")
	}
	createAs, _, _ := unstructured.NestedString(member.Object, "spec", "createAs")
	source, _, _ := unstructured.NestedString(member.Object, "spec", "primarySource", "databaseRef")
	if createAs != "standby" || source != "database" {
		t.Fatal("Data Guard standby must use the owned primary source")
	}
	for _, volume := range []string{"oradata", "fra"} {
		class, _, _ := unstructured.NestedString(member.Object, "spec", "persistence", volume, "storageClass")
		size, _, _ := unstructured.NestedString(member.Object, "spec", "persistence", volume, "size")
		if class != "block" || size != "10Gi" {
			t.Fatal("data/FRA storage escaped its allocation")
		}
	}
	var envelope struct {
		Profile          string                      `json:"profile"`
		Labels           map[string]string           `json:"labels"`
		RuntimeClassName string                      `json:"runtimeClassName"`
		Affinity         *corev1.Affinity            `json:"affinity"`
		Resources        corev1.ResourceRequirements `json:"resources"`
	}
	if json.Unmarshal([]byte(member.GetAnnotations()[oracleEnterprisePodPolicy]), &envelope) != nil || envelope.Profile != oracleEnterprisePolicy || envelope.RuntimeClassName != "runsc" || envelope.Labels[oracleEnterpriseMemberLabel] != "true" || envelope.Labels[registryScopeLabel] != RegistryScope(d.Project, d.Environment, "licensed") {
		t.Fatal("Oracle pod envelope lost its sandbox, identity or registry scope")
	}
	if envelope.Affinity == nil || len(envelope.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) != 3 || envelope.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].TopologyKey != corev1.LabelTopologyZone {
		t.Fatal("requested node and zone restrictions are missing")
	}
	broker := oracleEnterpriseBroker(d, "scoped-registry", p, []string{"worker-a"})
	envelope.Labels = nil
	if json.Unmarshal([]byte(broker.GetAnnotations()[oracleEnterprisePodPolicy]), &envelope) != nil || envelope.Resources.Limits.Cpu().MilliValue() != 100 || envelope.Resources.Limits.Memory().Value() != 256<<20 || envelope.Labels[oracleEnterpriseMemberLabel] != "" {
		t.Fatal("broker helper must have a separate bounded reservation")
	}
	fsfo, _, _ := unstructured.NestedBool(broker.Object, "spec", "topology", "policy", "fastStartFailover")
	if fsfo {
		t.Fatal("an unqualified automatic promotion must not be enabled")
	}
}

func TestOracleEnterpriseHealthRejectsSplitBrainAndUnhealthyStandbys(t *testing.T) {
	d := oracleEnterpriseFixture()
	health := []oracleEnterpriseHealth{
		{Role: "PRIMARY", Mode: "READ WRITE", Version: "19.25.0.0.0", PDB: "READ WRITE", UniqueName: "HPDB0", DBID: "1234", Protection: "MAXIMUM AVAILABILITY", Synchronized: 2, SynchronizedMembers: []string{"HPDB1", "HPDB2"}},
		{Role: "PHYSICAL STANDBY", Mode: "MOUNTED", Version: "19.25.0.0.0", UniqueName: "HPDB1", DBID: "1234", ApplyProcesses: 1},
		{Role: "PHYSICAL STANDBY", Mode: "MOUNTED", Version: "19.25.0.0.0", UniqueName: "HPDB2", DBID: "1234", ApplyProcesses: 1},
	}
	if primary, err := validateOracleEnterpriseHealth(d.Spec, health); err != nil || primary != 0 {
		t.Fatalf("valid native observation rejected: %d %v", primary, err)
	}
	for _, test := range []struct {
		name   string
		change func([]oracleEnterpriseHealth)
	}{
		{"two primaries", func(h []oracleEnterpriseHealth) {
			h[1].Role = "PRIMARY"
			h[1].Mode = "READ WRITE"
			h[1].PDB = "READ WRITE"
		}},
		{"foreign database", func(h []oracleEnterpriseHealth) { h[2].DBID = "9999" }},
		{"foreign identity", func(h []oracleEnterpriseHealth) { h[2].UniqueName = "OTHER" }},
		{"apply stopped", func(h []oracleEnterpriseHealth) { h[1].ApplyProcesses = 0 }},
		{"unlicensed read standby", func(h []oracleEnterpriseHealth) { h[1].Mode = "READ ONLY WITH APPLY" }},
		{"transport lag", func(h []oracleEnterpriseHealth) { h[0].Synchronized = 1 }},
		{"foreign synchronized destination", func(h []oracleEnterpriseHealth) { h[0].SynchronizedMembers = []string{"HPDB1", "FOREIGN"} }},
		{"duplicate synchronized destination", func(h []oracleEnterpriseHealth) { h[0].SynchronizedMembers = []string{"HPDB1", "HPDB1"} }},
		{"protection degraded", func(h []oracleEnterpriseHealth) { h[0].Protection = "RESYNCHRONIZATION" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := append([]oracleEnterpriseHealth(nil), health...)
			test.change(changed)
			if _, err := validateOracleEnterpriseHealth(d.Spec, changed); err == nil {
				t.Fatal("unhealthy topology admitted to application routing")
			}
		})
	}
}

func TestOracleEnterpriseRegistryScopeRotationAndProvisioningReference(t *testing.T) {
	ctx := context.Background()
	d := oracleEnterpriseFixture()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: types.UID("owned-namespace"), Labels: databaseLabels(d)}}
	kube := fake.NewClientset(ns)
	current := "oracle-registry-one"
	c := &Client{kube: kube, options: Options{RegistrySecretName: func(_ context.Context, project, environment, name string) (string, error) {
		if project != d.Project || environment != d.Environment || name != "licensed" {
			return "", errors.New("not in scope")
		}
		return current, nil
	}}}
	credential := RegistryCredential{Registry: "registry.example.com", Username: "licensed-user", Password: "test-registry-password-one", Revision: 1}
	scope := RegistryScope(d.Project, d.Environment, "licensed")
	if err := c.PutPlatformSecret(ctx, current, corev1.SecretTypeDockerConfigJson, RegistrySecretData(credential), map[string]string{registryScopeLabel: scope}); err != nil {
		t.Fatal(err)
	}
	blocked := errors.New("durable claim lost")
	if _, err := c.prepareOracleEnterpriseRegistry(ctx, d, func() error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal("registry copy ignored its durable operation fence")
	}
	name, err := c.prepareOracleEnterpriseRegistry(ctx, d, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	secret, err := kube.CoreV1().Secrets(ns.Name).Get(ctx, name, metav1.GetOptions{})
	if err != nil || len(secret.Data) != 1 || secret.Labels[registryRevisionLabel] != "1" {
		t.Fatal("registry copy must contain only scoped authentication data")
	}
	if used, err := c.RegistryInUse(ctx, d.Project, d.Environment, "licensed"); err != nil || !used {
		t.Fatalf("provisioning reference did not block registry deletion: %v", err)
	}
	current = "oracle-registry-two"
	credential.Revision, credential.Password = 2, "test-registry-password-two"
	if err = c.PutPlatformSecret(ctx, current, corev1.SecretTypeDockerConfigJson, RegistrySecretData(credential), map[string]string{registryScopeLabel: scope}); err != nil {
		t.Fatal(err)
	}
	if count, err := c.RefreshRegistryCopies(ctx, d.Project, d.Environment, "licensed", false); err != nil || count != 1 {
		t.Fatalf("database registry rotation failed: %d %v", count, err)
	}
	secret, err = kube.CoreV1().Secrets(ns.Name).Get(ctx, name, metav1.GetOptions{})
	if err != nil || secret.Labels[registryRevisionLabel] != "2" || !strings.Contains(string(secret.Data[corev1.DockerConfigJsonKey]), credential.Password) {
		t.Fatal("database registry copy did not rotate")
	}
	for _, change := range []func(*database.Resource){
		func(other *database.Resource) { other.Project = "other" },
		func(other *database.Resource) { other.Environment = "other" },
		func(other *database.Resource) {
			copy := *other.Spec.Oracle
			copy.Image = strings.Replace(copy.Image, "registry.example.com", "untrusted.example.com", 1)
			other.Spec.Oracle = &copy
		},
	} {
		other := d
		change(&other)
		if _, err := c.prepareOracleEnterpriseRegistry(ctx, other, func() error { return nil }); err == nil {
			t.Fatal("registry credential crossed its scope or image host")
		}
	}
}
