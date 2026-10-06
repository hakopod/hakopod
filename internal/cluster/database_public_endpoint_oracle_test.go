package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

type oraclePublicEndpointCoreV1 struct {
	typedcorev1.CoreV1Interface
}

func (oraclePublicEndpointCoreV1) RESTClient() rest.Interface { return &rest.RESTClient{} }

type oraclePublicEndpointKubeClient struct {
	kubernetes.Interface
	core typedcorev1.CoreV1Interface
}

func (c oraclePublicEndpointKubeClient) CoreV1() typedcorev1.CoreV1Interface { return c.core }

func oraclePublicEndpointUnitFixture() database.Resource {
	now := time.Now().UTC()
	topology := fmt.Sprintf("%x", sha256.Sum256([]byte("database-0:pod-uid")))
	return database.Resource{
		ID: "0123456789abcdef0123456789abcdef", Project: "demo", Environment: "development", Revision: 3, Status: "ready", PublicEndpointAccess: true,
		Spec:        database.Spec{SchemaVersion: 1, Name: "orders", Engine: "oracle", Version: "23.26", Mode: "standalone", Replicas: 0, Shards: 1, CPU: "1", Memory: "4Gi", StorageGiB: 10, TLS: &database.TLSConfig{Mode: "required"}, Oracle: &database.OracleConfig{Edition: "free"}},
		Observation: database.Observation{ObservedAt: now, Revision: 3, Status: "ready", Primary: "database-0", TopologyFingerprint: topology, Members: []database.Member{{Name: "database-0", UID: "pod-uid", Role: "primary", Node: "worker-a", Ready: true}}, Endpoints: []database.Endpoint{{Purpose: "read_write", Host: oracleHost(database.Resource{ID: "0123456789abcdef0123456789abcdef"}), Port: 2484}}, TLS: &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "leaf"}},
	}
}

func oraclePublicEndpointNamespace(d database.Resource) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: "namespace-uid", Labels: databaseLabels(d)}}
}

func oraclePublicEndpointStatefulSet(d database.Resource) *appsv1.StatefulSet {
	one := int32(1)
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "statefulset-uid", Labels: databaseLabels(d), Annotations: map[string]string{"hakopod.io/database-revision": "3"}}, Spec: appsv1.StatefulSetSpec{Replicas: &one, ServiceName: "database", Selector: &metav1.LabelSelector{MatchLabels: map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}}}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, CurrentReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1}}
}

func oraclePublicEndpointService(d database.Resource) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: DatabaseNamespace(d.ID), UID: "service-uid", Labels: databaseLabels(d), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: DatabaseNamespace(d.ID), UID: "namespace-uid"}}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.17", Selector: map[string]string{databaseOwner: d.ID, managedBy: "hakopod"}, Ports: []corev1.ServicePort{{Name: "tcps", Port: 2484, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(2484)}}}}
}

func TestOracleFreePublicIdentityNamesAreBoundedAndEnterpriseStaysPrivate(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	d.PublicEndpointNames = []string{"database-12484.example.test"}
	names := databaseIdentityNames(d)
	if !reflect.DeepEqual(names, []string{"database", "database." + DatabaseNamespace(d.ID), "database." + DatabaseNamespace(d.ID) + ".svc", "database." + DatabaseNamespace(d.ID) + ".svc.cluster.local", "database-12484.example.test"}) {
		t.Fatal("Oracle Free public SAN was not appended to the private identity", names)
	}
	d.Spec.Mode = "cluster"
	d.Spec.Replicas = 2
	d.Spec.Oracle.Edition = "enterprise"
	for _, name := range databaseIdentityNames(d) {
		if name == "database-12484.example.test" {
			t.Fatal("Oracle Enterprise inherited the Free public SAN path")
		}
	}
}

func TestOracleFreePublicWorkloadRequiresExactOwnedRevision(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	ns := oraclePublicEndpointNamespace(d)
	set := oraclePublicEndpointStatefulSet(d)
	c := &Client{kube: kubefake.NewSimpleClientset(ns, set)}
	if _, _, err := c.oraclePublicEndpointWorkload(context.Background(), d); err != nil {
		t.Fatal("valid Oracle Free workload rejected", err)
	}
	for name, change := range map[string]func(*appsv1.StatefulSet){
		"revision": func(candidate *appsv1.StatefulSet) { candidate.Annotations["hakopod.io/database-revision"] = "2" },
		"selector": func(candidate *appsv1.StatefulSet) { candidate.Spec.Selector.MatchLabels[databaseOwner] = "other" },
		"service":  func(candidate *appsv1.StatefulSet) { candidate.Spec.ServiceName = "database-public" },
		"replicas": func(candidate *appsv1.StatefulSet) { *candidate.Spec.Replicas = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := set.DeepCopy()
			change(candidate)
			client := &Client{kube: kubefake.NewSimpleClientset(ns.DeepCopy(), candidate)}
			if _, _, err := client.oraclePublicEndpointWorkload(context.Background(), d); err == nil {
				t.Fatal("changed Oracle Free workload accepted")
			}
		})
	}
}

func TestOracleFreePublicServiceRequiresOnlyOwnedTCPS(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	route, err := database.PublicEndpointRouteFor(d.Spec, "read_write")
	if err != nil {
		t.Fatal(err)
	}
	valid := oraclePublicEndpointService(d)
	if !oraclePublicEndpointServiceOwned(valid, d, "namespace-uid", route) {
		t.Fatal("valid Oracle Free TCPS Service rejected")
	}
	for name, change := range map[string]func(*corev1.Service){
		"tcp_port":    func(service *corev1.Service) { service.Spec.Ports[0].Port = 1521 },
		"target_port": func(service *corev1.Service) { service.Spec.Ports[0].TargetPort = intstr.FromInt(1521) },
		"port_name":   func(service *corev1.Service) { service.Spec.Ports[0].Name = "tcp" },
		"extra_tcp_port": func(service *corev1.Service) {
			service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{Name: "tcp", Port: 1521, TargetPort: intstr.FromInt(1521)})
		},
		"foreign_owner":    func(service *corev1.Service) { service.OwnerReferences[0].UID = "foreign" },
		"foreign_selector": func(service *corev1.Service) { service.Spec.Selector[databaseOwner] = "foreign" },
		"publish_unready":  func(service *corev1.Service) { service.Spec.PublishNotReadyAddresses = true },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid.DeepCopy()
			change(candidate)
			if oraclePublicEndpointServiceOwned(candidate, d, "namespace-uid", route) {
				t.Fatal("unsafe Oracle Free Service accepted")
			}
		})
	}
}

func TestOracleFreePublicMemberExposesOnlyContainerTCPS(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "oracle", Ports: []corev1.ContainerPort{{Name: "tcps", ContainerPort: 2484, Protocol: corev1.ProtocolTCP}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if !oraclePublicEndpointPodReady(pod) {
		t.Fatal("valid Oracle Free member rejected")
	}
	for name, change := range map[string]func(*corev1.Pod){
		"plaintext_port": func(candidate *corev1.Pod) {
			candidate.Spec.Containers[0].Ports = append(candidate.Spec.Containers[0].Ports, corev1.ContainerPort{Name: "tcp", ContainerPort: 1521})
		},
		"host_network": func(candidate *corev1.Pod) { candidate.Spec.HostNetwork = true },
		"sidecar": func(candidate *corev1.Pod) {
			candidate.Spec.Containers = append(candidate.Spec.Containers, corev1.Container{Name: "unknown"})
		},
		"not_ready": func(candidate *corev1.Pod) { candidate.Status.Conditions[0].Status = corev1.ConditionFalse },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := pod.DeepCopy()
			change(candidate)
			if oraclePublicEndpointPodReady(candidate) {
				t.Fatal("unsafe Oracle Free member accepted")
			}
		})
	}
}

func TestOracleFreePublicNetworkPolicyExposesOnlyTCPSFromOwnedIngress(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	c := &Client{kube: kubefake.NewSimpleClientset(), options: Options{ProxyNamespace: "haproxy-controller", ProxyRelease: "hakopod-ingress"}}
	if err := c.databaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	policy, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	publicRules := 0
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "haproxy-controller" {
				continue
			}
			publicRules++
			if len(rule.Ports) != 1 || rule.Ports[0].Protocol == nil || *rule.Ports[0].Protocol != corev1.ProtocolTCP || rule.Ports[0].Port == nil || rule.Ports[0].Port.Type != intstr.Int || rule.Ports[0].Port.IntVal != 2484 {
				t.Fatal("Oracle public policy exposed a non-TCPS port", rule.Ports)
			}
		}
	}
	if publicRules != 1 {
		t.Fatal("Oracle public policy did not add one owned ingress rule", policy.Spec.Ingress)
	}
	d.Status = "restoring"
	if err = c.databaseNetworkPolicy(context.Background(), d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	policy, err = c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(d.ID)).Get(context.Background(), "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "haproxy-controller" {
				t.Fatal("restoring Oracle database retained public ingress")
			}
		}
	}
}

func TestOracleFreePublicPersistentIdentityRejectsReplacementOrMissingClaims(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-0", Namespace: DatabaseNamespace(d.ID), UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-database-0"}}}, {Name: "backup", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "backup-database-0"}}}}}}
	claim := func(name, uid string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: DatabaseNamespace(d.ID), UID: types.UID(uid), Labels: databaseLabels(d)}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-" + name}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	}
	volume := func(name, claimUID, volumeUID, handle string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name, UID: types.UID(volumeUID)}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: DatabaseNamespace(d.ID), Name: name, UID: types.UID(claimUID)}, PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "storage.example.test", VolumeHandle: handle}}}}
	}
	c := &Client{kube: kubefake.NewSimpleClientset(claim("data-database-0", "data-uid"), claim("backup-database-0", "backup-uid"), volume("data-database-0", "data-uid", "data-pv-uid", "data-handle"), volume("backup-database-0", "backup-uid", "backup-pv-uid", "backup-handle"))}
	original, err := oraclePublicEndpointPVCIdentities(context.Background(), c, d, pod)
	if err != nil {
		t.Fatal("valid Oracle persistent identity rejected", err)
	}
	pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "replacement"
	if err := oraclePublicEndpointPVCsOwned(context.Background(), c, d, pod); err == nil {
		t.Fatal("changed Oracle persistent attachment accepted")
	}
	pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "data-database-0"
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(context.Background(), "pv-data-database-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pv.Spec.PersistentVolumeSource.CSI.VolumeHandle = "replacement-handle"
	if _, err = c.kube.CoreV1().PersistentVolumes().Update(context.Background(), pv, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	changed, err := oraclePublicEndpointPVCIdentities(context.Background(), c, d, pod)
	if err != nil || changed[0].BackingVolumeFingerprint == "" || changed[0].BackingVolumeFingerprint == original[0].BackingVolumeFingerprint {
		t.Fatal("changed Oracle backing volume was not fingerprinted", changed, err)
	}
}

func TestOracleFreePublicLeafResumesOnlyItsPersistedIntent(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	ns := oraclePublicEndpointNamespace(d)
	c := &Client{kube: kubefake.NewSimpleClientset(ns)}
	if err := c.prepareDatabaseIdentity(context.Background(), d, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	issuer, err := api.Get(context.Background(), "database-ca", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trust, _, err := database.ParsePublicTrust(issuer.Data["ca.crt"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.PublicEndpointNames = []string{"database-12484.example.test"}
	intent := strings.Repeat("a", 64)
	writes := 0
	before := func() error { writes++; return nil }
	if err := c.prepareDatabaseIdentityForTransition(context.Background(), d, intent, trust.Fingerprint, before); err != nil {
		t.Fatal(err)
	}
	issued, err := api.Get(context.Background(), "database-tls", metav1.GetOptions{})
	if err != nil || issued.Annotations["hakopod.io/public-endpoint-identity-intent"] != intent {
		t.Fatal("issued leaf was not bound to the durable intent", err)
	}
	issuedData := map[string][]byte{}
	for key, value := range issued.Data {
		issuedData[key] = append([]byte(nil), value...)
	}
	writes = 0
	if err = c.prepareDatabaseIdentityForTransition(context.Background(), d, intent, trust.Fingerprint, before); err != nil || writes != 0 {
		t.Fatal("exact post-Secret retry did not resume without mutation", err, writes)
	}
	resumed, err := api.Get(context.Background(), "database-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for key := range issuedData {
		if !bytes.Equal(issuedData[key], resumed.Data[key]) {
			t.Fatal("exact intent retry rotated its key pair", key)
		}
	}
	if err = c.prepareDatabaseIdentityForTransition(context.Background(), d, strings.Repeat("b", 64), trust.Fingerprint, before); err != nil {
		t.Fatal(err)
	}
	replaced, err := api.Get(context.Background(), "database-tls", metav1.GetOptions{})
	if err != nil || bytes.Equal(issuedData["tls.key"], replaced.Data["tls.key"]) {
		t.Fatal("different durable intent reused the prior private key", err)
	}
}

func TestOracleFreePublicIdentityRequiresExactOrderedNames(t *testing.T) {
	expected := []string{"database", "database.namespace", "database.namespace.svc", "database.namespace.svc.cluster.local", "database.example.test"}
	if !oraclePublicEndpointIdentityNamesExact(append([]string(nil), expected...), expected) {
		t.Fatal("exact Oracle identity names were rejected")
	}
	for name, actual := range map[string][]string{
		"extra SAN":     append(append([]string(nil), expected...), "unexpected.example.test"),
		"missing SAN":   append([]string(nil), expected[:len(expected)-1]...),
		"reordered SAN": {expected[1], expected[0], expected[2], expected[3], expected[4]},
	} {
		t.Run(name, func(t *testing.T) {
			if oraclePublicEndpointIdentityNamesExact(actual, expected) {
				t.Fatal("changed Oracle identity names were accepted", actual)
			}
		})
	}
}

func TestOracleFreePublicIssuedCARequiresReviewedFingerprint(t *testing.T) {
	if err := oraclePublicEndpointIssuedCAStable("reviewed-ca", "reviewed-ca"); err != nil {
		t.Fatal("reviewed Oracle issuer was rejected", err)
	}
	if err := oraclePublicEndpointIssuedCAStable("changed-ca", "reviewed-ca"); err == nil || err.Error() != "Oracle Free issuer changed during identity issuance" {
		t.Fatal("changed Oracle issuer did not return the explicit issuance error", err)
	}
}

func TestOracleFreeOperatorCannotEnterLegacyPublicTransition(t *testing.T) {
	ctx := context.Background()
	d := oraclePublicEndpointUnitFixture()
	ns := oraclePublicEndpointNamespace(d)
	object, err := oracleFreeObject(d, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	object.SetUID("root-uid")
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	pod := oracleFreeTestPod(t, d)
	base := kubefake.NewSimpleClientset(ns, &pod)
	c := &Client{kube: base, dynamic: dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme(), object), options: Options{ProxyNamespace: "haproxy-controller", ProxyRelease: "hakopod-ingress"}}
	closed := d
	closed.PublicEndpointAccess = false
	if err = c.databaseNetworkPolicy(ctx, closed, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	endpoint := database.PublicEndpoint{ID: "endpoint", DatabaseID: d.ID, Revision: 2, Spec: database.PublicEndpointSpec{Purpose: "read_write"}}
	route, err := database.PublicEndpointRouteFor(d.Spec, endpoint.Spec.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	operation := database.PublicEndpointOperation{ID: "operation", EndpointID: endpoint.ID, DatabaseID: d.ID, Revision: endpoint.Revision, Kind: "publish", Review: &database.PublicEndpointReview{DatabaseRevision: d.Revision, TopologyFingerprint: d.Observation.TopologyFingerprint, TLSFingerprint: d.Observation.TLS.Fingerprint, Route: &route, RouteFingerprint: route.Fingerprint()}}
	if _, err = c.BeginOraclePublicEndpointIdentityTransition(ctx, operation, d, endpoint, []string{"database-12484.example.test"}); err == nil {
		t.Fatal("SIDB entered a StatefulSet-only public identity transition")
	}
	policy, err := base.NetworkingV1().NetworkPolicies(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "haproxy-controller" {
				t.Fatal("unqualified SIDB public transition opened ingress")
			}
		}
	}
}

func TestOracleFreePublicTransitionNeverRenewsIssuer(t *testing.T) {
	now := time.Now().UTC()
	if !databaseIdentityIssuerRenewalRequired(true, nil, now) || !databaseIdentityIssuerRenewalRequired(false, &x509.Certificate{NotAfter: now.Add(29 * 24 * time.Hour)}, now) {
		t.Fatal("Oracle transition did not recognize an issuer renewal boundary")
	}
	if databaseIdentityIssuerRenewalRequired(false, &x509.Certificate{NotAfter: now.Add(31 * 24 * time.Hour)}, now) {
		t.Fatal("Oracle transition rejected an issuer outside the renewal window")
	}
}

func TestOracleFreePublicTransitionIntentBindsOldIdentityAndNotResults(t *testing.T) {
	transition := database.PublicEndpointIdentityTransition{
		SchemaVersion: database.PublicEndpointIdentityTransitionSchemaVersion, OperationID: "operation", Engine: "oracle", Kind: "publish",
		DatabaseRevision: 3, EndpointRevision: 2, RouteFingerprint: "route", DesiredNames: []string{"database.example.test"},
		ReviewedTopologyFingerprint: "old-topology", ReviewedLeafFingerprint: "old-leaf", ReviewedCAFingerprint: "old-ca",
		StatefulSetUID: "statefulset", OriginalGeneration: 7, OriginalTemplateHash: "old-template", OldMember: database.PublicEndpointTransitionMember{Name: "database-0", UID: "old-member"},
		PVCs: []database.PublicEndpointTransitionPVC{{Name: "data-database-0", UID: "data-uid", VolumeName: "data-volume", PersistentVolumeUID: "data-pv-uid", BackingVolumeFingerprint: "data-backing"}, {Name: "backup-database-0", UID: "backup-uid", VolumeName: "backup-volume", PersistentVolumeUID: "backup-pv-uid", BackingVolumeFingerprint: "backup-backing"}},
	}
	intent, err := oraclePublicEndpointTransitionIntent(transition)
	if err != nil || len(intent) != 64 {
		t.Fatal("invalid Oracle transition intent", intent, err)
	}
	result := transition
	result.ProposedLeafFingerprint, result.ProposedCAFingerprint = "new-leaf", "new-ca"
	result.TargetGeneration, result.TargetTemplateHash = 8, "new-template"
	result.FinalMember = &database.PublicEndpointTransitionMember{Name: "database-0", UID: "new-member"}
	result.FinalTopologyFingerprint, result.ServedLeafFingerprint, result.ServedCAFingerprint = "new-topology", "new-leaf", "new-ca"
	resumed, err := oraclePublicEndpointTransitionIntent(result)
	if err != nil || resumed != intent {
		t.Fatal("recording transition results changed the Secret resume intent", resumed, err)
	}
	changed := transition
	changed.PVCs = append([]database.PublicEndpointTransitionPVC(nil), transition.PVCs...)
	changed.PVCs[0].UID = "replacement"
	drifted, err := oraclePublicEndpointTransitionIntent(changed)
	if err != nil || drifted == intent {
		t.Fatal("Oracle transition intent did not bind the original PVC identity", drifted, err)
	}
}

func TestOracleFreePublicTargetHashBindsCompletePodTemplate(t *testing.T) {
	runAsNonRoot := true
	allowPrivilegeEscalation := false
	template := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "database"}, Annotations: map[string]string{"existing": "kept"}},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &runAsNonRoot},
			InitContainers:  []corev1.Container{{Name: "wallet", Image: "wallet@sha256:old"}},
			Containers:      []corev1.Container{{Name: "oracle", Image: "oracle@sha256:old", SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation}}},
			Volumes:         []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-database-0"}}}},
		},
	}
	identity := &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("leaf"), "ca.crt": []byte("ca")}}
	desired, targetHash, err := oraclePublicEndpointDesiredTemplate(template, identity)
	if err != nil {
		t.Fatal(err)
	}
	expected := *template.DeepCopy()
	expected.Annotations["hakopod.io/oracle-identity"] = oracleIdentityFingerprint(identity)
	if !reflect.DeepEqual(desired, expected) {
		t.Fatal("Oracle target changed fields beyond the identity annotation")
	}

	tests := map[string]func(*corev1.PodTemplateSpec){
		"extra init container": func(candidate *corev1.PodTemplateSpec) {
			candidate.Spec.InitContainers = append(candidate.Spec.InitContainers, corev1.Container{Name: "unexpected-init", Image: "unexpected@sha256:new"})
		},
		"extra sidecar": func(candidate *corev1.PodTemplateSpec) {
			candidate.Spec.Containers = append(candidate.Spec.Containers, corev1.Container{Name: "unexpected-sidecar", Image: "unexpected@sha256:new"})
		},
		"added volume": func(candidate *corev1.PodTemplateSpec) {
			candidate.Spec.Volumes = append(candidate.Spec.Volumes, corev1.Volume{Name: "unexpected", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		},
		"changed security settings": func(candidate *corev1.PodTemplateSpec) {
			value := true
			candidate.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = &value
		},
		"changed annotations": func(candidate *corev1.PodTemplateSpec) {
			candidate.Annotations["unexpected"] = "changed"
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := template.DeepCopy()
			change(candidate)
			_, changedHash, hashErr := oraclePublicEndpointDesiredTemplate(*candidate, identity)
			if hashErr != nil {
				t.Fatal(hashErr)
			}
			if changedHash == targetHash {
				t.Fatal("complete Oracle pod template drift was not bound to the target hash")
			}
		})
	}
}

func TestOracleFreePublicSecretResumeRejectsBackgroundWorkloadMutation(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	d.Observation.TLS.CAFingerprint = "old-ca"
	set := oraclePublicEndpointStatefulSet(d)
	set.Generation = 7
	set.Status.ObservedGeneration = 7
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-0", Namespace: DatabaseNamespace(d.ID), UID: types.UID("pod-uid")}}
	pvcs := []database.PublicEndpointTransitionPVC{{Name: "data-database-0", UID: "data-uid", VolumeName: "data-volume", PersistentVolumeUID: "data-pv-uid", BackingVolumeFingerprint: "data-backing"}, {Name: "backup-database-0", UID: "backup-uid", VolumeName: "backup-volume", PersistentVolumeUID: "backup-pv-uid", BackingVolumeFingerprint: "backup-backing"}}
	templateHash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	transition := database.PublicEndpointIdentityTransition{
		ReviewedTopologyFingerprint: d.Observation.TopologyFingerprint, ReviewedLeafFingerprint: d.Observation.TLS.Fingerprint, ReviewedCAFingerprint: d.Observation.TLS.CAFingerprint,
		StatefulSetUID: string(set.UID), OriginalGeneration: set.Generation, OriginalTemplateHash: templateHash,
		OldMember: database.PublicEndpointTransitionMember{Name: "database-0", UID: "pod-uid"}, PVCs: pvcs,
	}
	if err = oraclePublicEndpointOldResourcesMatch(d, transition, set, pod, pvcs); err != nil {
		t.Fatal("unchanged old proof was rejected", err)
	}

	tests := map[string]func(*appsv1.StatefulSet, *corev1.Pod, []database.PublicEndpointTransitionPVC){
		"statefulset generation": func(candidate *appsv1.StatefulSet, _ *corev1.Pod, _ []database.PublicEndpointTransitionPVC) {
			candidate.Generation++
		},
		"statefulset template": func(candidate *appsv1.StatefulSet, _ *corev1.Pod, _ []database.PublicEndpointTransitionPVC) {
			candidate.Spec.Template.Spec.InitContainers = append(candidate.Spec.Template.Spec.InitContainers, corev1.Container{Name: "background"})
		},
		"replacement pod": func(_ *appsv1.StatefulSet, candidate *corev1.Pod, _ []database.PublicEndpointTransitionPVC) {
			candidate.UID = "replacement-pod"
		},
		"replacement pvc": func(_ *appsv1.StatefulSet, _ *corev1.Pod, candidate []database.PublicEndpointTransitionPVC) {
			candidate[0].UID = "replacement-pvc"
		},
		"replacement persistent volume": func(_ *appsv1.StatefulSet, _ *corev1.Pod, candidate []database.PublicEndpointTransitionPVC) {
			candidate[0].PersistentVolumeUID = "replacement-pv"
		},
		"changed backing volume": func(_ *appsv1.StatefulSet, _ *corev1.Pod, candidate []database.PublicEndpointTransitionPVC) {
			candidate[0].BackingVolumeFingerprint = "replacement-backing"
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidateSet := set.DeepCopy()
			candidatePod := pod.DeepCopy()
			candidatePVCs := append([]database.PublicEndpointTransitionPVC(nil), pvcs...)
			change(candidateSet, candidatePod, candidatePVCs)
			if matchErr := oraclePublicEndpointOldResourcesMatch(d, transition, candidateSet, candidatePod, candidatePVCs); matchErr == nil {
				t.Fatal("background mutation after the intent-tagged Secret was accepted")
			}
		})
	}
}

func TestOracleFreePublicFinalResumeRejectsObservationAndResourceDrift(t *testing.T) {
	d := oraclePublicEndpointUnitFixture()
	member := database.PublicEndpointTransitionMember{Name: "database-0", UID: "replacement-pod"}
	topology := fmt.Sprintf("%x", sha256.Sum256([]byte(member.Name+":"+member.UID)))
	observed := d.Observation
	observed.ObservedAt = time.Now().UTC()
	observed.Primary = member.Name
	observed.Members = []database.Member{{Name: member.Name, UID: member.UID, Role: "primary", Ready: true}}
	observed.TopologyFingerprint = topology
	observed.TLS = &database.TLSObservation{Verified: true, PlaintextRejected: true, Fingerprint: "replacement-leaf", CAFingerprint: "replacement-ca"}
	set := oraclePublicEndpointStatefulSet(d)
	set.Generation = 8
	set.Status.ObservedGeneration = 8
	templateHash, err := oraclePublicEndpointTemplateHash(set.Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(member.UID)}}
	pvcs := []database.PublicEndpointTransitionPVC{{Name: "data-database-0", UID: "data-uid", VolumeName: "data-volume", PersistentVolumeUID: "data-pv-uid", BackingVolumeFingerprint: "data-backing"}, {Name: "backup-database-0", UID: "backup-uid", VolumeName: "backup-volume", PersistentVolumeUID: "backup-pv-uid", BackingVolumeFingerprint: "backup-backing"}}
	transition := database.PublicEndpointIdentityTransition{
		StatefulSetUID: string(set.UID), TargetGeneration: set.Generation, TargetTemplateHash: templateHash,
		OldMember: database.PublicEndpointTransitionMember{Name: "database-0", UID: "old-pod"}, PVCs: pvcs,
		ProposedLeafFingerprint: observed.TLS.Fingerprint, ProposedCAFingerprint: observed.TLS.CAFingerprint,
		FinalMember: &member, FinalTopologyFingerprint: topology, ServedLeafFingerprint: observed.TLS.Fingerprint, ServedCAFingerprint: observed.TLS.CAFingerprint,
	}
	if err = oraclePublicEndpointReplacementObservationMatches(d, observed, transition); err != nil {
		t.Fatal("valid final Oracle observation was rejected", err)
	}
	if err = oraclePublicEndpointReplacementResourcesMatch(transition, set, pod, pvcs); err != nil {
		t.Fatal("valid final Oracle resources were rejected", err)
	}

	observationTests := map[string]func(*database.Observation){
		"stale observation":        func(candidate *database.Observation) { candidate.ObservedAt = time.Time{} },
		"TLS verification lost":    func(candidate *database.Observation) { candidate.TLS.Verified = false },
		"plaintext rejection lost": func(candidate *database.Observation) { candidate.TLS.PlaintextRejected = false },
		"member changed": func(candidate *database.Observation) {
			candidate.Members[0].UID = "other-pod"
			candidate.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(candidate.Primary+":"+candidate.Members[0].UID)))
		},
	}
	for name, change := range observationTests {
		t.Run(name, func(t *testing.T) {
			candidate := observed
			candidate.Members = append([]database.Member(nil), observed.Members...)
			tls := *observed.TLS
			candidate.TLS = &tls
			change(&candidate)
			if matchErr := oraclePublicEndpointReplacementObservationMatches(d, candidate, transition); matchErr == nil {
				t.Fatal("changed final Oracle observation was accepted")
			}
		})
	}

	resourceTests := map[string]func(*appsv1.StatefulSet, *corev1.Pod, []database.PublicEndpointTransitionPVC){
		"StatefulSet changed": func(candidate *appsv1.StatefulSet, _ *corev1.Pod, _ []database.PublicEndpointTransitionPVC) {
			candidate.UID = "other-statefulset"
		},
		"template changed": func(candidate *appsv1.StatefulSet, _ *corev1.Pod, _ []database.PublicEndpointTransitionPVC) {
			candidate.Spec.Template.Annotations = map[string]string{"changed": "true"}
		},
		"pod changed": func(_ *appsv1.StatefulSet, candidate *corev1.Pod, _ []database.PublicEndpointTransitionPVC) {
			candidate.UID = "other-pod"
		},
		"PVC changed": func(_ *appsv1.StatefulSet, _ *corev1.Pod, candidate []database.PublicEndpointTransitionPVC) {
			candidate[0].UID = "other-pvc"
		},
		"PV changed": func(_ *appsv1.StatefulSet, _ *corev1.Pod, candidate []database.PublicEndpointTransitionPVC) {
			candidate[0].PersistentVolumeUID = "other-pv"
		},
	}
	for name, change := range resourceTests {
		t.Run(name, func(t *testing.T) {
			candidateSet := set.DeepCopy()
			candidatePod := pod.DeepCopy()
			candidatePVCs := append([]database.PublicEndpointTransitionPVC(nil), pvcs...)
			change(candidateSet, candidatePod, candidatePVCs)
			if matchErr := oraclePublicEndpointReplacementResourcesMatch(transition, candidateSet, candidatePod, candidatePVCs); matchErr == nil {
				t.Fatal("changed final Oracle resources were accepted")
			}
		})
	}
}
