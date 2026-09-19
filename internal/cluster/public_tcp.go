package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var publicTCPResource = schema.GroupVersionResource{Group: "ingress.v3.haproxy.org", Version: "v3", Resource: "tcps"}

const publicTCPName = "hakopod-public-tcp"
const publicTCPClaimLabel = "hakopod.io/public-tcp-claim"
const publicTCPMaxIngressPods = 8

type PublicTCPStatus struct {
	Port       int32    `json:"port"`
	TargetPort int32    `json:"target_port"`
	Status     string   `json:"status"`
	Message    string   `json:"message"`
	Addresses  []string `json:"addresses,omitempty"`
}

func publicTCPModels(t Target) []any {
	result := []any{}
	for _, name := range spec.Names(t.Spec) {
		svc := t.Spec.Services[name]
		for _, listener := range svc.PublicTCP {
			id := fmt.Sprintf("hp-%s-%d", ownerID(t.ApplicationID), listener.Port)
			result = append(result, map[string]any{
				"name":    id,
				"service": map[string]any{"name": name, "port": int64(spec.PublicTCPServicePort(svc, listener))},
				"frontend": map[string]any{
					"name":                  id,
					"binds":                 map[string]any{"v4": map[string]any{"name": "v4", "address": "0.0.0.0", "port": int64(listener.Port)}},
					"acl_list":              []any{map[string]any{"acl_name": "allowed_source", "criterion": "src", "value": strings.Join(listener.SourceCIDRs, " ")}},
					"tcp_request_rule_list": []any{map[string]any{"type": "connection", "action": "reject", "cond": "unless", "cond_test": "allowed_source"}},
					// Bound idle SMTP connections independently of HTTP keep-alive settings.
					"client_timeout": int64(300000), "maxconn": int64(256),
				},
			})
		}
	}
	return result
}

func publicTCPObject(t Target, ingressClass string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ingress.v3.haproxy.org/v3", "kind": "TCP", "spec": publicTCPModels(t)}}
	obj.SetName(publicTCPName)
	obj.SetNamespace(Namespace(t.ApplicationID))
	obj.SetLabels(labelsFor(t, ""))
	obj.SetAnnotations(map[string]string{"ingress.class": ingressClass, "hakopod.io/revision": strconv.FormatInt(t.Revision, 10)})
	return obj
}

// ValidatePublicTCP is read-only and suitable for deployment plans. Only ports
// explicitly provisioned by an operator can be assigned by an application.
func (c *Client) ValidatePublicTCP(ctx context.Context, t Target) error {
	if !spec.HasPublicTCP(t.Spec) {
		return nil
	}
	if c.ServerlessEnabled() {
		_, port, _ := net.SplitHostPort(c.options.ServerlessAddress)
		for _, svc := range t.Spec.Services {
			for _, listener := range svc.PublicTCP {
				if strconv.Itoa(int(listener.Port)) == port {
					return fmt.Errorf("public TCP port %s is reserved for the serverless activation gateway", port)
				}
			}
		}
	}
	if policy := c.PublicTCPPolicy(); !policy.Allowed {
		return fmt.Errorf("%w: %s", ErrPublicTCPDisabled, policy.Message)
	}
	if err := c.validateDedicatedPublicTCPNode(ctx, false); err != nil {
		return err
	}
	if c.dynamic == nil {
		return fmt.Errorf("public TCP requires the HAProxy v3 TCP API")
	}
	controller, err := c.publicTCPController(ctx)
	if err != nil {
		return err
	}
	requested := map[int32]bool{}
	for _, svc := range t.Spec.Services {
		for _, listener := range svc.PublicTCP {
			if c.options.DedicatedPublicTCPNode == "" && !slices.Contains(c.options.PublicTCPPorts, listener.Port) {
				return fmt.Errorf("public TCP port %d is not enabled by the installation operator", listener.Port)
			}
			if !controller[listener.Port] {
				return fmt.Errorf("public TCP port %d is not provisioned on the owned ingress controller and its service", listener.Port)
			}
			requested[listener.Port] = true
			claim, e := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(ctx, publicTCPClaimName(listener.Port), metav1.GetOptions{})
			if e == nil && (t.ApplicationID == "" || owned(claim, t) != nil || claim.Labels[publicTCPClaimLabel] != "true") {
				return fmt.Errorf("public TCP port %d is reserved by another application or operator", listener.Port)
			}
			if e != nil && !apierrors.IsNotFound(e) {
				return e
			}
		}
	}
	// Both APIs can own frontends. Refuse oversize scans instead of overlooking
	// a route outside the first page or trusting an unbounded informer cache.
	for _, resource := range []schema.GroupVersionResource{publicTCPResource, {Group: "ingress.v1.haproxy.org", Version: "v1", Resource: "tcps"}} {
		list, e := c.dynamic.Resource(resource).List(ctx, metav1.ListOptions{Limit: 256})
		if apierrors.IsNotFound(e) && resource != publicTCPResource {
			continue
		}
		if e != nil {
			return fmt.Errorf("inspect HAProxy TCP routes: %w", e)
		}
		if list.GetContinue() != "" || len(list.Items) > 256 {
			return fmt.Errorf("public TCP route inventory exceeds the 256-resource review limit")
		}
		for _, item := range list.Items {
			class := item.GetAnnotations()["ingress.class"]
			if class != "" && class != c.options.IngressClass {
				continue
			}
			if resource == publicTCPResource && t.ApplicationID != "" && item.GetNamespace() == Namespace(t.ApplicationID) && item.GetName() == publicTCPName && owned(&item, t) == nil {
				continue
			}
			models, ok, _ := unstructured.NestedSlice(item.Object, "spec")
			if !ok {
				return fmt.Errorf("cannot inspect HAProxy TCP route %s/%s", item.GetNamespace(), item.GetName())
			}
			for _, model := range models {
				m, ok := model.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid existing HAProxy TCP model")
				}
				binds, _, _ := unstructured.NestedMap(m, "frontend", "binds")
				// v1 bindings are a list, whereas v3 uses a keyed object.
				values := []any{}
				for _, bind := range binds {
					values = append(values, bind)
				}
				if list, found, _ := unstructured.NestedSlice(m, "frontend", "binds"); found {
					values = append(values, list...)
				}
				for _, raw := range values {
					bind, ok := raw.(map[string]any)
					if !ok {
						return fmt.Errorf("invalid existing HAProxy TCP bind")
					}
					port, found, _ := unstructured.NestedInt64(bind, "port")
					if !found {
						return fmt.Errorf("existing HAProxy TCP port ranges cannot be reviewed automatically")
					}
					if requested[int32(port)] {
						return fmt.Errorf("public TCP port %d conflicts with HAProxy route %s/%s", port, item.GetNamespace(), item.GetName())
					}
				}
			}
		}
	}
	return nil
}

func publicTCPClaimName(port int32) string { return "hakopod-tcp-port-" + strconv.Itoa(int(port)) }

// publicTCPController checks the actual owned controller, not only environment
// flags. TCP ConfigMap routes are rejected because they race the CRD controller.
func (c *Client) publicTCPController(ctx context.Context) (map[int32]bool, error) {
	if c.options.ProxyNamespace != "haproxy-controller" || c.options.ProxyRelease != "hakopod-ingress" {
		return nil, fmt.Errorf("public TCP requires the standard owned ingress namespace and release for network isolation")
	}
	if _, err := c.proxyConfigMap(ctx); err != nil {
		return nil, err
	}
	dep, err := c.kube.AppsV1().Deployments(c.options.ProxyNamespace).Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !c.ownsProxy(dep) || dep.Spec.Template.Spec.HostNetwork || dep.Spec.Replicas == nil || *dep.Spec.Replicas < 1 || dep.Status.ObservedGeneration < dep.Generation || dep.Status.ReadyReplicas != *dep.Spec.Replicas || dep.Status.UpdatedReplicas != *dep.Spec.Replicas || dep.Status.Replicas != *dep.Spec.Replicas {
		return nil, fmt.Errorf("public TCP requires the ready owned HAProxy deployment without host networking")
	}
	if *dep.Spec.Replicas > publicTCPMaxIngressPods {
		return nil, fmt.Errorf("public TCP requires 1–%d ingress replicas for bounded reload acknowledgement", publicTCPMaxIngressPods)
	}
	svc, err := c.kube.CoreV1().Services(c.options.ProxyNamespace).Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !c.ownsProxy(svc) || svc.Spec.Selector["app.kubernetes.io/instance"] != c.options.ProxyRelease || svc.Spec.Selector["app.kubernetes.io/name"] != "kubernetes-ingress" {
		return nil, fmt.Errorf("public TCP ingress Service does not belong to the configured release")
	}
	ports := map[int32]bool{}
	for _, container := range dep.Spec.Template.Spec.Containers {
		if container.Name != "kubernetes-ingress-controller" {
			continue
		}
		for _, arg := range container.Args {
			if strings.HasPrefix(arg, "--configmap-tcp-services") {
				return nil, fmt.Errorf("public TCP CRDs cannot share an ingress controller with a TCP-services ConfigMap")
			}
			if strings.HasPrefix(arg, "--namespace-whitelist") || strings.HasPrefix(arg, "--namespace-blacklist") {
				return nil, fmt.Errorf("public TCP requires ingress namespace discovery without custom namespace filters")
			}
		}
		if !slices.Contains(container.Args, "--ingress.class="+c.options.IngressClass) {
			return nil, fmt.Errorf("public TCP ingress class does not match the controller")
		}
		for _, p := range container.Ports {
			if p.Protocol != corev1.ProtocolTCP && p.Protocol != "" {
				continue
			}
			for _, sp := range svc.Spec.Ports {
				if sp.Port != p.ContainerPort || sp.Protocol != corev1.ProtocolTCP {
					continue
				}
				matches := sp.TargetPort.Type == intstr.Int && sp.TargetPort.IntVal == p.ContainerPort || sp.TargetPort.Type == intstr.String && sp.TargetPort.StrVal == p.Name
				if !matches {
					continue
				}
				// Local forwarding preserves the client IP used by the frontend ACL.
				host := p.HostPort == p.ContainerPort
				external := (svc.Spec.Type == corev1.ServiceTypeLoadBalancer || svc.Spec.Type == corev1.ServiceTypeNodePort) && svc.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal
				if host || external {
					ports[p.ContainerPort] = true
				}
			}
		}
	}
	return ports, nil
}

func (c *Client) ownsProxy(meta metav1.Object) bool {
	return meta.GetLabels()["app.kubernetes.io/name"] == "kubernetes-ingress" && meta.GetLabels()["app.kubernetes.io/instance"] == c.options.ProxyRelease && meta.GetAnnotations()["meta.helm.sh/release-name"] == c.options.ProxyRelease && meta.GetAnnotations()["meta.helm.sh/release-namespace"] == c.options.ProxyNamespace
}

// PreparePublicTCP retains unchanged listeners but closes removed or changed
// listeners before workloads change. Claims survive failures and rollback so a
// different application cannot take a temporarily unused SMTP port.
func (c *Client) PreparePublicTCP(ctx context.Context, t Target) error {
	if err := c.ValidatePublicTCP(ctx, t); err != nil {
		return err
	}
	if t.ApplicationID == "" {
		return fmt.Errorf("public TCP changes require an application ID")
	}
	for _, svc := range t.Spec.Services {
		for _, listener := range svc.PublicTCP {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			labels := labelsFor(t, "")
			labels[publicTCPClaimLabel] = "true"
			claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: publicTCPClaimName(listener.Port), Namespace: c.options.ProxyNamespace, Labels: labels}, Immutable: ptr(true), Data: map[string]string{"application_id": t.ApplicationID, "project": t.Project, "environment": t.Environment, "application": t.Spec.Name}}
			_, err := c.kube.CoreV1().ConfigMaps(claim.Namespace).Create(ctx, claim, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				existing, e := c.kube.CoreV1().ConfigMaps(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
				if e != nil {
					return e
				}
				if owned(existing, t) != nil || existing.Labels[publicTCPClaimLabel] != "true" {
					return fmt.Errorf("public TCP port %d was concurrently reserved", listener.Port)
				}
			} else if err != nil {
				return err
			}
		}
	}
	if c.dynamic == nil {
		return nil
	}
	api := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, publicTCPName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if spec.HasPublicTCP(t.Spec) || t.Previous != nil && spec.HasPublicTCP(*t.Previous) {
			return c.waitPublicTCPApplied(ctx, t, []any{}, nil)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err = owned(current, t); err != nil {
		return err
	}
	wanted := publicTCPModels(t)
	existing, _, err := unstructured.NestedSlice(current.Object, "spec")
	if err != nil {
		return err
	}
	retained := []any{}
	for _, old := range existing {
		for _, desired := range wanted {
			if reflect.DeepEqual(old, desired) {
				retained = append(retained, old)
				break
			}
		}
	}
	if reflect.DeepEqual(retained, existing) {
		baseline, parseErr := publicTCPStoredBaseline(current)
		if parseErr != nil {
			return parseErr
		}
		return c.acknowledgePublicTCP(ctx, t, retained, baseline)
	}
	baseline, err := c.publicTCPBaseline(ctx, t)
	if err != nil {
		return err
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	// An empty spec is a durable removal tombstone. Keep its baseline until
	// every worker has stopped accepting the removed frontends, including retries.
	current.Object["spec"] = retained
	publicTCPSetPending(current, baseline)
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return c.acknowledgePublicTCP(ctx, t, retained, baseline)
}

// ReconcilePublicTCP publishes only after the caller has verified all workloads.
func (c *Client) ReconcilePublicTCP(ctx context.Context, t Target) error {
	if !spec.HasPublicTCP(t.Spec) {
		return nil
	}
	if err := c.ValidatePublicTCP(ctx, t); err != nil {
		return err
	}
	for _, svc := range t.Spec.Services {
		for _, listener := range svc.PublicTCP {
			claim, claimErr := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(ctx, publicTCPClaimName(listener.Port), metav1.GetOptions{})
			if claimErr != nil {
				return fmt.Errorf("public TCP reservation is missing: %w", claimErr)
			}
			if owned(claim, t) != nil || claim.Labels[publicTCPClaimLabel] != "true" {
				return fmt.Errorf("public TCP reservation changed before publication")
			}
		}
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(t.ApplicationID))
	wanted := publicTCPObject(t, c.options.IngressClass)
	current, err := api.Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if err = owned(current, t); err != nil {
			return err
		}
		if reflect.DeepEqual(current.Object["spec"], wanted.Object["spec"]) {
			baseline, parseErr := publicTCPStoredBaseline(current)
			if parseErr != nil {
				return parseErr
			}
			return c.acknowledgePublicTCP(ctx, t, publicTCPModels(t), baseline)
		}
	}
	baseline, probeErr := c.publicTCPBaseline(ctx, t)
	if probeErr != nil {
		return probeErr
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	publicTCPSetPending(wanted, baseline)
	if current == nil {
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
	} else {
		current.Object["spec"] = wanted.Object["spec"]
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		for k, v := range wanted.GetAnnotations() {
			annotations[k] = v
		}
		delete(annotations, "hakopod.io/tcp-acknowledged")
		current.SetAnnotations(annotations)
		_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	return c.acknowledgePublicTCP(ctx, t, publicTCPModels(t), baseline)
}

// publicTCPPolicy adds only explicit target ports from the same owned ingress
// pods as HTTP. Source CIDRs are enforced before forwarding by HAProxy.
func publicTCPPolicy(svc spec.Service, policy *networkingv1.NetworkPolicy) {
	if len(svc.PublicTCP) == 0 {
		return
	}
	rule := networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "haproxy-controller"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": "hakopod-ingress"}}}}}
	seen := map[int32]bool{}
	for _, listener := range svc.PublicTCP {
		if !seen[listener.TargetPort] {
			seen[listener.TargetPort] = true
			rule.Ports = append(rule.Ports, networkingv1.NetworkPolicyPort{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt32(listener.TargetPort))})
		}
	}
	policy.Spec.Ingress = append(policy.Spec.Ingress, rule)
}

func (c *Client) ObservePublicTCP(ctx context.Context, t Target, service string) ([]PublicTCPStatus, error) {
	statuses := []PublicTCPStatus{}
	svc, ok := t.Spec.Services[service]
	if !ok || len(svc.PublicTCP) == 0 {
		return statuses, nil
	}
	if policy := c.PublicTCPPolicy(); !policy.Allowed {
		for _, listener := range svc.PublicTCP {
			statuses = append(statuses, PublicTCPStatus{Port: listener.Port, TargetPort: listener.TargetPort, Status: "disabled", Message: policy.Message})
		}
		return statuses, nil
	}
	if c.dynamic == nil {
		return nil, fmt.Errorf("HAProxy TCP API unavailable")
	}
	obj, err := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(t.ApplicationID)).Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	var existing []any
	if err == nil {
		if err = owned(obj, t); err != nil {
			return nil, err
		}
		if obj.GetAnnotations()["ingress.class"] != c.options.IngressClass {
			return nil, fmt.Errorf("public TCP route ingress class differs from the configured controller")
		}
		existing, _, _ = unstructured.NestedSlice(obj.Object, "spec")
	}
	for _, listener := range svc.PublicTCP {
		status := PublicTCPStatus{Port: listener.Port, TargetPort: listener.TargetPort, Status: "missing", Message: "The public TCP route has not been applied."}
		if len(existing) > 0 {
			status.Status = "drifted"
			status.Message = "The public TCP route differs from the applied revision."
		}
		for _, model := range publicTCPModels(t) {
			m := model.(map[string]any)
			backend := m["service"].(map[string]any)
			frontend := m["frontend"].(map[string]any)
			binds := frontend["binds"].(map[string]any)
			bind := binds["v4"].(map[string]any)
			if backend["name"] != service || bind["port"] != int64(listener.Port) {
				continue
			}
			for _, actual := range existing {
				if reflect.DeepEqual(actual, model) {
					status.Status = "pending"
					status.Message = "Waiting for the ingress controller to acknowledge this route."
					if obj.GetAnnotations()["hakopod.io/tcp-acknowledged"] == publicTCPHash(existing) {
						status.Status = "configured"
						status.Message = "Route configured and reload acknowledged. External reachability and application STARTTLS require an external probe."
					}
					break
				}
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// Retain the pre-update process IDs with the resource so retrying an accepted
// CRD update cannot mistake an old worker for an acknowledged ACL change.
func publicTCPStoredBaseline(obj *unstructured.Unstructured) (map[string]string, error) {
	raw := obj.GetAnnotations()["hakopod.io/tcp-reload-baseline"]
	if raw == "" {
		return nil, fmt.Errorf("public TCP route lacks a durable reload baseline; operator review required")
	}
	if len(raw) > 4096 {
		return nil, fmt.Errorf("public TCP reload baseline exceeds its bound")
	}
	var baseline map[string]string
	if err := json.Unmarshal([]byte(raw), &baseline); err != nil || len(baseline) > 8 {
		return nil, fmt.Errorf("invalid public TCP reload baseline")
	}
	return baseline, nil
}

func publicTCPSetPending(obj *unstructured.Unstructured, baseline map[string]string) {
	encoded, _ := json.Marshal(baseline)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["hakopod.io/tcp-reload-baseline"] = string(encoded)
	delete(annotations, "hakopod.io/tcp-acknowledged")
	obj.SetAnnotations(annotations)
}
func publicTCPHash(models []any) string {
	data, _ := json.Marshal(models)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Persist acknowledgement only after the real worker has loaded this exact
// configuration. Ordinary health observation reads this marker without exec.
func (c *Client) acknowledgePublicTCP(ctx context.Context, t Target, models []any, baseline map[string]string) error {
	if err := c.waitPublicTCPApplied(ctx, t, models, baseline); err != nil {
		return err
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.dynamic.Resource(publicTCPResource).Namespace(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, publicTCPName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(current, t); err != nil {
		return err
	}
	actual, _, err := unstructured.NestedSlice(current.Object, "spec")
	if err != nil {
		return err
	}
	stored, err := publicTCPStoredBaseline(current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, models) || !reflect.DeepEqual(stored, baseline) {
		return fmt.Errorf("public TCP configuration changed before reload acknowledgement")
	}
	hash := publicTCPHash(models)
	annotations := current.GetAnnotations()
	if annotations["hakopod.io/tcp-acknowledged"] == hash {
		return nil
	}
	annotations["hakopod.io/tcp-acknowledged"] = hash
	current.SetAnnotations(annotations)
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}
