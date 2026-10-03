package managedplatform

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

const maxNeonRenderedObjects = 105

const (
	maxNeonControlPlaneCABundleBytes = 64 << 10
	maxNeonControlPlaneCACerts       = 16
)

const (
	neonComputeOwnershipDirectory   = "/var/db/postgres/hakopod-ownership"
	neonComputeOwnershipStatePath   = neonComputeOwnershipDirectory + "/record.json"
	neonComputeOwnershipSetupScript = "ownership_dir=$1\n" +
		"expected=$2\n" +
		"if [ -L \"$ownership_dir\" ]; then echo 'Neon compute ownership directory must not be a symlink' >&2; exit 1; fi\n" +
		"if [ -e \"$ownership_dir\" ]; then\n" +
		"  if [ ! -d \"$ownership_dir\" ]; then echo 'Neon compute ownership path must be a directory' >&2; exit 1; fi\n" +
		"else\n" +
		"  umask 077\n" +
		"  mkdir \"$ownership_dir\"\n" +
		"fi\n" +
		"if [ -L \"$ownership_dir\" ]; then echo 'Neon compute ownership directory became a symlink' >&2; exit 1; fi\n" +
		"actual=$(stat -c '%u:%g:%a' \"$ownership_dir\")\n" +
		"if [ \"$actual\" != \"$expected\" ]; then echo 'Neon compute ownership directory has unsafe ownership or mode' >&2; exit 1; fi"
)

type NeonRuntimeIdentity struct {
	UID int64 `json:"uid" toml:"uid"`
	GID int64 `json:"gid" toml:"gid"`
}

type NeonRenderInput struct {
	Spec                          Spec
	PlatformID                    string
	Revision                      int64
	NamespaceUID                  types.UID
	Images                        map[string]string
	Identities                    map[string]NeonRuntimeIdentity
	ApprovedEncryptedStorageClass string
	SharedStorageGID              int64
	ProxyControlPlaneOrigin       string
	ProxyControlPlaneCAPEM        string
	ControlPlaneNamespace         string
	ControlPlanePodLabels         map[string]string
	ApprovedExternalHTTPSCIDRs    []string
	PreviousSpec                  *Spec
	// RecoveryStoragePrefix comes from the durable recovery binding, not the
	// public spec. It must stay beneath this platform's accepted object prefix.
	RecoveryStoragePrefix string
}

type NeonManifests struct {
	Namespace                     corev1.Namespace
	ExpectedUID                   types.UID
	Objects                       []runtime.Object
	RequiredSecrets               []string
	TLSRequired                   bool
	PruneConfigMapsBeforeRevision int64
	RetainSecretSnapshots         []string
}

func RenderNeon(in NeonRenderInput) (NeonManifests, error) {
	plan, err := PlanNeon(in.Spec, in.Images)
	if err != nil {
		return NeonManifests{}, err
	}
	if in.RecoveryStoragePrefix != "" {
		in.RecoveryStoragePrefix = strings.TrimSuffix(in.RecoveryStoragePrefix, "/")
		prefix := in.Spec.Neon.ObjectStoragePrefix + "/recovery/"
		if !strings.HasPrefix(in.RecoveryStoragePrefix, prefix) || !neonID.MatchString(strings.TrimPrefix(in.RecoveryStoragePrefix, prefix)) {
			return NeonManifests{}, fmt.Errorf("Neon recovery storage must use an operation beneath the accepted object prefix")
		}
	}
	if in.Revision < 1 || in.NamespaceUID == "" || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(in.PlatformID) {
		return NeonManifests{}, fmt.Errorf("Neon rendering requires a positive revision and observed namespace UID")
	}
	if err = exactKeys(in.Identities, neonComponents, "Neon runtime identities"); err != nil {
		return NeonManifests{}, err
	}
	for _, name := range neonComponents {
		if in.Identities[name].UID < 1 || in.Identities[name].GID < 1 {
			return NeonManifests{}, fmt.Errorf("Neon runtime identity %s must use a qualified non-root UID and GID", name)
		}
	}
	if in.ApprovedEncryptedStorageClass == "" || len(utilvalidation.IsDNS1123Subdomain(in.ApprovedEncryptedStorageClass)) != 0 {
		return NeonManifests{}, fmt.Errorf("Neon rendering requires an approved encrypted StorageClass")
	}
	if in.SharedStorageGID < 1 {
		return NeonManifests{}, fmt.Errorf("Neon rendering requires a qualified non-root shared storage GID")
	}
	if err = ValidateHTTPSOrigin(in.ProxyControlPlaneOrigin, "Neon proxy control-plane origin"); err != nil {
		return NeonManifests{}, err
	}
	if err = ValidateNeonControlPlaneCABundle(in.ProxyControlPlaneCAPEM); err != nil {
		return NeonManifests{}, err
	}
	if err = ValidateNeonNetworkTrust(in.ControlPlaneNamespace, in.ControlPlanePodLabels, in.ApprovedExternalHTTPSCIDRs); err != nil {
		return NeonManifests{}, err
	}
	if err = validateNeonRevision(in); err != nil {
		return NeonManifests{}, err
	}

	plan.Namespace = "managed-platform-" + in.PlatformID
	labels := map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform": in.Spec.Name, "hakopod.io/managed-platform-id": in.PlatformID, "hakopod.io/platform-kind": "neon", "hakopod.io/revision": strconv.FormatInt(in.Revision, 10)}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: plan.Namespace, Labels: neonCopyStrings(labels)}}
	owner := metav1.OwnerReference{APIVersion: "v1", Kind: "Namespace", Name: plan.Namespace, UID: in.NamespaceUID}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: plan.Namespace, Labels: neonCopyStrings(labels), OwnerReferences: []metav1.OwnerReference{owner}}
	}
	objects := []runtime.Object{neonProxyControlPlaneCAConfigMap(meta, in)}

	objects = append(objects, neonPVC(meta, in, "controller-database", "controller-database"))
	objects = append(objects, neonControllerDatabaseConfigMap(meta, in))
	for i := 0; i < in.Spec.Neon.Pageservers; i++ {
		name := "pageserver-" + strconv.Itoa(i)
		objects = append(objects, neonPVC(meta, in, name, "pageserver"), neonConfigMap(meta, in, name, i))
	}
	for i := 0; i < in.Spec.Neon.Safekeepers; i++ {
		objects = append(objects, neonPVC(meta, in, "safekeeper-"+strconv.Itoa(i), "safekeeper"))
	}
	for i := 0; i < neonComputeCount(in.Spec); i++ {
		objects = append(objects, neonPVC(meta, in, "compute-cache-"+strconv.Itoa(i), "compute-cache"), neonComputeTLSConfigMap(meta, in, i))
	}

	for _, component := range plan.Components {
		// compute-tls is a sidecar in each compute pod so that compute_ctl remains
		// bound to loopback while all management traffic crosses authenticated TLS.
		if component.Name == "compute-tls" {
			continue
		}
		count := component.Replicas
		if component.Name == "compute" {
			count = neonComputeCount(in.Spec)
		}
		if component.Name == "pageserver" || component.Name == "safekeeper" || component.Name == "compute" {
			for i := 0; i < count; i++ {
				name := component.Name + "-" + strconv.Itoa(i)
				pod := neonPod(in, component, labels, i)
				objects = append(objects, &appsv1.StatefulSet{ObjectMeta: meta("neon-" + name), Spec: appsv1.StatefulSetSpec{ServiceName: "neon-" + name, Replicas: neonInt32(1), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: neonSelector(in.Spec.Name, name)}, Template: pod}})
				if component.Name == "compute" {
					objects = append(objects, neonService(meta, in.Spec.Name, name, []int32{55433}, false))
					objects = append(objects, neonNamedService(meta, in.Spec.Name, name+"-control", name, []int32{3081}, true))
				} else {
					objects = append(objects, neonService(meta, in.Spec.Name, name, component.Ports, true))
				}
			}
			continue
		}
		pod := neonPod(in, component, labels, 0)
		if component.Name == "controller-database" {
			objects = append(objects, &appsv1.StatefulSet{ObjectMeta: meta("neon-controller-database"), Spec: appsv1.StatefulSetSpec{ServiceName: "neon-controller-database", Replicas: neonInt32(1), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: neonSelector(in.Spec.Name, component.Name)}, Template: pod}})
		} else {
			objects = append(objects, &appsv1.Deployment{ObjectMeta: meta("neon-" + component.Name), Spec: appsv1.DeploymentSpec{Replicas: neonInt32(1), Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: neonSelector(in.Spec.Name, component.Name)}, Template: pod}})
		}
		objects = append(objects, neonService(meta, in.Spec.Name, component.Name, component.Ports, false))
	}
	objects = append(objects, neonPolicies(meta, in.Spec.Name, plan.Components, in.ControlPlaneNamespace, in.ControlPlanePodLabels, in.ApprovedExternalHTTPSCIDRs)...)
	if len(objects) > maxNeonRenderedObjects {
		return NeonManifests{}, fmt.Errorf("Neon manifest inventory exceeds %d objects", maxNeonRenderedObjects)
	}
	sort.Slice(objects, func(i, j int) bool {
		left, right := objects[i].(metav1.Object), objects[j].(metav1.Object)
		if fmt.Sprintf("%T", objects[i]) == fmt.Sprintf("%T", objects[j]) {
			return left.GetName() < right.GetName()
		}
		return fmt.Sprintf("%T", objects[i]) < fmt.Sprintf("%T", objects[j])
	})
	secrets := make([]string, 0, len(in.Spec.Secrets)+1)
	for _, ref := range in.Spec.Secrets {
		secrets = append(secrets, neonSecretName(ref))
	}
	secrets = append(secrets, NeonControllerCallbackSecretName(in.Revision))
	sort.Strings(secrets)
	pruneBefore := in.Revision - 1
	if pruneBefore < 1 {
		pruneBefore = 0
	}
	return NeonManifests{Namespace: ns, ExpectedUID: in.NamespaceUID, Objects: objects, RequiredSecrets: secrets, TLSRequired: true, PruneConfigMapsBeforeRevision: pruneBefore, RetainSecretSnapshots: append([]string(nil), secrets...)}, nil
}

func ValidateNeonControlPlaneCABundle(value string) error {
	if len(value) == 0 || len(value) > maxNeonControlPlaneCABundleBytes {
		return fmt.Errorf("Neon proxy control-plane CA must be a non-empty certificate bundle of at most 64 KiB")
	}
	remaining := bytes.TrimSpace([]byte(value))
	if len(remaining) == 0 {
		return fmt.Errorf("Neon proxy control-plane CA must be a non-empty certificate bundle of at most 64 KiB")
	}
	count := 0
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return fmt.Errorf("Neon proxy control-plane CA must contain certificates only")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return fmt.Errorf("Neon proxy control-plane CA must contain valid certificate PEM blocks only")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
			return fmt.Errorf("Neon proxy control-plane CA bundle contains a certificate that is not a certificate authority")
		}
		count++
		if count > maxNeonControlPlaneCACerts {
			return fmt.Errorf("Neon proxy control-plane CA bundle exceeds 16 certificates")
		}
		remaining = bytes.TrimSpace(rest)
	}
	return nil
}

func neonProxyControlPlaneCAConfigMap(meta func(string) metav1.ObjectMeta, in NeonRenderInput) *corev1.ConfigMap {
	name := "neon-proxy-control-plane-ca-r" + strconv.FormatInt(in.Revision, 10)
	return &corev1.ConfigMap{ObjectMeta: meta(name), Immutable: neonBool(true), Data: map[string]string{"ca.crt": in.ProxyControlPlaneCAPEM}}
}

func NeonControllerCallbackSecretName(revision int64) string {
	return "neon-controller-callback-r" + strconv.FormatInt(revision, 10)
}

// A client receives its peer's public trust and authentication token, never its private key.
func neonPeerSecretProjection(component, secret string) []corev1.KeyToPath {
	keys := []string(nil)
	switch {
	case component == "storage-controller" && secret == "controller-database-password":
		keys = []string{"ca.crt"}
	case component == "storage-controller" && (secret == "pageserver-auth" || secret == "safekeeper-auth"):
		keys = []string{"token", "ca.crt"}
	case component == "pageserver" && secret == "controller-auth":
		keys = []string{"upcall-token", "ca.crt"}
	case component == "pageserver" && secret == "safekeeper-auth":
		keys = []string{"token", "ca.crt"}
	case (component == "pageserver" || component == "safekeeper") && secret == "broker-auth":
		keys = []string{"ca.crt"}
	case component == "compute" && (secret == "pageserver-auth" || secret == "safekeeper-auth"):
		keys = []string{"ca.crt"}
	}
	var items []corev1.KeyToPath
	for _, key := range keys {
		items = append(items, corev1.KeyToPath{Key: key, Path: key})
	}
	return items
}

func neonPreparePeerTrust(container *corev1.Container, component string) {
	var sources []string
	switch component {
	case "pageserver":
		sources = []string{"pageserver-auth", "controller-auth", "safekeeper-auth", "broker-auth"}
	case "safekeeper":
		sources = []string{"safekeeper-auth", "broker-auth"}
	case "storage-controller":
		sources = []string{"controller-auth", "pageserver-auth", "safekeeper-auth"}
	default:
		return
	}
	paths := make([]string, 0, len(sources)+1)
	for _, source := range sources {
		paths = append(paths, "/var/run/secrets/hakopod/"+source+"/ca.crt")
	}
	if component == "storage-controller" {
		paths = append(paths, "/etc/hakopod-control-plane/ca.crt")
	}
	script := "umask 077; cat " + strings.Join(paths, " ") + " > /tmp/neon-peer-ca.crt; exec \"$@\""
	args := append([]string{script, "neon-peer-trust"}, container.Command...)
	args = append(args, container.Args...)
	container.Command = []string{"/bin/sh", "-ec"}
	container.Args = args
}

func validateNeonRevision(in NeonRenderInput) error {
	if in.Revision == 1 {
		if in.PreviousSpec != nil {
			return fmt.Errorf("initial Neon provisioning cannot have a previous spec")
		}
		return nil
	}
	if in.PreviousSpec == nil || in.PreviousSpec.Neon == nil {
		return fmt.Errorf("Neon updates require the complete previous spec")
	}
	return ValidateNeonResourceUpdate(*in.PreviousSpec, in.Spec)
}

func neonComputeCount(spec Spec) int {
	if spec.Neon.ComputeReplicas < 1 {
		return 1
	}
	return spec.Neon.ComputeReplicas
}

func neonPVC(meta func(string) metav1.ObjectMeta, in NeonRenderInput, name, storageKey string) *corev1.PersistentVolumeClaim {
	quantity := *resource.NewQuantity(in.Spec.Storage[storageKey]<<30, resource.BinarySI)
	return &corev1.PersistentVolumeClaim{ObjectMeta: meta("neon-" + name), Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &in.ApprovedEncryptedStorageClass, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: quantity}}}}
}

func neonConfigMap(meta func(string) metav1.ObjectMeta, in NeonRenderInput, name string, ordinal int) *corev1.ConfigMap {
	c := in.Spec.Neon
	remote := fmt.Sprintf("{endpoint='%s',bucket_name='%s',bucket_region='%s',prefix_in_bucket='%s/pageserver'}", c.ObjectStorageURL, c.ObjectStorageBucket, c.ObjectStorageRegion, neonRemoteStoragePrefix(in))
	config := fmt.Sprintf("listen_pg_addr='0.0.0.0:6400'\nlisten_http_addr='127.0.0.1:9897'\nlisten_https_addr='0.0.0.0:9898'\npg_distrib_dir='/usr/local'\nhttp_auth_type='NeonJWT'\npg_auth_type='NeonJWT'\nenable_tls_page_service_api=true\nssl_key_file='/var/run/secrets/hakopod/pageserver-auth/tls.key'\nssl_cert_file='/var/run/secrets/hakopod/pageserver-auth/tls.crt'\nssl_ca_file='/tmp/neon-peer-ca.crt'\nbroker_endpoint='https://neon-broker:50051'\ncontrol_plane_api='https://neon-storage-controller:6699/upcall/v1/'\nauth_validation_public_key_path='/var/run/secrets/hakopod/pageserver-auth/public-key.pem'\nhakopod_ownership_v1=true\nremote_storage=%s\n", remote)
	return &corev1.ConfigMap{ObjectMeta: meta("neon-" + name + "-r" + strconv.FormatInt(in.Revision, 10)), Immutable: neonBool(true), Data: map[string]string{"identity.toml": fmt.Sprintf("id=%d\n", ordinal+1), "pageserver.toml": config}}
}

func neonRemoteStoragePrefix(in NeonRenderInput) string {
	if in.RecoveryStoragePrefix != "" {
		return in.RecoveryStoragePrefix
	}
	return in.Spec.Neon.ObjectStoragePrefix
}

func neonComputeTLSConfigMap(meta func(string) metav1.ObjectMeta, in NeonRenderInput, ordinal int) *corev1.ConfigMap {
	name := "neon-compute-" + strconv.Itoa(ordinal) + "-tls-r" + strconv.FormatInt(in.Revision, 10)
	config := "global\n  maxconn 64\n  ssl-default-bind-options ssl-min-ver TLSv1.2\ndefaults\n  mode http\n  timeout connect 2s\n  timeout client 30s\n  timeout server 30s\nfrontend compute_control\n  bind :3081 ssl crt /tmp/compute-tls.pem\n  default_backend compute_ctl\nbackend compute_ctl\n  server local 127.0.0.1:3080\n"
	// compute_ctl administers PostgreSQL over loopback. Application connections
	// must use TLS and SCRAM, including when they reach the compute directly.
	hba := "local all cloud_admin trust\nlocal all all scram-sha-256\nhost all cloud_admin 127.0.0.1/32 trust\nhost all cloud_admin ::1/128 trust\nhostnossl all all 0.0.0.0/0 reject\nhostnossl all all ::/0 reject\nhostssl all all 0.0.0.0/0 scram-sha-256\nhostssl all all ::/0 scram-sha-256\nhost all all 0.0.0.0/0 reject\nhost all all ::/0 reject\n"
	return &corev1.ConfigMap{ObjectMeta: meta(name), Immutable: neonBool(true), Data: map[string]string{"haproxy.cfg": config, "pg_hba.conf": hba}}
}

func neonControllerDatabaseConfigMap(meta func(string) metav1.ObjectMeta, in NeonRenderInput) *corev1.ConfigMap {
	name := "neon-controller-database-r" + strconv.FormatInt(in.Revision, 10)
	hba := "local all all trust\nhostnossl all all 0.0.0.0/0 reject\nhostnossl all all ::/0 reject\nhostssl storage_controller storage_controller 0.0.0.0/0 scram-sha-256\nhostssl storage_controller storage_controller ::/0 scram-sha-256\nhost all all 0.0.0.0/0 reject\nhost all all ::/0 reject\n"
	return &corev1.ConfigMap{ObjectMeta: meta(name), Immutable: neonBool(true), Data: map[string]string{"pg_hba.conf": hba}}
}

func neonPod(in NeonRenderInput, component Component, labels map[string]string, ordinal int) corev1.PodTemplateSpec {
	logicalName := component.Name
	instanceName := logicalName
	if logicalName == "pageserver" || logicalName == "safekeeper" || logicalName == "compute" {
		instanceName += "-" + strconv.Itoa(ordinal)
	}
	identity := in.Identities[logicalName]
	security := &corev1.SecurityContext{AllowPrivilegeEscalation: neonBool(false), ReadOnlyRootFilesystem: neonBool(true), RunAsNonRoot: neonBool(true), RunAsUser: &identity.UID, RunAsGroup: &identity.GID, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	container := corev1.Container{Name: logicalName, Image: component.Image, ImagePullPolicy: corev1.PullIfNotPresent, SecurityContext: security, Resources: corev1.ResourceRequirements{Requests: neonResourceList(component.Resources), Limits: neonResourceList(component.Resources)}}
	for _, port := range component.Ports {
		container.Ports = append(container.Ports, corev1.ContainerPort{Name: "tcp-" + strconv.Itoa(int(port)), ContainerPort: port})
	}
	container.ReadinessProbe, container.LivenessProbe, container.StartupProbe = neonProbes(logicalName)
	tmpLimit := resource.MustParse("256Mi")
	volumes := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpLimit}}}}
	container.VolumeMounts = []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}
	for _, key := range component.SecretKeys {
		ref := in.Spec.Secrets[key]
		secretSource := &corev1.SecretVolumeSource{SecretName: neonSecretName(ref), DefaultMode: neonInt32(0440)}
		secretSource.Items = neonPeerSecretProjection(logicalName, key)
		volumes = append(volumes, corev1.Volume{Name: key, VolumeSource: corev1.VolumeSource{Secret: secretSource}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: key, MountPath: "/var/run/secrets/hakopod/" + key, ReadOnly: true})
	}
	if logicalName == "pageserver" || logicalName == "safekeeper" || logicalName == "compute" {
		container.Env = append(container.Env, corev1.EnvVar{Name: "NEON_STORAGE_CA_FILE", Value: "/var/run/secrets/hakopod/safekeeper-auth/ca.crt"})
	}
	if logicalName == "controller-database" {
		configName := "neon-controller-database-r" + strconv.FormatInt(in.Revision, 10)
		volumes = append(volumes, neonPVCVolume("data", "neon-controller-database"), corev1.Volume{Name: "controller-database-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: "/var/lib/postgresql/data"}, corev1.VolumeMount{Name: "controller-database-config", MountPath: "/etc/hakopod-postgres", ReadOnly: true})
		container.Command = []string{"/bin/sh", "-ec"}
		container.Args = []string{"umask 077; cp /var/run/secrets/hakopod/controller-database-password/tls.key /tmp/controller-database.key; chmod 0600 /tmp/controller-database.key; exec /usr/local/bin/docker-entrypoint.sh postgres -c hba_file=/etc/hakopod-postgres/pg_hba.conf -c ssl=on -c ssl_cert_file=/var/run/secrets/hakopod/controller-database-password/tls.crt -c ssl_key_file=/tmp/controller-database.key -c ssl_ca_file=/var/run/secrets/hakopod/controller-database-password/ca.crt -c unix_socket_directories=/tmp -c password_encryption=scram-sha-256"}
		container.Env = append(container.Env, neonSecretEnv(in.Spec.Secrets["controller-database-password"], "POSTGRES_PASSWORD", "value"), corev1.EnvVar{Name: "POSTGRES_DB", Value: "storage_controller"}, corev1.EnvVar{Name: "POSTGRES_USER", Value: "storage_controller"}, corev1.EnvVar{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"}, corev1.EnvVar{Name: "PGHOST", Value: "/tmp"})
	} else if logicalName == "broker" {
		container.Command = []string{"storage_broker"}
		container.Args = []string{"--listen-https-addr=0.0.0.0:50051", "--ssl-key-file=/var/run/secrets/hakopod/broker-auth/tls.key", "--ssl-cert-file=/var/run/secrets/hakopod/broker-auth/tls.crt"}
	} else if logicalName == "storage-controller" {
		callbackName := NeonControllerCallbackSecretName(in.Revision)
		callbackPath := "/api/v1/internal/neon/storage-controller/" + in.PlatformID + "/" + strconv.FormatInt(in.Revision, 10)
		configName := "neon-proxy-control-plane-ca-r" + strconv.FormatInt(in.Revision, 10)
		volumes = append(volumes, corev1.Volume{Name: "control-plane-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}, DefaultMode: neonInt32(0444)}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "control-plane-ca", MountPath: "/etc/hakopod-control-plane", ReadOnly: true})
		container.Command = []string{"storage_controller"}
		container.Args = []string{"--listen-https=0.0.0.0:6699", "--ssl-key-file=/var/run/secrets/hakopod/controller-auth/tls.key", "--ssl-cert-file=/var/run/secrets/hakopod/controller-auth/tls.crt", "--ssl-ca-file=/tmp/neon-peer-ca.crt", "--control-plane-url=" + strings.TrimSuffix(in.ProxyControlPlaneOrigin, "/") + callbackPath, "--timelines-onto-safekeepers=true", "--use-https-pageserver-api=true", "--use-https-safekeeper-api=true", "--hakopod-ownership-v1", "--reconciler-concurrency=4", "--priority-reconciler-concurrency=2", "--safekeeper-reconciler-concurrency=2"}
		container.Env = append(container.Env, corev1.EnvVar{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: callbackName}, Key: "database-url"}}}, corev1.EnvVar{Name: "STORCON_DB_CERT_CHECKS", Value: "1"}, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/var/run/secrets/hakopod/controller-database-password/ca.crt"}, neonSecretEnv(in.Spec.Secrets["controller-auth"], "PUBLIC_KEY", "public-key.pem"), neonSecretEnv(in.Spec.Secrets["pageserver-auth"], "PAGESERVER_JWT_TOKEN", "token"), neonSecretEnv(in.Spec.Secrets["safekeeper-auth"], "SAFEKEEPER_JWT_TOKEN", "token"))
		container.Env = append(container.Env, corev1.EnvVar{Name: "CONTROL_PLANE_JWT_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: callbackName}, Key: "token"}}})
	} else if logicalName == "pageserver" {
		configName := "neon-" + instanceName + "-r" + strconv.FormatInt(in.Revision, 10)
		volumes = append(volumes, neonPVCVolume("data", "neon-"+instanceName), corev1.Volume{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: "/var/lib/neon"}, corev1.VolumeMount{Name: "config", MountPath: "/etc/neon-template", ReadOnly: true})
		container.Command = []string{"/bin/sh", "-ec"}
		container.Args = []string{"umask 077; cp /etc/neon-template/identity.toml /var/lib/neon/identity.toml; cp /etc/neon-template/pageserver.toml /var/lib/neon/pageserver.toml; printf \"control_plane_api_token='%s'\\n\" \"$(cat /var/run/secrets/hakopod/controller-auth/upcall-token)\" >> /var/lib/neon/pageserver.toml; exec pageserver --workdir=/var/lib/neon"}
		container.Env = append(container.Env, neonObjectStorageEnv(in.Spec.Secrets["object-storage"])...)
		container.Env = append(container.Env, neonSecretEnv(in.Spec.Secrets["safekeeper-auth"], "NEON_AUTH_TOKEN", "token"))
	} else if logicalName == "safekeeper" {
		volumes = append(volumes, neonPVCVolume("data", "neon-"+instanceName))
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: "/var/lib/neon"})
		remote := fmt.Sprintf("{endpoint='%s',bucket_name='%s',bucket_region='%s',prefix_in_bucket='/%s/safekeeper-%d'}", in.Spec.Neon.ObjectStorageURL, in.Spec.Neon.ObjectStorageBucket, in.Spec.Neon.ObjectStorageRegion, neonRemoteStoragePrefix(in), ordinal)
		container.Command = []string{"safekeeper"}
		container.Args = []string{"--datadir=/var/lib/neon", "--id=" + strconv.Itoa(ordinal+1), "--listen-pg=0.0.0.0:5454", "--advertise-pg=neon-" + instanceName + ":5454", "--listen-http=127.0.0.1:7677", "--listen-https=0.0.0.0:7676", "--ssl-key-file=/var/run/secrets/hakopod/safekeeper-auth/tls.key", "--ssl-cert-file=/var/run/secrets/hakopod/safekeeper-auth/tls.crt", "--ssl-ca-file=/tmp/neon-peer-ca.crt", "--enable-tls-wal-service-api", "--use-https-safekeeper-api", "--broker-endpoint=https://neon-broker:50051", "--remote-storage=" + remote, "--pg-auth-public-key-path=/var/run/secrets/hakopod/safekeeper-auth/public-key.pem", "--http-auth-public-key-path=/var/run/secrets/hakopod/safekeeper-auth/public-key.pem", "--auth-token-path=/var/run/secrets/hakopod/safekeeper-auth/token", "--hakopod-ownership-v1"}
		container.Env = append(container.Env, neonObjectStorageEnv(in.Spec.Secrets["object-storage"])...)
	} else if logicalName == "compute" {
		configName := "neon-compute-" + strconv.Itoa(ordinal) + "-tls-r" + strconv.FormatInt(in.Revision, 10)
		volumes = append(volumes, neonPVCVolume("cache", "neon-compute-cache-"+strconv.Itoa(ordinal)), corev1.Volume{Name: "compute-tls-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}})
		volumes = append(volumes, corev1.Volume{Name: "compute-postgres-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}, Items: []corev1.KeyToPath{{Key: "pg_hba.conf", Path: "pg_hba.conf"}}, DefaultMode: neonInt32(0444)}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "cache", MountPath: "/var/db/postgres"}, corev1.VolumeMount{Name: "compute-postgres-config", MountPath: "/etc/hakopod-postgres", ReadOnly: true})
		container.Command = []string{"compute_ctl"}
		container.Args = []string{"--pgdata=/var/db/postgres/compute", "--connstr=postgresql://cloud_admin@127.0.0.1:55433/postgres", "--compute-id=" + instanceName, "--external-http-port=3080", "--config=/var/run/secrets/hakopod/compute-auth/config.json", "--ownership-state-path=" + neonComputeOwnershipStatePath}
		ownershipIdentity := fmt.Sprintf("%d:%d:700", identity.UID, identity.GID)
		ownershipInit := corev1.Container{Name: "prepare-compute-ownership", Image: component.Image, ImagePullPolicy: corev1.PullIfNotPresent, SecurityContext: security, Resources: corev1.ResourceRequirements{Requests: neonResourceList(component.Resources), Limits: neonResourceList(component.Resources)}, Command: []string{"/bin/sh", "-ec"}, Args: []string{neonComputeOwnershipSetupScript, "prepare-compute-ownership", neonComputeOwnershipDirectory, ownershipIdentity}, VolumeMounts: []corev1.VolumeMount{{Name: "cache", MountPath: "/var/db/postgres"}}}
		proxyIdentity := in.Identities["compute-tls"]
		proxySecurity := &corev1.SecurityContext{AllowPrivilegeEscalation: neonBool(false), ReadOnlyRootFilesystem: neonBool(true), RunAsNonRoot: neonBool(true), RunAsUser: &proxyIdentity.UID, RunAsGroup: &proxyIdentity.GID, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
		proxy := corev1.Container{Name: "compute-tls", Image: in.Images["compute-tls"], ImagePullPolicy: corev1.PullIfNotPresent, SecurityContext: proxySecurity, Resources: corev1.ResourceRequirements{Requests: neonResourceList(in.Spec.Resources["compute-tls"]), Limits: neonResourceList(in.Spec.Resources["compute-tls"])}, Command: []string{"/bin/sh", "-ec"}, Args: []string{"umask 077; cat /var/run/secrets/hakopod/compute-auth/tls.crt /var/run/secrets/hakopod/compute-auth/tls.key > /tmp/compute-tls.pem; exec haproxy -W -db -f /etc/haproxy/haproxy.cfg"}, Ports: []corev1.ContainerPort{{Name: "https-control", ContainerPort: 3081}}, VolumeMounts: []corev1.VolumeMount{{Name: "compute-auth", MountPath: "/var/run/secrets/hakopod/compute-auth", ReadOnly: true}, {Name: "compute-tls-config", MountPath: "/etc/haproxy", ReadOnly: true}, {Name: "tmp", MountPath: "/tmp"}}}
		container.Ports = []corev1.ContainerPort{{Name: "http-control", ContainerPort: 3080}, {Name: "postgres", ContainerPort: 55433}}
		podContainers := []corev1.Container{container, proxy}
		policy := corev1.FSGroupChangeOnRootMismatch
		return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: neonComponentLabels(labels, instanceName, logicalName)}, Spec: corev1.PodSpec{AutomountServiceAccountToken: neonBool(false), EnableServiceLinks: neonBool(false), NodeName: neonNodeName(in.Spec, logicalName, ordinal), NodeSelector: neonPlatformNodeSelector(), TerminationGracePeriodSeconds: neonInt64(30), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: neonBool(true), FSGroup: &identity.GID, SupplementalGroups: []int64{identity.GID}, FSGroupChangePolicy: &policy, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, InitContainers: []corev1.Container{ownershipInit}, Containers: podContainers, Volumes: volumes}}
	} else if logicalName == "proxy" {
		origin := strings.TrimSuffix(in.ProxyControlPlaneOrigin, "/")
		configName := "neon-proxy-control-plane-ca-r" + strconv.FormatInt(in.Revision, 10)
		mode := int32(0444)
		volumes = append(volumes, corev1.Volume{Name: "control-plane-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}, DefaultMode: &mode}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "control-plane-ca", MountPath: "/etc/hakopod-control-plane", ReadOnly: true})
		container.Command = []string{"proxy"}
		container.Args = []string{"--proxy=0.0.0.0:5432", "--http=0.0.0.0:7001", "--mgmt=127.0.0.1:7000", "--tls-key=/var/run/secrets/hakopod/proxy-auth/tls.key", "--tls-cert=/var/run/secrets/hakopod/proxy-auth/tls.crt", "--auth-backend=control-plane", "--auth-endpoint=" + origin + "/api/v1/internal/neon/proxy"}
		container.Env = append(container.Env, neonSecretEnv(in.Spec.Secrets["proxy-auth"], "NEON_PROXY_TO_CONTROLPLANE_TOKEN", "token"), corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/etc/hakopod-control-plane/ca.crt"})
	}
	neonPreparePeerTrust(&container, logicalName)
	policy := corev1.FSGroupChangeOnRootMismatch
	return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: neonComponentLabels(labels, instanceName, logicalName)}, Spec: corev1.PodSpec{AutomountServiceAccountToken: neonBool(false), EnableServiceLinks: neonBool(false), NodeName: neonNodeName(in.Spec, logicalName, ordinal), NodeSelector: neonPlatformNodeSelector(), TerminationGracePeriodSeconds: neonInt64(30), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: neonBool(true), RunAsUser: &identity.UID, RunAsGroup: &identity.GID, FSGroup: &in.SharedStorageGID, SupplementalGroups: []int64{in.SharedStorageGID}, FSGroupChangePolicy: &policy, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{container}, Volumes: volumes}}
}

func neonPlatformNodeSelector() map[string]string {
	return map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64"}
}

func ValidateNeonNetworkTrust(controlNamespace string, controlPodLabels map[string]string, externalHTTPS []string) error {
	if len(utilvalidation.IsDNS1123Label(controlNamespace)) != 0 || len(controlPodLabels) < 1 || len(controlPodLabels) > 8 {
		return fmt.Errorf("Neon network trust requires one control-plane namespace and 1-8 exact pod labels")
	}
	if _, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: controlPodLabels}); err != nil {
		return fmt.Errorf("Neon control-plane pod labels are invalid: %w", err)
	}
	if len(externalHTTPS) < 1 || len(externalHTTPS) > 16 {
		return fmt.Errorf("Neon external HTTPS egress requires 1-16 approved CIDRs")
	}
	seen := map[string]bool{}
	for _, cidr := range externalHTTPS {
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil || network.String() != cidr || !publicEgressIP(ip) {
			return fmt.Errorf("Neon external HTTPS egress CIDR %q must be canonical and public", cidr)
		}
		prefix, bits := network.Mask.Size()
		if bits == 32 && prefix < 24 || bits == 128 && prefix < 64 {
			return fmt.Errorf("Neon external HTTPS egress CIDR %q is too broad", cidr)
		}
		if seen[cidr] {
			return fmt.Errorf("Neon external HTTPS egress CIDR %q is duplicated", cidr)
		}
		seen[cidr] = true
	}
	return nil
}

func neonPolicies(meta func(string) metav1.ObjectMeta, platform string, components []Component, controlNamespace string, controlPodLabels map[string]string, externalHTTPS []string) []runtime.Object {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	dns := intstr.FromInt32(53)
	_ = components
	roleLabels := func(role string) map[string]string {
		return map[string]string{"hakopod.io/managed-platform": platform, "hakopod.io/neon-role": role}
	}
	rolePeer := func(role string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: roleLabels(role)}}
	}
	ports := func(values ...int32) []networkingv1.NetworkPolicyPort {
		result := make([]networkingv1.NetworkPolicyPort, 0, len(values))
		for _, value := range values {
			port := intstr.FromInt32(value)
			result = append(result, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &port})
		}
		return result
	}
	ingress := func(roles []string, values ...int32) networkingv1.NetworkPolicyIngressRule {
		peers := make([]networkingv1.NetworkPolicyPeer, 0, len(roles))
		for _, role := range roles {
			peers = append(peers, rolePeer(role))
		}
		return networkingv1.NetworkPolicyIngressRule{From: peers, Ports: ports(values...)}
	}
	egress := func(roles []string, values ...int32) networkingv1.NetworkPolicyEgressRule {
		peers := make([]networkingv1.NetworkPolicyPeer, 0, len(roles))
		for _, role := range roles {
			peers = append(peers, rolePeer(role))
		}
		return networkingv1.NetworkPolicyEgressRule{To: peers, Ports: ports(values...)}
	}
	internal := func(name, role string, ingressRules []networkingv1.NetworkPolicyIngressRule, egressRules []networkingv1.NetworkPolicyEgressRule) runtime.Object {
		policyTypes := []networkingv1.PolicyType{}
		if ingressRules != nil {
			policyTypes = append(policyTypes, networkingv1.PolicyTypeIngress)
		}
		if egressRules != nil {
			policyTypes = append(policyTypes, networkingv1.PolicyTypeEgress)
		}
		return &networkingv1.NetworkPolicy{ObjectMeta: meta(name), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: roleLabels(role)}, PolicyTypes: policyTypes, Ingress: ingressRules, Egress: egressRules}}
	}
	base := &networkingv1.NetworkPolicy{ObjectMeta: meta("neon-default-deny-and-internal"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &dns}, {Protocol: &udp, Port: &dns}}}}}}
	proxyPort := intstr.FromInt32(5432)
	proxy := &networkingv1.NetworkPolicy{ObjectMeta: meta("neon-proxy-ingress"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: neonSelector(platform, "proxy")}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/managed-ingress": "true"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &proxyPort}}}}}}
	controlPeer := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": controlNamespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: neonCopyStrings(controlPodLabels)}}
	controlPolicy := func(name, role string, portNumber int32) runtime.Object {
		port := intstr.FromInt32(portNumber)
		return &networkingv1.NetworkPolicy{ObjectMeta: meta(name), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/managed-platform": platform, "hakopod.io/neon-role": role}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{controlPeer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}}}}}
	}
	proxyControlPort := intstr.FromInt32(443)
	proxyControl := &networkingv1.NetworkPolicy{ObjectMeta: meta("neon-proxy-control-plane-egress"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: neonSelector(platform, "proxy")}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{controlPeer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &proxyControlPort}}}}}}
	controllerCallback := networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{controlPeer}, Ports: ports(443)}
	objects := []runtime.Object{
		base,
		proxy,
		proxyControl,
		controlPolicy("neon-storage-controller-control-ingress", "storage-controller", 6699),
		controlPolicy("neon-compute-control-ingress", "compute", 3081),
		controlPolicy("neon-pageserver-control-ingress", "pageserver", 9898),
		controlPolicy("neon-safekeeper-control-ingress", "safekeeper", 7676),
		internal("neon-broker-internal", "broker", []networkingv1.NetworkPolicyIngressRule{ingress([]string{"pageserver", "safekeeper"}, 50051)}, nil),
		internal("neon-controller-database-internal", "controller-database", []networkingv1.NetworkPolicyIngressRule{ingress([]string{"storage-controller"}, 5432)}, nil),
		internal("neon-storage-controller-internal", "storage-controller", []networkingv1.NetworkPolicyIngressRule{ingress([]string{"pageserver"}, 6699)}, []networkingv1.NetworkPolicyEgressRule{egress([]string{"controller-database"}, 5432), egress([]string{"pageserver"}, 9898), egress([]string{"safekeeper"}, 7676), controllerCallback}),
		internal("neon-pageserver-internal", "pageserver", []networkingv1.NetworkPolicyIngressRule{ingress([]string{"compute"}, 6400), ingress([]string{"storage-controller"}, 9898)}, []networkingv1.NetworkPolicyEgressRule{egress([]string{"broker"}, 50051), egress([]string{"storage-controller"}, 6699), egress([]string{"safekeeper"}, 5454)}),
		internal("neon-safekeeper-internal", "safekeeper", []networkingv1.NetworkPolicyIngressRule{ingress([]string{"compute", "pageserver", "safekeeper"}, 5454), ingress([]string{"storage-controller", "safekeeper"}, 7676)}, []networkingv1.NetworkPolicyEgressRule{egress([]string{"broker"}, 50051), egress([]string{"safekeeper"}, 5454, 7676)}),
		internal("neon-compute-internal", "compute", []networkingv1.NetworkPolicyIngressRule{ingress([]string{"proxy"}, 55433)}, []networkingv1.NetworkPolicyEgressRule{egress([]string{"pageserver"}, 6400), egress([]string{"safekeeper"}, 5454)}),
		internal("neon-proxy-compute-egress", "proxy", nil, []networkingv1.NetworkPolicyEgressRule{egress([]string{"compute"}, 55433)}),
	}
	https := intstr.FromInt32(443)
	rules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(externalHTTPS))
	for _, cidr := range externalHTTPS {
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &https}}})
	}
	external := &networkingv1.NetworkPolicy{ObjectMeta: meta("neon-approved-external-https"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/managed-platform": platform}, MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "hakopod.io/neon-role", Operator: metav1.LabelSelectorOpIn, Values: []string{"pageserver", "safekeeper"}}}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: rules}}
	return append(objects, external)
}

func neonService(meta func(string) metav1.ObjectMeta, platform, component string, ports []int32, publishNotReady bool) *corev1.Service {
	return neonNamedService(meta, platform, component, component, ports, publishNotReady)
}

func neonNamedService(meta func(string) metav1.ObjectMeta, platform, name, selectorComponent string, ports []int32, publishNotReady bool) *corev1.Service {
	values := make([]corev1.ServicePort, 0, len(ports))
	for _, port := range ports {
		values = append(values, corev1.ServicePort{Name: "tcp-" + strconv.Itoa(int(port)), Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP})
	}
	return &corev1.Service{ObjectMeta: meta("neon-" + name), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, PublishNotReadyAddresses: publishNotReady, Selector: neonSelector(platform, selectorComponent), Ports: values}}
}

func neonProbes(component string) (readiness, liveness, startup *corev1.Probe) {
	port := int32(0)
	path := "/status"
	switch component {
	case "broker":
		port = 50051
	case "controller-database":
		port = 5432
	case "storage-controller":
		port = 6699
		path = "/ready"
	case "pageserver":
		port = 9898
		path = "/v1/status"
	case "safekeeper":
		port = 7676
		path = "/v1/status"
	case "compute":
		port = 3080
	case "proxy":
		port = 7001
		path = "/v1/status"
	}
	handler := corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(port), Scheme: corev1.URISchemeHTTP}}
	if component == "storage-controller" || component == "pageserver" || component == "safekeeper" {
		handler.HTTPGet.Scheme = corev1.URISchemeHTTPS
	}
	if component == "broker" || component == "controller-database" {
		handler = corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}
	}
	readiness = &corev1.Probe{ProbeHandler: handler, TimeoutSeconds: 3, PeriodSeconds: 5, FailureThreshold: 3}
	liveness = &corev1.Probe{ProbeHandler: handler, InitialDelaySeconds: 30, TimeoutSeconds: 3, PeriodSeconds: 10, FailureThreshold: 6}
	startup = liveness.DeepCopy()
	startup.InitialDelaySeconds = 0
	startup.PeriodSeconds = 5
	startup.FailureThreshold = 60
	return
}

func neonNodeName(spec Spec, component string, ordinal int) string {
	nodes := spec.Placement.NodeNames
	if len(nodes) == 0 {
		return ""
	}
	if component == "pageserver" || component == "safekeeper" {
		return nodes[ordinal%len(nodes)]
	}
	return nodes[0]
}
func neonSecretName(ref SecretReference) string {
	return ref.Name + "-r" + strconv.FormatInt(ref.Revision, 10)
}
func neonSecretEnv(ref SecretReference, name, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: neonSecretName(ref)}, Key: key}}}
}
func neonObjectStorageEnv(ref SecretReference) []corev1.EnvVar {
	return []corev1.EnvVar{neonSecretEnv(ref, "AWS_ACCESS_KEY_ID", "access-key-id"), neonSecretEnv(ref, "AWS_SECRET_ACCESS_KEY", "secret-access-key")}
}
func neonPVCVolume(name, claim string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}
}
func neonSelector(platform, component string) map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform": platform, "app.kubernetes.io/name": "neon-" + component, "app.kubernetes.io/component": component}
}
func neonComponentLabels(base map[string]string, component, role string) map[string]string {
	out := neonCopyStrings(base)
	out["app.kubernetes.io/name"] = "neon-" + component
	out["app.kubernetes.io/component"] = component
	out["hakopod.io/neon-role"] = role
	return out
}
func neonCopyStrings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func neonResourceList(in Resources) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(in.CPU), corev1.ResourceMemory: resource.MustParse(in.Memory)}
}
func neonBool(value bool) *bool    { return &value }
func neonInt32(value int32) *int32 { return &value }
func neonInt64(value int64) *int64 { return &value }
