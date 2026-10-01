package managedplatform

import (
	"crypto/sha256"
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

const SupabaseUpstreamCommit = "d6c81b66c9999cb121dd8876f541313d484f157f"
const supabasePostgresHBA = "local all all trust\nhostnossl all all 0.0.0.0/0 reject\nhostnossl all all ::/0 reject\nhostssl all all 0.0.0.0/0 scram-sha-256\nhostssl all all ::/0 scram-sha-256\n"

// SupabaseRenderInput contains only public desired state. Secret bodies are
// resolved into immutable Kubernetes Secrets before this renderer is called.
type SupabaseRenderInput struct {
	Spec                          Spec
	PlatformID                    string
	Images                        map[string]string
	Revision                      int64
	NamespaceUID                  types.UID
	Assets                        map[string]string
	Identities                    map[string]RuntimeIdentity
	ApprovedEncryptedStorageClass string
	DatabaseClaim                 ObservedClaimState
	PreviousSpec                  *Spec
	SharedStorageGID              int64
	ApprovedExternalHTTPSCIDRs    []string
}

// RuntimeIdentity is qualified with the pinned image before rendering. The
// renderer never guesses an image's supported non-root user or group.
type RuntimeIdentity struct {
	UID int64
	GID int64
}
type ObservedClaimState struct {
	Observed bool
	UID      types.UID
}

// SupabaseManifests is deterministic: every collection is sorted by kind and
// name, and callers can compare the namespace UID before applying Objects.
type SupabaseManifests struct {
	Namespace                     corev1.Namespace
	ExpectedUID                   types.UID
	Objects                       []runtime.Object
	RequiredSecrets               []string
	TLSRequired                   bool
	PruneConfigMapsBeforeRevision int64
	RetainSecretSnapshots         []string
}

var supabaseAssetNames = []string{
	"api/envoy/cds.yaml", "api/envoy/docker-entrypoint.sh", "api/envoy/envoy.yaml", "api/envoy/lds.template.yaml",
	"api/kong-entrypoint.sh", "api/kong.yml", "db/_supabase.sql", "db/init/data.sql", "db/jwt.sql", "db/logs.sql",
	"db/pooler.sql", "db/realtime.sql", "db/roles.sql", "db/webhooks.sql", "functions/deno.jsonc", "functions/hello/index.ts",
	"functions/main/index.ts", "logs/vector.yml", "pooler/pooler.exs", "proxy/caddy/Caddyfile", "proxy/nginx/supabase-nginx.conf.tpl",
	"snippets/.gitkeep", "storage/.gitkeep",
}

var supabaseAssetSHA256 = map[string]string{
	"api/envoy/cds.yaml":                  "1d7514b891370ed27c25911df008887402e16ab09273e6e433225bb7f09f7905",
	"api/envoy/docker-entrypoint.sh":      "8a0c9503764de32ba39e0d81e73629457b13ec42ef19897e30e4918c996d0508",
	"api/envoy/envoy.yaml":                "3697f23b0be9ec5b829f937c600eb9b878f1f778ab510b42ad5e4f14742447e9",
	"api/envoy/lds.template.yaml":         "c0c9218d57df78df195958d74c0501184a1db3fd451b2d2ed1ec649ef92420e2",
	"api/kong-entrypoint.sh":              "aacdadade6adde6284163ea79e0d88decce0276796ae4749a15f018d56eda933",
	"api/kong.yml":                        "c55d3f8064560188b12f2bdd0aa01ced36cd6cab3734b539c01604977b33ddc9",
	"db/_supabase.sql":                    "9dce462adc04137d6afabcf28efa60a6c355270a4b32d7af58891ac4eb964c5f",
	"db/init/data.sql":                    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"db/jwt.sql":                          "c1d3f5c01e44646c2e533c5a1c111821cc49cb5a4c2534e08cddad61d8fb0339",
	"db/logs.sql":                         "f0463ce5030907acef49326d2bffd36002df0718035071be7c99bd7fc897c63d",
	"db/pooler.sql":                       "df97ebe148d94cfb92a5e37ebf972dfd496be195f0915ecf14396b2cf50efecb",
	"db/realtime.sql":                     "7e9e442e7fc4dae05544c07b67bede37a00d84644304dfce4d937134cb4c8f88",
	"db/roles.sql":                        "3ad717b225daa38aa982da26750f35641eb404e1eb5e69a763c22236ab96c1b2",
	"db/webhooks.sql":                     "05a61316b899374253891cbba66ce073d9273f33983c3f47961eda3aa26c2823",
	"functions/deno.jsonc":                "a9a29da36c2576f755dd868b9f553a02afdf7d7bccf52e97e65c7f68cf36bfef",
	"functions/hello/index.ts":            "65e7d7ce5d898dd285e660cbab54521fca4bf990c41370f51fad5b4d4cae5f42",
	"functions/main/index.ts":             "ed402c31abf346198d71cc7bab3b7eead03b91d6b9d638b30fc5e5fada25802b",
	"logs/vector.yml":                     "8f9fa080e3cd8107ac3e6d3bf8d6aa8959b6845d3cd8d5144fb8f28a45607a41",
	"pooler/pooler.exs":                   "8d9f464bb31d1a92926d2301b2544d08cb93d1f8693c6770525f5d1038f178e5",
	"proxy/caddy/Caddyfile":               "7c571b03cbc5ebdc10a0d7f05e5caae3ff3e6c4a4543624cf08a52ac654fb01e",
	"proxy/nginx/supabase-nginx.conf.tpl": "f9dc5f45b6c4b3a3e3640c0711eb875a7f4ddb6530909c49b444035f30154a16",
	"snippets/.gitkeep":                   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"storage/.gitkeep":                    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
}

var databaseAssetPaths = []string{"db/_supabase.sql", "db/jwt.sql", "db/logs.sql", "db/pooler.sql", "db/realtime.sql", "db/webhooks.sql"}
var databaseAssetTargets = []struct{ Source, Target string }{
	{"db/_supabase.sql", "migrations/97-_supabase.sql"},
	{"db/jwt.sql", "init-scripts/99-jwt.sql"},
	{"db/logs.sql", "migrations/99-logs.sql"},
	{"db/pooler.sql", "migrations/99-pooler.sql"},
	{"db/realtime.sql", "migrations/99-realtime.sql"},
	{"db/webhooks.sql", "init-scripts/98-webhooks.sql"},
}
var envoyPublicAssetPaths = []string{"api/envoy/cds.yaml", "api/envoy/envoy.yaml"}
var functionAssetPaths = []string{"functions/deno.jsonc", "functions/hello/index.ts", "functions/main/index.ts"}
var poolerAssetPaths = []string{"pooler/pooler.exs"}

func SupabaseAssetNames() []string { return append([]string(nil), supabaseAssetNames...) }

func RenderSupabase(in SupabaseRenderInput) (SupabaseManifests, error) {
	plan, err := PlanSupabase(in.Spec, in.Images)
	if err != nil {
		return SupabaseManifests{}, err
	}
	if in.Revision < 1 || in.NamespaceUID == "" || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(in.PlatformID) {
		return SupabaseManifests{}, fmt.Errorf("rendering requires a positive revision and observed namespace UID")
	}
	if err := validateSupabaseAssets(in.Assets); err != nil {
		return SupabaseManifests{}, err
	}
	if err := validateRuntimeIdentities(in.Identities); err != nil {
		return SupabaseManifests{}, err
	}
	if in.ApprovedEncryptedStorageClass == "" || len(utilvalidation.IsDNS1123Subdomain(in.ApprovedEncryptedStorageClass)) != 0 {
		return SupabaseManifests{}, fmt.Errorf("rendering requires an approved encrypted StorageClass")
	}
	if in.SharedStorageGID < 1 {
		return SupabaseManifests{}, fmt.Errorf("rendering requires a qualified non-root shared storage GID")
	}
	if err := validateSecretRotation(in); err != nil {
		return SupabaseManifests{}, err
	}
	if len(in.ApprovedExternalHTTPSCIDRs) > 16 {
		return SupabaseManifests{}, fmt.Errorf("external HTTPS egress permits at most 16 approved CIDRs")
	}
	seenCIDRs := map[string]bool{}
	for _, cidr := range in.ApprovedExternalHTTPSCIDRs {
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil || network.String() != cidr || !publicEgressIP(ip) {
			return SupabaseManifests{}, fmt.Errorf("external HTTPS egress CIDR %q must be canonical and public", cidr)
		}
		prefix, bits := network.Mask.Size()
		if bits == 32 && prefix < 24 || bits == 128 && prefix < 64 {
			return SupabaseManifests{}, fmt.Errorf("external HTTPS egress CIDR %q is too broad", cidr)
		}
		if seenCIDRs[cidr] {
			return SupabaseManifests{}, fmt.Errorf("external HTTPS egress CIDR %q is duplicated", cidr)
		}
		seenCIDRs[cidr] = true
	}

	plan.Namespace = "managed-platform-" + in.PlatformID
	labels := map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform": in.Spec.Name, "hakopod.io/managed-platform-id": in.PlatformID, "hakopod.io/platform-kind": "supabase", "hakopod.io/revision": strconv.FormatInt(in.Revision, 10)}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: plan.Namespace, Labels: cloneStrings(labels)}}
	owner := metav1.OwnerReference{APIVersion: "v1", Kind: "Namespace", Name: plan.Namespace, UID: in.NamespaceUID}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: plan.Namespace, Labels: cloneStrings(labels), OwnerReferences: []metav1.OwnerReference{owner}}
	}
	configName := func(name string) string { return name + "-r" + strconv.FormatInt(in.Revision, 10) }

	objects := []runtime.Object{}
	for _, group := range []struct {
		name   string
		prefix string
		paths  []string
	}{
		{"supabase-database-bootstrap", "db/", databaseAssetPaths},
		{"supabase-envoy-public", "api/envoy/", envoyPublicAssetPaths},
		{"supabase-functions", "functions/", functionAssetPaths},
		{"supabase-pooler", "pooler/", poolerAssetPaths},
	} {
		data := map[string]string{}
		for _, path := range group.paths {
			data[assetKey(path)] = in.Assets[path]
		}
		if group.name == "supabase-envoy-public" {
			data[assetKey("api/envoy/cds.yaml")] = strings.ReplaceAll(data[assetKey("api/envoy/cds.yaml")], "realtime-dev.supabase-realtime", "realtime")
		}
		if group.name == "supabase-database-bootstrap" {
			data["pg_hba.conf"] = supabasePostgresHBA
		}
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: meta(configName(group.name)), Immutable: boolPtr(true), Data: data})
	}

	for _, key := range SupabaseRequiredStorageKeys() {
		objects = append(objects, &corev1.PersistentVolumeClaim{ObjectMeta: meta("supabase-" + key), Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &in.ApprovedEncryptedStorageClass, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resourceQuantity(in.Spec.Storage[key])}}}})
	}

	for _, component := range plan.Components {
		pod := supabasePod(in, component, labels, configName)
		if component.Name == "database" {
			objects = append(objects, &appsv1.StatefulSet{ObjectMeta: meta("supabase-database"), Spec: appsv1.StatefulSetSpec{ServiceName: serviceName(component.Name), Replicas: int32Ptr(1), UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: componentSelectorLabels(in.Spec.Name, component.Name)}, Template: pod}})
		} else {
			objects = append(objects, &appsv1.Deployment{ObjectMeta: meta("supabase-" + component.Name), Spec: appsv1.DeploymentSpec{Replicas: int32Ptr(1), Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, Selector: &metav1.LabelSelector{MatchLabels: componentSelectorLabels(in.Spec.Name, component.Name)}, Template: pod}})
		}
		ports := make([]corev1.ServicePort, 0, len(component.Ports))
		for _, port := range component.Ports {
			ports = append(ports, corev1.ServicePort{Name: "tcp-" + strconv.Itoa(int(port)), Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP})
		}
		objects = append(objects, &corev1.Service{ObjectMeta: meta(serviceName(component.Name)), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: componentSelectorLabels(in.Spec.Name, component.Name), Ports: ports}})
	}

	objects = append(objects, supabasePolicies(meta, labels, plan.Components, in.ApprovedExternalHTTPSCIDRs)...)
	sort.Slice(objects, func(i, j int) bool {
		ai, aj := objects[i].(metav1.Object), objects[j].(metav1.Object)
		if objects[i].GetObjectKind().GroupVersionKind().Kind == objects[j].GetObjectKind().GroupVersionKind().Kind {
			return ai.GetName() < aj.GetName()
		}
		return fmt.Sprintf("%T", objects[i]) < fmt.Sprintf("%T", objects[j])
	})
	secrets := make([]string, 0, len(in.Spec.Secrets))
	for _, ref := range in.Spec.Secrets {
		secrets = append(secrets, secretSnapshotName(ref))
	}
	sort.Strings(secrets)
	pruneBefore := in.Revision - 1
	if pruneBefore < 1 {
		pruneBefore = 0
	}
	return SupabaseManifests{Namespace: ns, ExpectedUID: in.NamespaceUID, Objects: objects, RequiredSecrets: secrets, TLSRequired: true, PruneConfigMapsBeforeRevision: pruneBefore, RetainSecretSnapshots: append([]string(nil), secrets...)}, nil
}

func supabasePod(in SupabaseRenderInput, component Component, labels map[string]string, configName func(string) string) corev1.PodTemplateSpec {
	const postgresDataDirectory = "/var/lib/postgresql/data/pgdata"
	const postgresKeyVolume = "/var/lib/postgresql/pgsodium-volume"
	const postgresKeyDirectory = postgresKeyVolume + "/keyring"
	const postgresKeyFile = "pgsodium_root.key"
	identity := in.Identities[component.Name]
	security := &corev1.SecurityContext{AllowPrivilegeEscalation: boolPtr(false), ReadOnlyRootFilesystem: boolPtr(true), RunAsNonRoot: boolPtr(true), RunAsUser: &identity.UID, RunAsGroup: &identity.GID, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	container := corev1.Container{Name: component.Name, Image: component.Image, ImagePullPolicy: corev1.PullIfNotPresent, SecurityContext: security, Resources: corev1.ResourceRequirements{Requests: resourceList(component.Resources), Limits: resourceList(component.Resources)}}
	for _, port := range component.Ports {
		container.Ports = append(container.Ports, corev1.ContainerPort{Name: "tcp-" + strconv.Itoa(int(port)), ContainerPort: port})
	}
	container.ReadinessProbe, container.LivenessProbe, container.StartupProbe = supabaseProbes(component.Name)
	for _, key := range component.SecretKeys {
		if key == "envoy-runtime-config" || key == "gateway-tls-certificate" || key == "database-role-bootstrap" || key == "database-tls-certificate" {
			continue
		}
		ref := in.Spec.Secrets[key]
		container.Env = append(container.Env, corev1.EnvVar{Name: secretEnvironmentName(component.Name, key), ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretSnapshotName(ref)}, Key: secretValueKey(component.Name, key)}}})
	}
	container.Env = append(container.Env, supabasePublicEnvironment(in.Spec, component.Name)...)
	container.Env = append(container.Env, supabaseSecretAliases(in.Spec, component.Name)...)
	for _, key := range component.StorageKeys {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: key, MountPath: storageMount(component.Name, key), ReadOnly: component.Name == "studio" && key == "edge-functions"})
	}
	tmpLimit := resource.MustParse("256Mi")
	volumes := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpLimit}}}}
	initContainers := []corev1.Container{}
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"})
	for _, key := range component.StorageKeys {
		volumes = append(volumes, corev1.Volume{Name: key, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "supabase-" + key}}})
	}
	if component.Name == "database" {
		container.Env = append(container.Env, corev1.EnvVar{Name: "PGDATA", Value: postgresDataDirectory})
		roleRef := in.Spec.Secrets["database-role-bootstrap"]
		tlsRef := in.Spec.Secrets["database-tls-certificate"]
		tlsLimit := resource.MustParse("4Mi")
		volumes = append(volumes, databaseAssetVolume("bootstrap", configName("supabase-database-bootstrap")), corev1.Volume{Name: "role-bootstrap", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretSnapshotName(roleRef), Items: []corev1.KeyToPath{{Key: "value", Path: "99-z-hakopod-role-passwords.sql"}}}}}, corev1.Volume{Name: "database-tls-source", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretSnapshotName(tlsRef), DefaultMode: int32Ptr(0440), Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}, {Key: "tls.key", Path: "tls.key"}, {Key: "ca.crt", Path: "ca.crt"}}}}}, corev1.Volume{Name: "database-tls-runtime", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tlsLimit}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "database-tls-runtime", MountPath: "/etc/postgresql-tls", ReadOnly: true})
		for _, asset := range databaseAssetTargets {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "bootstrap", MountPath: "/docker-entrypoint-initdb.d/" + asset.Target, SubPath: asset.Target, ReadOnly: true})
		}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "bootstrap", MountPath: "/etc/postgresql-custom/hakopod-pg_hba.conf", SubPath: "pg_hba.conf", ReadOnly: true})
		// The image creates and configures its base roles first. This final
		// postgres-phase file only applies the separately scoped passwords.
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "role-bootstrap", MountPath: "/docker-entrypoint-initdb.d/init-scripts/99-z-hakopod-role-passwords.sql", SubPath: "99-z-hakopod-role-passwords.sql", ReadOnly: true})
		postgresRunLimit := resource.MustParse("64Mi")
		volumes = append(volumes, corev1.Volume{Name: "postgres-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &postgresRunLimit}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "postgres-run", MountPath: "/var/run/postgresql"})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "database-encryption", MountPath: "/etc/postgresql-custom/" + postgresKeyFile, SubPath: "keyring/" + postgresKeyFile, ReadOnly: true})
		keyInit := corev1.Container{Name: "initialize-pgsodium-key", Image: component.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-ceu"}, Args: []string{`directory="$1"
key="$directory/pgsodium_root.key"
expected_uid="$2"
expected_gid="$3"
validate_directory() {
  test -d "$1" && test ! -L "$1"
  test "$(stat -c %u "$1")" = "$expected_uid"
  test "$(stat -c %g "$1")" = "$expected_gid"
  test "$(stat -c %a "$1")" = 700
}
validate_key() {
  test -f "$1" && test ! -L "$1"
  test "$(stat -c %u "$1")" = "$expected_uid"
  test "$(stat -c %g "$1")" = "$expected_gid"
  test "$(stat -c %a "$1")" = 600
  test "$(wc -c < "$1" | tr -d ' ')" = 64
  LC_ALL=C grep -Eq '^[0-9a-f]{64}$' "$1"
}
umask 077
if [ ! -e "$directory" ] && [ ! -L "$directory" ]; then
  if mkdir "$directory" 2>/dev/null; then chmod 0700 "$directory"; fi
fi
validate_directory "$directory"
if [ ! -e "$key" ] && [ ! -L "$key" ]; then
  temporary="$directory/.pgsodium_root.key.$$"
  trap 'rm -f "$temporary"' EXIT HUP INT TERM
  head -c 32 /dev/urandom | od -A n -t x1 | tr -d ' \n' > "$temporary"
  validate_key "$temporary"
  if ln "$temporary" "$key" 2>/dev/null; then :; fi
  rm -f "$temporary"
  trap - EXIT HUP INT TERM
fi
validate_key "$key"`, "initialize-pgsodium-key", postgresKeyDirectory, strconv.FormatInt(identity.UID, 10), strconv.FormatInt(identity.GID, 10)}, SecurityContext: security.DeepCopy(), Resources: container.Resources, VolumeMounts: []corev1.VolumeMount{{Name: "database-encryption", MountPath: postgresKeyVolume}, {Name: "tmp", MountPath: "/tmp"}}}
		initContainers = append(initContainers, keyInit)
		tlsInit := corev1.Container{Name: "initialize-database-tls", Image: component.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-ceu"}, Args: []string{`umask 077
test -L /source/..data
data_link=$(readlink /source/..data)
case "$data_link" in ..[0-9][0-9][0-9][0-9]_[0-9][0-9]_[0-9][0-9]_*) ;; *) exit 1;; esac
case "$data_link" in */*|*..data*) exit 1;; esac
test -d "/source/$data_link" && test ! -L "/source/$data_link"
for name in tls.crt tls.key ca.crt; do
  test -L "/source/$name" && test "$(readlink "/source/$name")" = "..data/$name"
done
cd "/source/$data_link"
for name in tls.crt tls.key ca.crt; do
  test -f "$name" && test ! -L "$name"
  size=$(wc -c < "$name" | tr -d ' ')
  test "$size" -gt 0
  case "$name" in tls.key) test "$size" -le 16384;; *) test "$size" -le 49152;; esac
done
stage=/target/.database-tls-staging
current=/target/current
validate_file() {
  name="$1"; path="$2"; mode="$3"
  test -f "$path" && test ! -L "$path"
  test "$(stat -c %u:%g:%a "$path")" = "$(id -u):$(id -g):$mode"
  test "$(sha256sum "$name" | awk '{print $1}')" = "$(sha256sum "$path" | awk '{print $1}')"
}
if [ -e "$current" ] || [ -L "$current" ]; then
  test -d "$current" && test ! -L "$current"
  test "$(stat -c %u:%g:%a "$current")" = "$(id -u):$(id -g):700"
  validate_file tls.crt "$current/tls.crt" 644
  validate_file tls.key "$current/tls.key" 600
  validate_file ca.crt "$current/ca.crt" 644
  test "$(find "$current" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" = 3
  exit 0
fi
if [ -e "$stage" ] || [ -L "$stage" ]; then
  test -d "$stage" && test ! -L "$stage"
  test "$(stat -c %u:%g:%a "$stage")" = "$(id -u):$(id -g):700"
  for path in "$stage"/* "$stage"/.[!.]* "$stage"/..?*; do
    if [ ! -e "$path" ] && [ ! -L "$path" ]; then continue; fi
    test -f "$path" && test ! -L "$path"
    test "$(stat -c %u:%g "$path")" = "$(id -u):$(id -g)"
    case "$(basename "$path")" in tls.crt|tls.key|ca.crt) ;; *) exit 1;; esac
    size=$(wc -c < "$path" | tr -d ' ')
    case "$(basename "$path")" in tls.key) test "$size" -le 16384;; *) test "$size" -le 49152;; esac
    case "$(stat -c %a "$path")" in 400|600|644) ;; *) exit 1;; esac
  done
  rm -rf "$stage"
fi
mkdir "$stage"
chmod 0700 "$stage"
trap 'rm -rf "$stage"' EXIT HUP INT TERM
for name in tls.crt tls.key ca.crt; do
  cp "$name" "$stage/$name"
  test -f "$stage/$name" && test ! -L "$stage/$name"
  test "$(sha256sum "$name" | awk '{print $1}')" = "$(sha256sum "$stage/$name" | awk '{print $1}')"
done
chmod 0600 "$stage/tls.key"
chmod 0644 "$stage/tls.crt" "$stage/ca.crt"
validate_file tls.crt "$stage/tls.crt" 644
validate_file tls.key "$stage/tls.key" 600
validate_file ca.crt "$stage/ca.crt" 644
mv "$stage" "$current"
trap - EXIT HUP INT TERM`}, SecurityContext: security.DeepCopy(), Resources: container.Resources, VolumeMounts: []corev1.VolumeMount{{Name: "database-tls-source", MountPath: "/source", ReadOnly: true}, {Name: "database-tls-runtime", MountPath: "/target"}, {Name: "tmp", MountPath: "/tmp"}}}
		initContainers = append(initContainers, tlsInit)
		container.Args = []string{"postgres", "-c", "config_file=/etc/postgresql/postgresql.conf", "-c", "data_directory=" + postgresDataDirectory, "-c", "hba_file=/etc/postgresql-custom/hakopod-pg_hba.conf", "-c", "ssl=on", "-c", "ssl_min_protocol_version=TLSv1.2", "-c", "ssl_cert_file=/etc/postgresql-tls/current/tls.crt", "-c", "ssl_key_file=/etc/postgresql-tls/current/tls.key", "-c", "ssl_ca_file=/etc/postgresql-tls/current/ca.crt", "-c", "log_min_messages=fatal"}
	}
	databaseTLSClients := map[string]bool{"auth": true, "pooler": true, "postgres-meta": true, "realtime": true, "rest": true, "storage": true}
	if databaseTLSClients[component.Name] {
		tlsRef := in.Spec.Secrets["database-tls-certificate"]
		volumes = append(volumes, corev1.Volume{Name: "database-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretSnapshotName(tlsRef), DefaultMode: int32Ptr(0440), Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "database-ca", MountPath: "/etc/hakopod-database-ca", ReadOnly: true})
	}
	if component.Name == "pooler" {
		volumes = append(volumes, assetVolume("pooler-config", configName("supabase-pooler"), "pooler/", poolerAssetPaths))
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "pooler-config", MountPath: "/etc/pooler", ReadOnly: true})
		container.Command = []string{"/bin/sh", "-ceu"}
		container.Args = []string{`export HOME=/tmp/pooler-home MIX_HOME=/tmp/pooler-mix HEX_HOME=/tmp/pooler-hex ERL_CRASH_DUMP=/tmp/pooler-crash.dump
mkdir -p "$HOME" "$MIX_HOME" "$HEX_HOME"
/app/bin/migrate
/app/bin/supavisor eval "$(cat /etc/pooler/pooler.exs)"
exec /app/bin/server`}
	}
	if component.Name == "edge-runtime" {
		cacheLimit := resource.MustParse("1Gi")
		volumes = append(volumes, assetVolume("functions-config", configName("supabase-functions"), "functions/", functionAssetPaths), corev1.Volume{Name: "deno-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &cacheLimit}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "deno-cache", MountPath: "/var/cache/deno"})
		container.Env = append(container.Env, corev1.EnvVar{Name: "DENO_DIR", Value: "/var/cache/deno"})
		initContainers = append(initContainers, corev1.Container{Name: "seed-functions", Image: component.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-c"}, Args: []string{"set -eu; if [ ! -e /target/main/index.ts ]; then cp -R /seed/. /target/; fi"}, SecurityContext: security.DeepCopy(), Resources: container.Resources, VolumeMounts: []corev1.VolumeMount{{Name: "functions-config", MountPath: "/seed", ReadOnly: true}, {Name: "edge-functions", MountPath: "/target"}, {Name: "tmp", MountPath: "/tmp"}}})
		container.Args = []string{"start", "--user-worker-request-idle-timeout", "150000", "--main-service", "/home/deno/functions/main"}
	}
	if component.Name == "api-gateway" {
		ref := in.Spec.Secrets["envoy-runtime-config"]
		tlsRef := in.Spec.Secrets["gateway-tls-certificate"]
		volumes = append(volumes, assetVolume("envoy-public", configName("supabase-envoy-public"), "api/envoy/", envoyPublicAssetPaths), corev1.Volume{Name: "envoy-runtime", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretSnapshotName(ref), Items: []corev1.KeyToPath{{Key: "value", Path: "lds.yaml"}}}}}, corev1.Volume{Name: "gateway-tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretSnapshotName(tlsRef), DefaultMode: int32Ptr(0440), Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}, {Key: "tls.key", Path: "tls.key"}, {Key: "ca.crt", Path: "ca.crt"}}}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "envoy-public", MountPath: "/etc/envoy/envoy.yaml", SubPath: "envoy.yaml", ReadOnly: true}, corev1.VolumeMount{Name: "envoy-public", MountPath: "/etc/envoy/cds.yaml", SubPath: "cds.yaml", ReadOnly: true}, corev1.VolumeMount{Name: "envoy-runtime", MountPath: "/etc/envoy/lds.yaml", SubPath: "lds.yaml", ReadOnly: true}, corev1.VolumeMount{Name: "gateway-tls", MountPath: "/etc/envoy/tls", ReadOnly: true})
		container.Command = []string{"envoy"}
		container.Args = []string{"-c", "/etc/envoy/envoy.yaml", "--service-cluster", "supabase"}
	}
	if component.Name == "edge-runtime" || component.Name == "studio" {
		tlsRef := in.Spec.Secrets["gateway-tls-certificate"]
		volumes = append(volumes, corev1.Volume{Name: "gateway-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretSnapshotName(tlsRef), DefaultMode: int32Ptr(0440), Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "gateway-ca", MountPath: "/etc/hakopod-gateway-ca", ReadOnly: true})
		container.Env = append(container.Env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/etc/hakopod-gateway-ca/ca.crt"})
		if component.Name == "studio" {
			container.Env = append(container.Env, corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: "/etc/hakopod-gateway-ca/ca.crt"})
		}
	}
	if component.Name == "realtime" {
		container.Command = []string{"/bin/sh", "-ceu"}
		container.Args = []string{`export HOME=/tmp/realtime-home MIX_HOME=/tmp/realtime-mix HEX_HOME=/tmp/realtime-hex ERL_CRASH_DUMP=/tmp/erl_crash.dump
mkdir -p "$HOME" "$MIX_HOME" "$HEX_HOME"
/app/bin/migrate
if [ "${SEED_SELF_HOST:-}" = true ]; then
  /app/bin/realtime eval 'Realtime.Release.seeds(Realtime.Repo)'
fi
exec /app/bin/server`}
	}
	if component.Name == "rest" {
		container.Command = []string{"postgrest"}
	}
	policy := corev1.FSGroupChangeOnRootMismatch
	storageGID := in.SharedStorageGID
	if component.Name == "database" {
		storageGID = identity.GID
	}
	return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: componentLabels(labels, component.Name)}, Spec: corev1.PodSpec{AutomountServiceAccountToken: boolPtr(false), EnableServiceLinks: boolPtr(false), NodeSelector: map[string]string{"kubernetes.io/hostname": in.Spec.Placement.NodeNames[0]}, TerminationGracePeriodSeconds: int64Ptr(30), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: boolPtr(true), RunAsUser: &identity.UID, RunAsGroup: &identity.GID, FSGroup: &storageGID, SupplementalGroups: []int64{storageGID}, FSGroupChangePolicy: &policy, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, InitContainers: initContainers, Containers: []corev1.Container{container}, Volumes: volumes}}
}

func supabasePolicies(meta func(string) metav1.ObjectMeta, labels map[string]string, components []Component, externalHTTPS []string) []runtime.Object {
	tcp := corev1.ProtocolTCP
	dns := intstr.FromInt32(53)
	// Keep default denial separate from the explicit component dependencies.
	// Adding a listener does not grant every component access to that listener.
	base := &networkingv1.NetworkPolicy{ObjectMeta: meta("supabase-default-deny-and-internal"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &dns}, {Protocol: protocolPtr(corev1.ProtocolUDP), Port: &dns}}}}}}
	dependencies := map[string]map[string][]int32{
		"api-gateway":   {"auth": {9999}, "rest": {3000}, "realtime": {4000}, "storage": {5000}, "edge-runtime": {9000}, "postgres-meta": {8080}, "studio": {3000}},
		"auth":          {"database": {5432}},
		"edge-runtime":  {"api-gateway": {8443}},
		"pooler":        {"database": {5432}},
		"postgres-meta": {"database": {5432}},
		"realtime":      {"database": {5432}},
		"rest":          {"database": {5432}},
		"storage":       {"database": {5432}, "rest": {3000}, "image-proxy": {5001}},
		"studio":        {"postgres-meta": {8080}, "api-gateway": {8443}},
	}
	peer := func(component string) networkingv1.NetworkPolicyPeer {
		selector := componentSelectorLabels(labels["hakopod.io/managed-platform"], component)
		selector["hakopod.io/managed-platform-id"] = labels["hakopod.io/managed-platform-id"]
		return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: selector}}
	}
	ports := func(values []int32) []networkingv1.NetworkPolicyPort {
		out := make([]networkingv1.NetworkPolicyPort, 0, len(values))
		for _, value := range values {
			port := intstr.FromInt32(value)
			out = append(out, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &port})
		}
		return out
	}
	objects := []runtime.Object{base}
	for _, component := range components {
		policy := &networkingv1.NetworkPolicy{ObjectMeta: meta("supabase-" + component.Name + "-internal"), Spec: networkingv1.NetworkPolicySpec{PodSelector: *peer(component.Name).PodSelector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
		for _, other := range components {
			if values := dependencies[component.Name][other.Name]; len(values) != 0 {
				policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{peer(other.Name)}, Ports: ports(values)})
			}
			if values := dependencies[other.Name][component.Name]; len(values) != 0 {
				policy.Spec.Ingress = append(policy.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{peer(other.Name)}, Ports: ports(values)})
			}
		}
		objects = append(objects, policy)
	}
	port := intstr.FromInt32(8443)
	gateway := &networkingv1.NetworkPolicy{ObjectMeta: meta("supabase-envoy-ingress"), Spec: networkingv1.NetworkPolicySpec{PodSelector: *peer("api-gateway").PodSelector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"hakopod.io/managed-ingress": "true"}}}}, Ports: ports([]int32{port.IntVal})}}}}
	objects = append(objects, gateway)
	if len(externalHTTPS) > 0 {
		https := intstr.FromInt32(443)
		rules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(externalHTTPS))
		for _, cidr := range externalHTTPS {
			rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &https}}})
		}
		objects = append(objects, &networkingv1.NetworkPolicy{ObjectMeta: meta("supabase-edge-approved-https"), Spec: networkingv1.NetworkPolicySpec{PodSelector: *peer("edge-runtime").PodSelector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: rules}})
	}
	return objects
}

func supabaseProbes(component string) (readiness, liveness, startup *corev1.Probe) {
	handler := corev1.ProbeHandler{}
	port := int32(0)
	period, failures, startDelay, timeout := int32(5), int32(3), int32(0), int32(5)
	switch component {
	case "studio":
		port = 3000
		startDelay = 20
		timeout = 10
		handler.HTTPGet = &corev1.HTTPGetAction{Path: "/api/platform/profile", Port: intstr.FromInt32(port), Scheme: corev1.URISchemeHTTP}
	case "api-gateway":
		port = 8443
		period = 10
		handler.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}
	case "auth":
		port = 9999
		handler.HTTPGet = &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(port), Scheme: corev1.URISchemeHTTP}
	case "rest":
		port = 3000
		handler.Exec = &corev1.ExecAction{Command: []string{"postgrest", "--ready"}}
	case "realtime":
		port = 4000
		period = 30
		startDelay = 10
		handler.Exec = &corev1.ExecAction{Command: []string{"/bin/sh", "-c", "curl -sSfL --head -o /dev/null -H \"Authorization: Bearer ${ANON_KEY}\" http://127.0.0.1:4000/api/tenants/realtime-dev/health"}}
	case "storage":
		port = 5000
		startDelay = 10
		handler.HTTPGet = &corev1.HTTPGetAction{Path: "/status", Port: intstr.FromInt32(port), Scheme: corev1.URISchemeHTTP}
	case "image-proxy":
		port = 5001
		handler.Exec = &corev1.ExecAction{Command: []string{"imgproxy", "health"}}
	case "database":
		port = 5432
		failures = 10
		handler.Exec = &corev1.ExecAction{Command: []string{"pg_isready", "-U", "postgres", "-h", "127.0.0.1"}}
	case "pooler":
		port = 4000
		period = 10
		failures = 10
		startDelay = 30
		handler.HTTPGet = &corev1.HTTPGetAction{Path: "/api/health", Port: intstr.FromInt32(port), Scheme: corev1.URISchemeHTTP}
	case "postgres-meta":
		port = 8080
		handler.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}
	case "edge-runtime":
		port = 9000
		handler.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}
	}
	readiness = &corev1.Probe{ProbeHandler: handler, TimeoutSeconds: timeout, PeriodSeconds: period, FailureThreshold: failures}
	liveness = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 30, TimeoutSeconds: 3, PeriodSeconds: 10, FailureThreshold: 6}
	startup = liveness.DeepCopy()
	startup.InitialDelaySeconds = startDelay
	startup.PeriodSeconds = 5
	startup.FailureThreshold = 60
	return readiness, liveness, startup
}

func secretSnapshotName(ref SecretReference) string {
	return ref.Name + "-r" + strconv.FormatInt(ref.Revision, 10)
}
func componentLabels(base map[string]string, component string) map[string]string {
	out := cloneStrings(base)
	out["app.kubernetes.io/name"] = "supabase-" + component
	out["app.kubernetes.io/component"] = component
	return out
}
func componentSelectorLabels(platform, component string) map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform": platform, "app.kubernetes.io/name": "supabase-" + component, "app.kubernetes.io/component": component}
}
func cloneStrings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func assetKey(path string) string { return strings.ReplaceAll(path, "/", "..") }
func assetVolume(name, configMap, prefix string, paths []string) corev1.Volume {
	items := make([]corev1.KeyToPath, 0, len(paths))
	for _, path := range paths {
		items = append(items, corev1.KeyToPath{Key: assetKey(path), Path: strings.TrimPrefix(path, prefix)})
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap}, Items: items}}}
}
func databaseAssetVolume(name, configMap string) corev1.Volume {
	items := make([]corev1.KeyToPath, 0, len(databaseAssetPaths)+1)
	for _, asset := range databaseAssetTargets {
		items = append(items, corev1.KeyToPath{Key: assetKey(asset.Source), Path: asset.Target})
	}
	items = append(items, corev1.KeyToPath{Key: "pg_hba.conf", Path: "pg_hba.conf"})
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap}, Items: items}}}
}
func storageMount(component, key string) string {
	if key == "database" {
		return "/var/lib/postgresql/data"
	}
	if key == "database-encryption" {
		return "/var/lib/postgresql/pgsodium-volume"
	}
	if key == "objects" {
		return "/var/lib/storage"
	}
	if key == "edge-functions" && component == "studio" {
		return "/app/edge-functions"
	}
	if key == "edge-functions" {
		return "/home/deno/functions"
	}
	if key == "studio-snippets" {
		return "/app/snippets"
	}
	return "/var/lib/" + key
}
func serviceName(component string) string {
	names := map[string]string{"api-gateway": "api-gw", "database": "db", "edge-runtime": "functions", "image-proxy": "imgproxy", "pooler": "supavisor", "postgres-meta": "meta"}
	if name := names[component]; name != "" {
		return name
	}
	return component
}
func secretValueKey(component, key string) string {
	if component == "edge-runtime" && (key == "publishable-key" || key == "secret-key") {
		return "edge-json"
	}
	return "value"
}
func secretEnvironmentName(component, key string) string {
	known := map[string]string{
		"auth/auth-database-url": "GOTRUE_DB_DATABASE_URL", "auth/jwt-secret": "GOTRUE_JWT_SECRET", "auth/jwt-signing-keys": "GOTRUE_JWT_KEYS",
		"database/database-owner-password": "POSTGRES_PASSWORD", "database/jwt-secret": "JWT_SECRET",
		"edge-runtime/anon-key": "SUPABASE_ANON_KEY", "edge-runtime/jwt-secret": "JWT_SECRET", "edge-runtime/jwt-verification-keys": "SUPABASE_JWKS", "edge-runtime/publishable-key": "SUPABASE_PUBLISHABLE_KEYS", "edge-runtime/secret-key": "SUPABASE_SECRET_KEYS", "edge-runtime/service-role-key": "SUPABASE_SERVICE_ROLE_KEY",
		"pooler/supavisor-database-url": "DATABASE_URL", "pooler/secret-key-base": "SECRET_KEY_BASE", "pooler/vault-encryption-key": "VAULT_ENC_KEY", "pooler/pooler-api-jwt-secret": "API_JWT_SECRET",
		"postgres-meta/postgres-meta-database-password": "PG_META_DB_PASSWORD", "postgres-meta/pg-meta-crypto-key": "CRYPTO_KEY",
		"realtime/anon-key": "ANON_KEY", "realtime/realtime-database-password": "DB_PASSWORD", "realtime/realtime-db-encryption-key": "DB_ENC_KEY", "realtime/secret-key-base": "SECRET_KEY_BASE", "realtime/jwt-secret": "API_JWT_SECRET", "realtime/jwt-verification-keys": "API_JWT_JWKS",
		"rest/rest-database-url": "PGRST_DB_URI", "rest/jwt-verification-keys": "PGRST_JWT_SECRET",
		"storage/storage-database-url": "DATABASE_URL", "storage/anon-key": "ANON_KEY", "storage/service-role-key": "SERVICE_KEY", "storage/jwt-secret": "AUTH_JWT_SECRET", "storage/jwt-verification-keys": "JWT_JWKS", "storage/storage-s3-access-key": "S3_PROTOCOL_ACCESS_KEY_ID", "storage/storage-s3-secret-key": "S3_PROTOCOL_ACCESS_KEY_SECRET",
		"studio/anon-key": "SUPABASE_ANON_KEY", "studio/jwt-secret": "AUTH_JWT_SECRET", "studio/pg-meta-crypto-key": "PG_META_CRYPTO_KEY", "studio/postgres-meta-database-password": "POSTGRES_PASSWORD", "studio/publishable-key": "SUPABASE_PUBLISHABLE_KEY", "studio/secret-key": "SUPABASE_SECRET_KEY", "studio/service-role-key": "SUPABASE_SERVICE_KEY",
	}
	if value := known[component+"/"+key]; value != "" {
		return value
	}
	return "HAKOPOD_" + strings.ToUpper(strings.ReplaceAll(component+"_"+key, "-", "_"))
}
func secretEnv(ref SecretReference, name, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretSnapshotName(ref)}, Key: key}}}
}
func supabaseSecretAliases(s Spec, component string) []corev1.EnvVar {
	switch component {
	case "database":
		return []corev1.EnvVar{secretEnv(s.Secrets["database-owner-password"], "PGPASSWORD", "value")}
	case "realtime":
		return []corev1.EnvVar{secretEnv(s.Secrets["jwt-secret"], "METRICS_JWT_SECRET", "value")}
	case "pooler":
		return []corev1.EnvVar{secretEnv(s.Secrets["jwt-secret"], "METRICS_JWT_SECRET", "value"), secretEnv(s.Secrets["supavisor-database-url"], "POSTGRES_PASSWORD", "password")}
	case "postgres-meta":
		return []corev1.EnvVar{secretEnv(s.Secrets["database-tls-certificate"], "PG_META_DB_SSL_ROOT_CERT", "ca.crt")}
	case "storage":
		return []corev1.EnvVar{secretEnv(s.Secrets["database-tls-certificate"], "DATABASE_SSL_ROOT_CERT", "ca.crt")}
	default:
		return nil
	}
}
func supabasePublicEnvironment(s Spec, component string) []corev1.EnvVar {
	c := s.Supabase
	values := map[string]map[string]string{
		"studio":        {"HOSTNAME": "0.0.0.0", "STUDIO_PG_META_URL": "http://meta:8080", "POSTGRES_HOST": "db", "POSTGRES_PORT": "5432", "POSTGRES_DB": c.DatabaseName, "POSTGRES_USER_READ_WRITE": "hakopod_meta", "PGRST_DB_SCHEMAS": "public,storage,graphql_public", "PGRST_DB_MAX_ROWS": strconv.Itoa(c.RESTMaxRows), "PGRST_DB_EXTRA_SEARCH_PATH": "public,extensions", "DEFAULT_ORGANIZATION_NAME": s.Name, "DEFAULT_PROJECT_NAME": s.Name, "SUPABASE_URL": "https://api-gw:8443", "SUPABASE_PUBLIC_URL": c.PublicURL, "ENABLED_FEATURES_LOGS_ALL": "false", "SNIPPETS_MANAGEMENT_FOLDER": "/app/snippets", "EDGE_FUNCTIONS_MANAGEMENT_FOLDER": "/app/edge-functions"},
		"auth":          {"GOTRUE_API_HOST": "0.0.0.0", "GOTRUE_API_PORT": "9999", "API_EXTERNAL_URL": strings.TrimSuffix(c.PublicURL, "/") + "/auth/v1", "GOTRUE_DB_DRIVER": "postgres", "GOTRUE_SITE_URL": c.SiteURL, "GOTRUE_URI_ALLOW_LIST": strings.Join(c.RedirectURLs, ","), "GOTRUE_DISABLE_SIGNUP": strconv.FormatBool(!c.EmailSignup && !c.AnonymousSignup), "GOTRUE_JWT_ADMIN_ROLES": "service_role", "GOTRUE_JWT_AUD": "authenticated", "GOTRUE_JWT_DEFAULT_GROUP_NAME": "authenticated", "GOTRUE_JWT_EXP": strconv.Itoa(c.JWTExpirySeconds), "GOTRUE_JWT_ISSUER": strings.TrimSuffix(c.PublicURL, "/") + "/auth/v1", "GOTRUE_EXTERNAL_EMAIL_ENABLED": strconv.FormatBool(c.EmailSignup), "GOTRUE_EXTERNAL_ANONYMOUS_USERS_ENABLED": strconv.FormatBool(c.AnonymousSignup), "GOTRUE_MAILER_AUTOCONFIRM": "false", "GOTRUE_MAILER_URLPATHS_INVITE": "/auth/v1/verify", "GOTRUE_MAILER_URLPATHS_CONFIRMATION": "/auth/v1/verify", "GOTRUE_MAILER_URLPATHS_RECOVERY": "/auth/v1/verify", "GOTRUE_MAILER_URLPATHS_EMAIL_CHANGE": "/auth/v1/verify", "GOTRUE_EXTERNAL_PHONE_ENABLED": "false"},
		"rest":          {"PGRST_DB_SCHEMAS": "public,storage,graphql_public", "PGRST_DB_MAX_ROWS": strconv.Itoa(c.RESTMaxRows), "PGRST_DB_EXTRA_SEARCH_PATH": "public,extensions", "PGRST_DB_ANON_ROLE": "anon", "PGRST_ADMIN_SERVER_PORT": "3001", "PGRST_ADMIN_SERVER_HOST": "localhost", "PGRST_DB_USE_LEGACY_GUCS": "false", "PGRST_APP_SETTINGS_JWT_EXP": strconv.Itoa(c.JWTExpirySeconds)},
		"realtime":      {"PORT": "4000", "DB_HOST": "db", "DB_PORT": "5432", "DB_USER": "hakopod_realtime", "DB_NAME": c.DatabaseName, "DB_AFTER_CONNECT_QUERY": "SET search_path TO _realtime", "DB_SSL": "true", "DB_SSL_CA_CERT": "/etc/hakopod-database-ca/ca.crt", "METRICS_JWT_SECRET": "", "ERL_AFLAGS": "-proto_dist inet_tcp", "DNS_NODES": "''", "RLIMIT_NOFILE": "10000", "APP_NAME": "realtime", "SEED_SELF_HOST": "true", "RUN_JANITOR": "true", "DISABLE_HEALTHCHECK_LOGGING": "true"},
		"storage":       {"POSTGREST_URL": "http://rest:3000", "STORAGE_PUBLIC_URL": c.PublicURL, "REQUEST_ALLOW_X_FORWARDED_PATH": "true", "FILE_SIZE_LIMIT": strconv.FormatInt(c.StorageFileLimitBytes, 10), "STORAGE_BACKEND": "file", "GLOBAL_S3_BUCKET": "stub", "FILE_STORAGE_BACKEND_PATH": "/var/lib/storage", "TENANT_ID": s.Name, "REGION": "local", "ENABLE_IMAGE_TRANSFORMATION": "true", "IMGPROXY_URL": "http://imgproxy:5001"},
		"image-proxy":   {"IMGPROXY_BIND": ":5001", "IMGPROXY_LOCAL_FILESYSTEM_ROOT": "/", "IMGPROXY_USE_ETAG": "true", "IMGPROXY_AUTO_WEBP": "true", "IMGPROXY_MAX_SRC_RESOLUTION": "16.8"},
		"postgres-meta": {"PG_META_PORT": "8080", "PG_META_DB_HOST": "db", "PG_META_DB_PORT": "5432", "PG_META_DB_NAME": c.DatabaseName, "PG_META_DB_USER": "hakopod_meta", "PG_META_DB_SSL_MODE": "verify-full"},
		"edge-runtime":  {"SUPABASE_URL": "https://api-gw:8443", "SUPABASE_PUBLIC_URL": c.PublicURL, "VERIFY_JWT": "true"},
		"database":      {"POSTGRES_HOST": "/var/run/postgresql", "PGPORT": "5432", "POSTGRES_PORT": "5432", "PGDATABASE": c.DatabaseName, "POSTGRES_DB": c.DatabaseName, "JWT_EXP": strconv.Itoa(c.JWTExpirySeconds)},
		"pooler":        {"PORT": "4000", "POSTGRES_PORT": "5432", "POSTGRES_HOST": "db", "POSTGRES_DB": c.DatabaseName, "CLUSTER_POSTGRES": "true", "REGION": "local", "ERL_AFLAGS": "-proto_dist inet_tcp", "POOLER_TENANT_ID": s.Name, "POOLER_DEFAULT_POOL_SIZE": strconv.Itoa(c.PoolSize), "POOLER_MAX_CLIENT_CONN": strconv.Itoa(c.PoolMaxClients), "POOLER_POOL_MODE": "transaction", "DB_POOL_SIZE": "5", "DATABASE_SSL_CA_CERT": "/etc/hakopod-database-ca/ca.crt", "DATABASE_SSL_SERVER_NAME": "db", "GLOBAL_UPSTREAM_CA_PATH": "/etc/hakopod-database-ca/ca.crt"},
	}
	names := make([]string, 0, len(values[component]))
	for name := range values[component] {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]corev1.EnvVar, 0, len(names))
	for _, name := range names {
		if values[component][name] != "" {
			out = append(out, corev1.EnvVar{Name: name, Value: values[component][name]})
		}
	}
	return out
}
func validateRuntimeIdentities(values map[string]RuntimeIdentity) error {
	if err := exactKeys(values, supabaseComponents, "runtime identities"); err != nil {
		return err
	}
	for _, name := range supabaseComponents {
		if values[name].UID < 1 || values[name].GID < 1 {
			return fmt.Errorf("runtime identity %s must use qualified non-root UID and GID", name)
		}
	}
	return nil
}
func publicEgressIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, value := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"} {
		_, denied, _ := net.ParseCIDR(value)
		if denied.Contains(ip) {
			return false
		}
	}
	return true
}
func validateSecretRotation(in SupabaseRenderInput) error {
	if !in.DatabaseClaim.Observed {
		return fmt.Errorf("rendering requires an observed database claim state")
	}
	if in.DatabaseClaim.UID == "" {
		if in.Revision != 1 || in.PreviousSpec != nil {
			return fmt.Errorf("initial provisioning requires revision 1 and no previous spec")
		}
		return nil
	}
	if in.PreviousSpec == nil {
		return fmt.Errorf("an observed database claim requires the complete previous spec")
	}
	if in.PreviousSpec.Supabase == nil || in.PreviousSpec.Supabase.DatabaseName != in.Spec.Supabase.DatabaseName || in.PreviousSpec.Supabase.JWTExpirySeconds != in.Spec.Supabase.JWTExpirySeconds {
		return fmt.Errorf("database_name and jwt_expiry_seconds cannot change until transactional database settings migration is implemented")
	}
	if err := exactKeys(in.PreviousSpec.Secrets, supabaseSecretKeys, "previous secrets"); err != nil {
		return fmt.Errorf("updates require the complete previous secret inventory: %w", err)
	}
	for _, key := range supabaseSecretKeys {
		previous, current := in.PreviousSpec.Secrets[key], in.Spec.Secrets[key]
		if previous != current {
			return fmt.Errorf("secret rotation for %s is unavailable until transactional database and client rotation is implemented", key)
		}
	}
	return nil
}
func validateSupabaseAssets(assets map[string]string) error {
	if len(assets) != len(supabaseAssetNames) {
		return fmt.Errorf("upstream assets must contain the complete pinned 23-file inventory")
	}
	for _, name := range supabaseAssetNames {
		value, ok := assets[name]
		if !ok {
			return fmt.Errorf("upstream assets requires %s", name)
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(value))) != supabaseAssetSHA256[name] {
			return fmt.Errorf("upstream asset %s does not match commit %s", name, SupabaseUpstreamCommit)
		}
	}
	return nil
}
func resourceQuantity(gib int64) resource.Quantity {
	return *resource.NewQuantity(gib<<30, resource.BinarySI)
}
func resourceList(r Resources) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(r.CPU), corev1.ResourceMemory: resource.MustParse(r.Memory)}
}
func boolPtr(v bool) *bool                           { return &v }
func int32Ptr(v int32) *int32                        { return &v }
func int64Ptr(v int64) *int64                        { return &v }
func protocolPtr(v corev1.Protocol) *corev1.Protocol { return &v }
