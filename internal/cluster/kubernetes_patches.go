package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/yaml"
)

// A user's Kubernetes patch is applied to the object Hakopod generated, and the
// result is compared with that object. Anything the guards below do not allow
// is refused, so a field Kubernetes adds later is protected until reviewed.
// See docs/kubernetes-patches.md for the rules in prose.

// patchedAnnotationsKey lists the annotation keys a patch added or changed, so
// the next reconcile can drop them once the patch stops setting them.
const patchedAnnotationsKey = "hakopod.io/patched-annotations"

// KubernetesPatchError names the service, object and field of a refused patch.
type KubernetesPatchError struct{ Service, Object, Reason string }

func (e *KubernetesPatchError) Error() string {
	return fmt.Sprintf("%s: kubernetes.%s patch refused: %s", e.Service, e.Object, e.Reason)
}

func strategicPatch[T any](generated *T, text string) (*T, error) {
	patch, err := spec.ParseKubernetesPatch(text)
	if err != nil {
		return nil, err
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	original, err := json.Marshal(generated)
	if err != nil {
		return nil, err
	}
	var zero T
	merged, err := strategicpatch.StrategicMergePatch(original, patchJSON, zero)
	if err != nil {
		return nil, fmt.Errorf("cannot merge: %v", err)
	}
	// A misspelt field would otherwise vanish silently.
	decoder := json.NewDecoder(bytes.NewReader(merged))
	decoder.DisallowUnknownFields()
	out := new(T)
	if err := decoder.Decode(out); err != nil {
		return nil, fmt.Errorf("the patched object is invalid: %v", err)
	}
	return out, nil
}

func patchDeployment(name string, svc spec.Service, generated *appsv1.Deployment) (*appsv1.Deployment, error) {
	if svc.Kubernetes == nil || svc.Kubernetes.Deployment == "" {
		return generated, nil
	}
	patched, err := strategicPatch(generated, svc.Kubernetes.Deployment)
	if err == nil {
		err = guardDeployment(generated, patched)
	}
	if err != nil {
		return nil, &KubernetesPatchError{name, "deployment", err.Error()}
	}
	recordPatchedAnnotations(generated.Annotations, &patched.Annotations)
	recordPatchedAnnotations(generated.Spec.Template.Annotations, &patched.Spec.Template.Annotations)
	return patched, nil
}

func patchService(name string, svc spec.Service, generated *corev1.Service) (*corev1.Service, error) {
	if svc.Kubernetes == nil || svc.Kubernetes.Service == "" {
		return generated, nil
	}
	patched, err := strategicPatch(generated, svc.Kubernetes.Service)
	if err == nil {
		err = guardService(generated, patched)
	}
	if err != nil {
		return nil, &KubernetesPatchError{name, "service", err.Error()}
	}
	recordPatchedAnnotations(generated.Annotations, &patched.Annotations)
	return patched, nil
}

func patchIngress(name string, svc spec.Service, generated *networkingv1.Ingress) (*networkingv1.Ingress, error) {
	if svc.Kubernetes == nil || svc.Kubernetes.Ingress == "" {
		return generated, nil
	}
	patched, err := strategicPatch(generated, svc.Kubernetes.Ingress)
	if err == nil {
		err = guardIngress(generated, patched)
	}
	if err != nil {
		return nil, &KubernetesPatchError{name, "ingress", err.Error()}
	}
	recordPatchedAnnotations(generated.Annotations, &patched.Annotations)
	return patched, nil
}

func recordPatchedAnnotations(generated map[string]string, patched *map[string]string) {
	keys := []string{}
	for key, value := range *patched {
		if previous, ok := generated[key]; !ok || previous != value {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	(*patched)[patchedAnnotationsKey] = strings.Join(keys, ",")
}

// patchedAnnotations returns the keys a previous patch set on an object,
// including the record itself.
func patchedAnnotations(annotations map[string]string) map[string]bool {
	keys := map[string]bool{}
	if record, ok := annotations[patchedAnnotationsKey]; ok {
		keys[patchedAnnotationsKey] = true
		for _, key := range strings.Split(record, ",") {
			keys[key] = true
		}
	}
	return keys
}

func dropPatchedAnnotations(annotations map[string]string) {
	for key := range patchedAnnotations(annotations) {
		delete(annotations, key)
	}
}

// sameExcept compares two objects field by field by their JSON names and
// names the first protected field that differs.
func sameExcept(path string, generated, patched any, allowed ...string) error {
	a, b := map[string]any{}, map[string]any{}
	for _, item := range []struct {
		from any
		to   *map[string]any
	}{{generated, &a}, {patched, &b}} {
		data, err := json.Marshal(item.from)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, item.to); err != nil {
			return err
		}
	}
	keys := []string{}
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !slices.Contains(allowed, key) && !reflect.DeepEqual(a[key], b[key]) {
			if path != "" {
				key = path + "." + key
			}
			return fmt.Errorf("%s is protected and cannot be changed", key)
		}
	}
	return nil
}

// reservedKey covers Hakopod's own labels and annotations (network membership,
// ownership, runtime state) and Kubernetes-reserved domains, some of which
// change pod security, such as AppArmor profiles.
func reservedKey(key string) string {
	prefix, _, found := strings.Cut(key, "/")
	if !found {
		prefix = ""
	}
	switch {
	case strings.Contains(key, "hakopod"):
		return "is reserved for Hakopod"
	case prefix == "app.kubernetes.io":
		// The recommended application labels are meant for users.
		return ""
	case prefix == "kubernetes.io" || prefix == "k8s.io" || strings.HasSuffix(prefix, ".kubernetes.io") || strings.HasSuffix(prefix, ".k8s.io"):
		return "uses a Kubernetes-reserved prefix"
	}
	return ""
}

// guardKeys allows new keys that refuse accepts and keeps every generated
// key unless override accepts changing or removing it.
func guardKeys(path string, generated, patched map[string]string, refuse func(string) string, override func(string) bool) error {
	keys := []string{}
	for key := range generated {
		keys = append(keys, key)
	}
	for key := range patched {
		if _, ok := generated[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		before, generatedKey := generated[key]
		after, patchedKey := patched[key]
		if generatedKey && patchedKey && before == after {
			continue
		}
		if generatedKey && !override(key) {
			return fmt.Errorf("%s[%q] is set by Hakopod and cannot be changed or removed", path, key)
		}
		if patchedKey {
			if reason := refuse(key); reason != "" {
				return fmt.Errorf("%s[%q] %s", path, key, reason)
			}
		}
	}
	return nil
}

func never(string) bool { return false }

// guardMetadata protects everything but labels and annotations, which may
// gain keys outside the reserved prefixes.
func guardMetadata(path string, generated, patched metav1.ObjectMeta, refuseAnnotation func(string) string, overrideAnnotation func(string) bool) error {
	a, b := generated.DeepCopy(), patched.DeepCopy()
	a.Labels, a.Annotations, b.Labels, b.Annotations = nil, nil, nil, nil
	if err := sameExcept(path, a, b); err != nil {
		return err
	}
	if err := guardKeys(path+".labels", generated.Labels, patched.Labels, reservedKey, never); err != nil {
		return err
	}
	return guardKeys(path+".annotations", generated.Annotations, patched.Annotations, refuseAnnotation, overrideAnnotation)
}

// Pod fields a patch may change. Every other pod field, including
// serviceAccountName, automountServiceAccountToken, hostNetwork, hostPID,
// hostIPC, hostUsers, securityContext, nodeName, runtimeClassName,
// imagePullSecrets, schedulerName, resourceClaims and ephemeralContainers,
// must stay as generated.
var patchablePodFields = []string{"containers", "initContainers", "volumes", "tolerations", "nodeSelector", "affinity", "topologySpreadConstraints", "priorityClassName", "terminationGracePeriodSeconds", "dnsPolicy", "dnsConfig", "hostAliases", "enableServiceLinks", "readinessGates"}

// Container fields a patch may change. image, imagePullPolicy, command, args,
// workingDir, securityContext, envFrom and the rest stay as generated.
var patchableContainerFields = []string{"env", "resources", "livenessProbe", "readinessProbe", "startupProbe", "lifecycle", "ports", "volumeMounts", "terminationMessagePath", "terminationMessagePolicy"}

func guardDeployment(generated, patched *appsv1.Deployment) error {
	if err := sameExcept("", generated, patched, "metadata", "spec"); err != nil {
		return err
	}
	if err := guardMetadata("metadata", generated.ObjectMeta, patched.ObjectMeta, reservedKey, never); err != nil {
		return err
	}
	// Replicas, selector and update strategy have their own TOML fields.
	if err := sameExcept("spec", generated.Spec, patched.Spec, "template"); err != nil {
		return err
	}
	if err := guardMetadata("spec.template.metadata", generated.Spec.Template.ObjectMeta, patched.Spec.Template.ObjectMeta, reservedKey, never); err != nil {
		return err
	}
	return guardPod("spec.template.spec", &generated.Spec.Template.Spec, &patched.Spec.Template.Spec)
}

func guardPod(path string, generated, patched *corev1.PodSpec) error {
	if err := sameExcept(path, generated, patched, patchablePodFields...); err != nil {
		return err
	}
	if strings.HasPrefix(patched.PriorityClassName, "system-") && patched.PriorityClassName != generated.PriorityClassName {
		return fmt.Errorf("%s.priorityClassName cannot use a system priority class", path)
	}
	if generated.Affinity != nil && !reflect.DeepEqual(generated.Affinity, patched.Affinity) {
		return fmt.Errorf("%s.affinity is set by Hakopod for this service and cannot be changed", path)
	}
	if err := guardKeys(path+".nodeSelector", generated.NodeSelector, patched.NodeSelector, func(string) string { return "" }, never); err != nil {
		return err
	}
	for _, toleration := range generated.Tolerations {
		if !slices.ContainsFunc(patched.Tolerations, func(t corev1.Toleration) bool { return reflect.DeepEqual(t, toleration) }) {
			return fmt.Errorf("%s.tolerations: the toleration for %q is set by Hakopod and cannot be changed or removed", path, toleration.Key)
		}
	}
	for _, toleration := range patched.Tolerations {
		if slices.ContainsFunc(generated.Tolerations, func(t corev1.Toleration) bool { return reflect.DeepEqual(t, toleration) }) {
			continue
		}
		if toleration.Key == "" {
			return fmt.Errorf("%s.tolerations: a toleration must name a taint key; an empty key tolerates every taint", path)
		}
		if strings.Contains(toleration.Key, "hakopod") {
			return fmt.Errorf("%s.tolerations: %q is a Hakopod taint reserved for its own placement", path, toleration.Key)
		}
	}
	added := map[string]bool{}
	for _, volume := range patched.Volumes {
		i := slices.IndexFunc(generated.Volumes, func(v corev1.Volume) bool { return v.Name == volume.Name })
		if i >= 0 {
			if !reflect.DeepEqual(generated.Volumes[i], volume) {
				return fmt.Errorf("%s.volumes[%q] is set by Hakopod and cannot be changed", path, volume.Name)
			}
			continue
		}
		// Secret, ConfigMap and projected volumes could read Hakopod's own
		// credentials or a service account token in the namespace.
		rest := volume.DeepCopy()
		rest.EmptyDir, rest.DownwardAPI = nil, nil
		if !reflect.DeepEqual(*rest, corev1.Volume{Name: volume.Name}) {
			return fmt.Errorf("%s.volumes[%q]: a patch may add only emptyDir and downwardAPI volumes", path, volume.Name)
		}
		added[volume.Name] = true
	}
	if err := guardContainers(path+".initContainers", generated.InitContainers, patched.InitContainers, added); err != nil {
		return err
	}
	return guardContainers(path+".containers", generated.Containers, patched.Containers, added)
}

func guardContainers(path string, generated, patched []corev1.Container, addedVolumes map[string]bool) error {
	if len(generated) != len(patched) {
		return fmt.Errorf("%s: a patch cannot add or remove containers", path)
	}
	for i := range patched {
		g, p := generated[i], patched[i]
		at := fmt.Sprintf("%s[%q]", path, p.Name)
		if g.Name != p.Name {
			return fmt.Errorf("%s: a patch cannot add, remove or reorder containers", path)
		}
		if err := sameExcept(at, g, p, patchableContainerFields...); err != nil {
			return err
		}
		for _, env := range g.Env {
			if env.ValueFrom != nil && !slices.ContainsFunc(p.Env, func(e corev1.EnvVar) bool { return reflect.DeepEqual(e, env) }) {
				return fmt.Errorf("%s.env[%q] comes from a secret or binding and cannot be changed or removed", at, env.Name)
			}
		}
		for _, env := range p.Env {
			if env.ValueFrom != nil && (env.ValueFrom.SecretKeyRef != nil || env.ValueFrom.ConfigMapKeyRef != nil) && !slices.ContainsFunc(g.Env, func(e corev1.EnvVar) bool { return reflect.DeepEqual(e, env) }) {
				return fmt.Errorf("%s.env[%q] cannot read a Secret or ConfigMap; use a secret reference in the specification", at, env.Name)
			}
		}
		if !reflect.DeepEqual(g.Resources.Claims, p.Resources.Claims) {
			return fmt.Errorf("%s.resources.claims is protected and cannot be changed", at)
		}
		for _, list := range []struct {
			name      string
			from, too corev1.ResourceList
		}{{"requests", g.Resources.Requests, p.Resources.Requests}, {"limits", g.Resources.Limits, p.Resources.Limits}} {
			for _, name := range slices.Concat(slices.Collect(maps.Keys(list.from)), slices.Collect(maps.Keys(list.too))) {
				if name == corev1.ResourceCPU || name == corev1.ResourceMemory || name == corev1.ResourceEphemeralStorage {
					continue
				}
				before, a := list.from[name]
				after, b := list.too[name]
				if a != b || a && before.Cmp(after) != 0 {
					return fmt.Errorf("%s.resources.%s[%q]: a patch may change only cpu, memory and ephemeral-storage", at, list.name, name)
				}
			}
		}
		for _, mount := range p.VolumeMounts {
			if !addedVolumes[mount.Name] && !slices.ContainsFunc(g.VolumeMounts, func(m corev1.VolumeMount) bool { return reflect.DeepEqual(m, mount) }) {
				return fmt.Errorf("%s.volumeMounts[%q]: a patch may mount only the volumes it adds", at, mount.MountPath)
			}
		}
		// The kubelet runs these requests from the node, outside network policy.
		for _, handler := range []*corev1.ProbeHandler{probeHandler(p.LivenessProbe), probeHandler(p.ReadinessProbe), probeHandler(p.StartupProbe)} {
			if handler != nil && (handler.HTTPGet != nil && handler.HTTPGet.Host != "" || handler.TCPSocket != nil && handler.TCPSocket.Host != "") {
				return fmt.Errorf("%s: probes cannot name a host; the kubelet would connect from the node", at)
			}
		}
		if l := p.Lifecycle; l != nil {
			for _, handler := range []*corev1.LifecycleHandler{l.PostStart, l.PreStop} {
				if handler != nil && (handler.HTTPGet != nil && handler.HTTPGet.Host != "" || handler.TCPSocket != nil && handler.TCPSocket.Host != "") {
					return fmt.Errorf("%s.lifecycle cannot name a host; the kubelet would connect from the node", at)
				}
			}
		}
	}
	return nil
}

func probeHandler(p *corev1.Probe) *corev1.ProbeHandler {
	if p == nil {
		return nil
	}
	return &p.ProbeHandler
}

func guardService(generated, patched *corev1.Service) error {
	if err := sameExcept("", generated, patched, "metadata", "spec"); err != nil {
		return err
	}
	if err := guardMetadata("metadata", generated.ObjectMeta, patched.ObjectMeta, reservedKey, never); err != nil {
		return err
	}
	// type, selector, clusterIP, externalIPs, loadBalancerIP and every other
	// field that could expose the service outside the cluster stay generated.
	if err := sameExcept("spec", generated.Spec, patched.Spec, serviceSpecPatchable...); err != nil {
		return err
	}
	for _, port := range generated.Spec.Ports {
		if !slices.ContainsFunc(patched.Spec.Ports, func(p corev1.ServicePort) bool { return reflect.DeepEqual(p, port) }) {
			return fmt.Errorf("spec.ports[%q] is set by Hakopod and cannot be changed or removed", port.Name)
		}
	}
	return nil
}

var serviceSpecPatchable = []string{"ports", "sessionAffinity", "sessionAffinityConfig", "publishNotReadyAddresses", "internalTrafficPolicy", "trafficDistribution"}

// HAProxy annotations a patch cannot set: raw configuration snippets, Secret or
// custom resource references, and settings that change what Hakopod Edge sees
// before its policy runs.
var refusedHAProxyAnnotations = []string{"auth-type", "auth-secret", "auth-realm", "server-ca", "server-crt", "client-ca", "client-crt-optional", "ssl-certificate", "ssl-passthrough", "src-ip-header", "route-acl", "path-rewrite", "set-host", "standalone-backend"}

func haproxyAnnotation(key string) string {
	name, ok := strings.CutPrefix(key, "haproxy.org/")
	switch {
	case !ok:
		return "is outside haproxy.org/; an ingress patch may set only HAProxy annotations"
	case strings.Contains(name, "snippet"), strings.HasPrefix(name, "cr-"), slices.Contains(refusedHAProxyAnnotations, name):
		return "is not available to patches; it injects HAProxy configuration, reads a Secret or bypasses Hakopod Edge"
	}
	return ""
}

func guardIngress(generated, patched *networkingv1.Ingress) error {
	if err := sameExcept("", generated, patched, "metadata", "spec"); err != nil {
		return err
	}
	override := func(key string) bool { return haproxyAnnotation(key) == "" }
	if err := guardMetadata("metadata", generated.ObjectMeta, patched.ObjectMeta, haproxyAnnotation, override); err != nil {
		return err
	}
	// ingressClassName, hosts, backends and TLS stay as generated.
	return sameExcept("spec", generated.Spec, patched.Spec)
}

// RenderManifests returns, as YAML, the Deployment, Service and Ingress Hakopod
// generates for one long-running service, with its Kubernetes patches applied.
// It reads cluster state where generation does but writes nothing. Secrets are
// separate objects and are never rendered. Bindings resolved while deploying
// (environment snapshots, workload identity, container daemon, runtime and node
// profiles, registry credentials and reserved capacity) are not shown; the
// deployment applies the same patches and guards on top of them.
func (c *Client) RenderManifests(ctx context.Context, t Target, name string) ([]byte, error) {
	t.Spec = spec.RuntimeEnvironment(t.Spec)
	svc, ok := t.Spec.Services[name]
	if !ok {
		return nil, fmt.Errorf("unknown service %q", name)
	}
	if svc.Job != nil || svc.Session != nil || svc.Actions != nil {
		return nil, fmt.Errorf("%s: manifests are rendered only for long-running services", name)
	}
	d, err := patchDeployment(name, svc, deployment(t, name, svc, c.options.RolloutTimeout, c.options.ReadinessProbeImage))
	if err != nil {
		return nil, err
	}
	d.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	objects := []any{d}
	if len(spec.ServicePorts(svc)) > 0 {
		s, err := patchService(name, svc, serviceObject(t, name, svc))
		if err != nil {
			return nil, err
		}
		s.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
		objects = append(objects, s)
	}
	if svc.Public || certificateOnlyIngress(svc) {
		i, err := c.ingressObject(ctx, t, name, svc)
		if err != nil {
			return nil, err
		}
		if i, err = patchIngress(name, svc, i); err != nil {
			return nil, err
		}
		i.TypeMeta = metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"}
		objects = append(objects, i)
	}
	var out bytes.Buffer
	for i, object := range objects {
		data, err := yaml.Marshal(object)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			out.WriteString("---\n")
		}
		out.Write(data)
	}
	return out.Bytes(), nil
}

// validateKubernetesPatches refuses a release whose patches change a protected
// field before anything is applied. Other rendering failures are left to the
// deployment, which reports them where they occur.
func (c *Client) validateKubernetesPatches(ctx context.Context, t Target) error {
	for _, name := range spec.Names(t.Spec) {
		if t.Spec.Services[name].Kubernetes == nil {
			continue
		}
		var refused *KubernetesPatchError
		if _, err := c.RenderManifests(ctx, t, name); errors.As(err, &refused) {
			return err
		}
	}
	return nil
}
