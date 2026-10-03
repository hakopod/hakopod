package managedplatform

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func neonTestControlPlaneCAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Neon control-plane test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestNeonComputeAuthenticationIsImmutableAndTLSOnly(t *testing.T) {
	spec := neonCandidateSpec()
	spec.Neon.ComputeReplicas = 2
	images := map[string]string{}
	identities := map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 10001, GID: 10001}
	}
	manifests, err := RenderNeon(NeonRenderInput{Spec: spec, PreviousSpec: &spec, PlatformID: strings.Repeat("a", 32), Revision: 7, NamespaceUID: types.UID("namespace-uid"), Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 10001, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: neonTestControlPlaneCAPEM(t), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}})
	if err != nil {
		t.Fatal(err)
	}
	configs := map[string]*corev1.ConfigMap{}
	computes := map[string]*appsv1.StatefulSet{}
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *corev1.ConfigMap:
			configs[value.Name] = value
		case *appsv1.StatefulSet:
			if strings.HasPrefix(value.Name, "neon-compute-") {
				computes[value.Name] = value
			}
		}
	}
	if len(computes) != 2 {
		t.Fatal("writer and replica must both have an authentication policy")
	}
	for name, compute := range computes {
		config := configs[name+"-tls-r7"]
		if config == nil || config.Immutable == nil || !*config.Immutable || len(config.OwnerReferences) != 1 || config.OwnerReferences[0].UID != types.UID("namespace-uid") {
			t.Fatal("compute authentication policy is not immutable and owned")
		}
		policy := config.Data["pg_hba.conf"]
		for _, address := range []string{"0.0.0.0/0", "::/0"} {
			refusal := strings.Index(policy, "hostnossl all all "+address+" reject\n")
			secure := strings.Index(policy, "hostssl all all "+address+" scram-sha-256\n")
			if refusal < 0 || secure <= refusal {
				t.Fatal("compute must reject plaintext before allowing authenticated TLS")
			}
		}
		for _, line := range strings.Split(policy, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[len(fields)-1] == "trust" && line != "local all cloud_admin trust" && line != "host all cloud_admin 127.0.0.1/32 trust" && line != "host all cloud_admin ::1/128 trust" {
				t.Fatal("compute authentication trusts a non-local administrator connection")
			}
		}
		mounted := false
		for _, mount := range compute.Spec.Template.Spec.Containers[0].VolumeMounts {
			if mount.Name == "compute-postgres-config" {
				mounted = mount.ReadOnly && mount.MountPath == "/etc/hakopod-postgres" && mount.SubPath == ""
			}
		}
		bound := false
		for _, volume := range compute.Spec.Template.Spec.Volumes {
			if volume.Name == "compute-postgres-config" && volume.ConfigMap != nil {
				bound = volume.ConfigMap.Name == config.Name && reflect.DeepEqual(volume.ConfigMap.Items, []corev1.KeyToPath{{Key: "pg_hba.conf", Path: "pg_hba.conf"}})
			}
		}
		if !mounted || !bound {
			t.Fatal("compute must mount its exact revision's authentication policy read-only")
		}
	}
}

func TestRenderNeonIncludesCompletePinnedStack(t *testing.T) {
	spec := neonCandidateSpec()
	spec.Placement.NodeNames = []string{"node-a", "node-b", "node-c"}
	images := map[string]string{}
	identities := map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 10001, GID: 10001}
	}
	manifests, err := RenderNeon(NeonRenderInput{Spec: spec, PlatformID: strings.Repeat("a", 32), Revision: 1, NamespaceUID: types.UID("namespace-uid"), Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 10001, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: neonTestControlPlaneCAPEM(t), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}})
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"neon-broker": false, "neon-controller-database": false, "neon-storage-controller": false, "neon-pageserver-0": false, "neon-pageserver-1": false, "neon-safekeeper-0": false, "neon-safekeeper-1": false, "neon-safekeeper-2": false, "neon-compute-0": false, "neon-proxy": false}
	services := map[string]*corev1.Service{}
	policies := map[string]*networkingv1.NetworkPolicy{}
	ownershipModes := map[string]bool{"pageserver": false, "storage-controller": false, "safekeeper": false}
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *appsv1.Deployment:
			assertNeonLinuxAMD64Scheduling(t, value.Name, value.Spec.Template.Spec)
			if value.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || value.Spec.Strategy.RollingUpdate != nil {
				t.Fatalf("managed platform deployment %s can overlap old and new pods", value.Name)
			}
			if _, ok := wanted[value.Name]; ok {
				wanted[value.Name] = true
			}
			if value.Name == "neon-proxy" && !strings.Contains(strings.Join(value.Spec.Template.Spec.Containers[0].Args, " "), "--auth-endpoint=https://control.example.test/api/v1/internal/neon/proxy") {
				t.Fatal("proxy does not use the trusted server control-plane origin")
			}
			if value.Name == "neon-storage-controller" && slices.Contains(value.Spec.Template.Spec.Containers[0].Args, "--hakopod-ownership-v1") {
				ownershipModes["storage-controller"] = true
			}
			if value.Spec.Template.Spec.NodeName != "node-a" {
				t.Fatalf("deployment %s does not use its exact trusted Node name", value.Name)
			}
		case *appsv1.StatefulSet:
			assertNeonLinuxAMD64Scheduling(t, value.Name, value.Spec.Template.Spec)
			if value.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
				t.Fatalf("managed platform StatefulSet %s does not replace one stable ordinal at a time", value.Name)
			}
			if _, ok := wanted[value.Name]; ok {
				wanted[value.Name] = true
			}
			if value.Name == "neon-compute-0" {
				assertNeonComputeOwnershipStorage(t, value.Spec.Template.Spec, identities["compute"])
			}
			if value.Name == "neon-pageserver-1" && value.Spec.Template.Spec.NodeName != "node-b" {
				t.Fatal("Neon storage placement treated a Node name as an unrelated hostname label")
			}
			if strings.HasPrefix(value.Name, "neon-safekeeper-") && slices.Contains(value.Spec.Template.Spec.Containers[0].Args, "--hakopod-ownership-v1") {
				ownershipModes["safekeeper"] = true
			}
		case *corev1.ConfigMap:
			if strings.HasPrefix(value.Name, "neon-pageserver-") && strings.Contains(value.Data["pageserver.toml"], "hakopod_ownership_v1=true") {
				ownershipModes["pageserver"] = true
			}
		case *corev1.Service:
			services[value.Name] = value
			publish := strings.HasPrefix(value.Name, "neon-pageserver-") || strings.HasPrefix(value.Name, "neon-safekeeper-") || strings.HasSuffix(value.Name, "-control")
			if value.Spec.PublishNotReadyAddresses != publish {
				t.Fatalf("service %s has unsafe bootstrap publication %t", value.Name, value.Spec.PublishNotReadyAddresses)
			}
		case *networkingv1.NetworkPolicy:
			policies[value.Name] = value
		}
	}
	dataService, controlService := services["neon-compute-0"], services["neon-compute-0-control"]
	if dataService == nil || controlService == nil || dataService.Spec.Type != corev1.ServiceTypeClusterIP || controlService.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatal("compute data and bootstrap control services must remain cluster-local")
	}
	if dataService.Spec.PublishNotReadyAddresses || len(dataService.Spec.Ports) != 1 || dataService.Spec.Ports[0].Port != 55433 {
		t.Fatal("compute data service bypasses serving readiness or exposes a control port")
	}
	if !controlService.Spec.PublishNotReadyAddresses || len(controlService.Spec.Ports) != 1 || controlService.Spec.Ports[0].Port != 3081 {
		t.Fatal("compute bootstrap control service is not isolated from the data service")
	}
	proxyPolicy := policies["neon-proxy-ingress"]
	if proxyPolicy == nil || len(proxyPolicy.Spec.Ingress) != 1 || len(proxyPolicy.Spec.Ingress[0].Ports) != 1 || proxyPolicy.Spec.Ingress[0].Ports[0].Port == nil || proxyPolicy.Spec.Ingress[0].Ports[0].Port.IntVal != 5432 {
		t.Fatal("external ingress policy exposes a Neon control port")
	}
	basePolicy := policies["neon-default-deny-and-internal"]
	if basePolicy == nil || len(basePolicy.Spec.Ingress) != 0 || len(basePolicy.Spec.Egress) != 1 || len(basePolicy.Spec.Egress[0].Ports) != 2 {
		t.Fatal("base policy does not default deny internal traffic while preserving DNS")
	}
	controlPorts := map[string]int32{"neon-storage-controller-control-ingress": 6699, "neon-compute-control-ingress": 3081, "neon-safekeeper-control-ingress": 7676}
	for name, wantPort := range controlPorts {
		policy := policies[name]
		if policy == nil || len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 || len(policy.Spec.Ingress[0].Ports) != 1 {
			t.Fatalf("missing bounded control ingress policy %s", name)
		}
		peer := policy.Spec.Ingress[0].From[0]
		port := policy.Spec.Ingress[0].Ports[0].Port
		if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "hakopod-system" || peer.PodSelector == nil || peer.PodSelector.MatchLabels["app.kubernetes.io/name"] != "hakopod-server" || port == nil || port.IntVal != wantPort {
			t.Fatalf("control ingress policy %s is not pinned to the operator identity and exact port", name)
		}
	}
	proxyControl := policies["neon-proxy-control-plane-egress"]
	if proxyControl == nil || len(proxyControl.Spec.Egress) != 1 || len(proxyControl.Spec.Egress[0].To) != 1 || proxyControl.Spec.Egress[0].To[0].NamespaceSelector == nil || proxyControl.Spec.Egress[0].To[0].PodSelector == nil || len(proxyControl.Spec.Egress[0].Ports) != 1 || proxyControl.Spec.Egress[0].Ports[0].Port == nil || proxyControl.Spec.Egress[0].Ports[0].Port.IntVal != 443 {
		t.Fatal("proxy cannot reach the exact server-owned control plane over HTTPS")
	}
	proxyCompute := policies["neon-proxy-compute-egress"]
	if proxyCompute == nil || len(proxyCompute.Spec.Egress) != 1 || len(proxyCompute.Spec.Egress[0].To) != 1 || proxyCompute.Spec.Egress[0].To[0].PodSelector == nil || proxyCompute.Spec.Egress[0].To[0].PodSelector.MatchLabels["hakopod.io/neon-role"] != "compute" || len(proxyCompute.Spec.Egress[0].Ports) != 1 || proxyCompute.Spec.Egress[0].Ports[0].Port == nil || proxyCompute.Spec.Egress[0].Ports[0].Port.IntVal != 55433 {
		t.Fatal("proxy compute egress is not limited to PostgreSQL")
	}
	for _, policy := range policies {
		selector := policy.Spec.PodSelector.MatchLabels
		if len(selector) != 0 && selector["hakopod.io/neon-role"] != "proxy" && selector["app.kubernetes.io/component"] != "proxy" {
			continue
		}
		for _, rule := range policy.Spec.Egress {
			for _, port := range rule.Ports {
				if port.Port == nil {
					t.Fatal("proxy egress contains an unbounded port")
				}
				switch port.Port.IntVal {
				case 53, 443, 55433:
				default:
					t.Fatalf("proxy can reach forbidden internal port %d through %s", port.Port.IntVal, policy.Name)
				}
			}
		}
	}
	external := policies["neon-approved-external-https"]
	if external == nil || len(external.Spec.PodSelector.MatchExpressions) != 1 || external.Spec.PodSelector.MatchExpressions[0].Key != "hakopod.io/neon-role" || external.Spec.PodSelector.MatchExpressions[0].Operator != metav1.LabelSelectorOpIn || !reflect.DeepEqual(external.Spec.PodSelector.MatchExpressions[0].Values, []string{"pageserver", "safekeeper"}) || len(external.Spec.Egress) != 1 || len(external.Spec.Egress[0].To) != 1 || external.Spec.Egress[0].To[0].IPBlock == nil || external.Spec.Egress[0].To[0].IPBlock.CIDR != "8.8.8.8/32" || len(external.Spec.Egress[0].Ports) != 1 || external.Spec.Egress[0].Ports[0].Port == nil || external.Spec.Egress[0].Ports[0].Port.IntVal != 443 {
		t.Fatal("object-storage egress is not limited to pageserver and safekeeper pods over approved HTTPS CIDRs")
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("missing native Neon workload %s", name)
		}
	}
	for component, enabled := range ownershipModes {
		if !enabled {
			t.Fatalf("Neon %s ownership strict mode is disabled", component)
		}
	}
	if len(manifests.Objects) > maxNeonRenderedObjects {
		t.Fatal("Neon manifest inventory exceeded its bound")
	}
	if !manifests.TLSRequired || len(manifests.RequiredSecrets) != len(NeonSecretKeys())+1 || !slices.Contains(manifests.RequiredSecrets, NeonControllerCallbackSecretName(1)) {
		t.Fatal("Neon renderer omitted TLS or immutable secret snapshots")
	}
}

func TestRenderNeonPreservesStorageIdentityAndNonRootSecurity(t *testing.T) {
	spec := neonCandidateSpec()
	spec.Placement.NodeNames = []string{"node-a", "node-b", "node-c"}
	images, identities := map[string]string{}, map[string]NeonRuntimeIdentity{}
	for _, name := range NeonComponents() {
		images[name] = "registry.example.test/neon/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = NeonRuntimeIdentity{UID: 10001, GID: 10001}
	}
	input := NeonRenderInput{Spec: spec, PlatformID: strings.Repeat("b", 32), Revision: 1, NamespaceUID: types.UID("namespace-uid"), Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 10001, ProxyControlPlaneOrigin: "https://control.example.test", ProxyControlPlaneCAPEM: neonTestControlPlaneCAPEM(t), ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}}
	manifests, err := RenderNeon(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *appsv1.Deployment:
			assertNeonPodSecurity(t, value.Spec.Template.Spec)
		case *appsv1.StatefulSet:
			assertNeonPodSecurity(t, value.Spec.Template.Spec)
		case *corev1.PersistentVolumeClaim:
			if value.Spec.StorageClassName == nil || *value.Spec.StorageClassName != "encrypted" {
				t.Fatal("Neon PVC is not bound to the approved encrypted StorageClass")
			}
		}
	}
	previous := spec
	previous.Storage = map[string]int64{}
	for key, value := range spec.Storage {
		previous.Storage[key] = value
	}
	input.Spec.Storage = map[string]int64{}
	for key, value := range spec.Storage {
		input.Spec.Storage[key] = value
	}
	input.Revision = 2
	input.PreviousSpec = &previous
	input.Spec.Storage["pageserver"]--
	if _, err = RenderNeon(input); err == nil {
		t.Fatal("Neon renderer accepted in-place storage shrink")
	}
}

func TestValidateNeonNetworkTrustFailsClosed(t *testing.T) {
	labels := map[string]string{"app.kubernetes.io/name": "hakopod-server"}
	if err := ValidateNeonNetworkTrust("", labels, []string{"8.8.8.8/32"}); err == nil {
		t.Fatal("empty control-plane namespace was accepted")
	}
	if err := ValidateNeonNetworkTrust("hakopod-system", map[string]string{}, []string{"8.8.8.8/32"}); err == nil {
		t.Fatal("empty control-plane pod selector was accepted")
	}
	if err := ValidateNeonNetworkTrust("hakopod-system", labels, []string{"0.0.0.0/0"}); err == nil {
		t.Fatal("unbounded external HTTPS egress was accepted")
	}
}

func assertNeonPodSecurity(t *testing.T, pod corev1.PodSpec) {
	t.Helper()
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || len(pod.Containers) < 1 || len(pod.Containers) > 2 {
		t.Fatal("Neon pod received cluster credentials or invalid container inventory")
	}
	containers := append(append([]corev1.Container(nil), pod.InitContainers...), pod.Containers...)
	for _, container := range containers {
		security := container.SecurityContext
		if security == nil || security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation {
			t.Fatal("Neon pod security contract is incomplete")
		}
	}
}

func assertNeonLinuxAMD64Scheduling(t *testing.T, name string, pod corev1.PodSpec) {
	t.Helper()
	want := map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64"}
	if !reflect.DeepEqual(pod.NodeSelector, want) {
		t.Fatalf("Neon workload %s is not restricted to Linux AMD64 nodes: %#v", name, pod.NodeSelector)
	}
}

func assertNeonComputeOwnershipStorage(t *testing.T, pod corev1.PodSpec, identity NeonRuntimeIdentity) {
	t.Helper()
	if pod.SecurityContext == nil || pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != identity.GID || !reflect.DeepEqual(pod.SecurityContext.SupplementalGroups, []int64{identity.GID}) {
		t.Fatal("compute cache does not use the compute-exclusive storage group")
	}
	if len(pod.Containers) != 2 || pod.Containers[1].Name != "compute-tls" || !strings.Contains(strings.Join(pod.Containers[1].Args, " "), "haproxy") {
		t.Fatal("compute management TLS sidecar is missing")
	}
	for _, mount := range pod.Containers[1].VolumeMounts {
		if mount.Name == "cache" || mount.MountPath == "/var/db/postgres" {
			t.Fatal("compute management TLS sidecar received the compute PVC")
		}
	}
	compute := pod.Containers[0]
	wantArgs := []string{"--pgdata=/var/db/postgres/compute", "--connstr=postgresql://cloud_admin@127.0.0.1:55433/postgres", "--compute-id=compute-0", "--external-http-port=3080", "--config=/var/run/secrets/hakopod/compute-auth/config.json", "--ownership-state-path=/var/db/postgres/hakopod-ownership/record.json"}
	if !reflect.DeepEqual(compute.Command, []string{"compute_ctl"}) || !reflect.DeepEqual(compute.Args, wantArgs) {
		t.Fatalf("compute executable vector changed or ownership state path is unsafe: %#v %#v", compute.Command, compute.Args)
	}
	mounted := false
	for _, mount := range compute.VolumeMounts {
		if mount.Name == "cache" && mount.MountPath == "/var/db/postgres" && !mount.ReadOnly {
			mounted = true
		}
	}
	if !mounted {
		t.Fatal("compute ownership state parent is not on the writable compute PVC")
	}
	if len(pod.InitContainers) != 1 {
		t.Fatalf("compute requires exactly one ownership-directory initializer, got %d", len(pod.InitContainers))
	}
	init := pod.InitContainers[0]
	if init.Name != "prepare-compute-ownership" || init.Image != compute.Image || !reflect.DeepEqual(init.Command, []string{"/bin/sh", "-ec"}) || !reflect.DeepEqual(init.Args, []string{neonComputeOwnershipSetupScript, "prepare-compute-ownership", neonComputeOwnershipDirectory, "10001:10001:700"}) {
		t.Fatal("compute ownership initializer does not use the pinned compute image, path, identity, and fixed shell vector")
	}
	script := init.Args[0]
	for _, required := range []string{"ownership_dir=$1", "expected=$2", "[ -L \"$ownership_dir\" ]", "[ -e \"$ownership_dir\" ]", "[ ! -d \"$ownership_dir\" ]", "umask 077", "mkdir \"$ownership_dir\"", "stat -c '%u:%g:%a'"} {
		if !strings.Contains(script, required) {
			t.Fatalf("ownership initializer omitted fail-closed check %q", required)
		}
	}
	for _, forbidden := range []string{"chmod ", "chown ", "compute-auth", "record.json"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("ownership initializer may adopt state or access credentials through %q", forbidden)
		}
	}
	if len(init.Env) != 0 || len(init.EnvFrom) != 0 || len(init.VolumeMounts) != 1 || init.VolumeMounts[0].Name != "cache" || init.VolumeMounts[0].MountPath != "/var/db/postgres" {
		t.Fatal("ownership initializer is not isolated to the compute PVC")
	}
	security := init.SecurityContext
	if security == nil || security.RunAsUser == nil || *security.RunAsUser != identity.UID || security.RunAsGroup == nil || *security.RunAsGroup != identity.GID || security.RunAsNonRoot == nil || !*security.RunAsNonRoot || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem {
		t.Fatal("ownership initializer does not run as the compute identity with a read-only root filesystem")
	}
}

func TestNeonComputeOwnershipSetupFailsClosed(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "ownership")
	identity := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()) + ":700"
	run := func(expected string) error {
		command := exec.Command("/bin/sh", "-ec", neonComputeOwnershipSetupScript, "prepare-compute-ownership", directory, expected)
		return command.Run()
	}
	if err := run(identity); err != nil {
		t.Fatalf("ownership initializer did not create a private directory: %v", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("ownership initializer created unsafe directory metadata: %v %v", info, err)
	}
	if err = os.Chmod(directory, 0750); err != nil {
		t.Fatal(err)
	}
	if err = run(identity); err == nil {
		t.Fatal("ownership initializer adopted an existing directory with the wrong mode")
	}
	info, err = os.Lstat(directory)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatal("ownership initializer changed the unsafe existing mode")
	}
	if err = os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(directory, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = run(identity); err == nil {
		t.Fatal("ownership initializer accepted a non-directory path")
	}
	if err = os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, directory); err != nil {
		t.Fatal(err)
	}
	if err = run(identity); err == nil {
		t.Fatal("ownership initializer followed an existing symlink")
	}
	if err = os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	wrongIdentity := strconv.Itoa(os.Getuid()+1) + ":" + strconv.Itoa(os.Getgid()) + ":700"
	if err = run(wrongIdentity); err == nil {
		t.Fatal("ownership initializer accepted a directory owned by another compute identity")
	}
}
