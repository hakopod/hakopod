package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"maps"
	"sort"

	"github.com/hakopod/hakopod/internal/spec"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Tenant hostnames live in shard Ingresses next to the service's own, so that
// thousands of them never make one huge object or a new revision.
const (
	customHostnameLabel     = "hakopod.io/custom-hostname"
	customHostnamesPerShard = 100
)

var certificateResource = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}

// CustomHostnameShardName is the deterministic name of one shard Ingress.
func CustomHostnameShardName(service string, n int) string {
	return fmt.Sprintf("%s-ch-%d", service, n)
}

// customHostnameSecret names the Certificate and TLS Secret of one host. It
// does not depend on the shard, so a host that moves keeps its certificate.
func customHostnameSecret(host string) string {
	sum := sha256.Sum256([]byte(host))
	return fmt.Sprintf("hp-ch-%x", sum[:12])
}

// customHostnameShards places hosts, given oldest first, into shards of at most
// 100. The shard count follows the service limit so shards sit half full at
// the limit; a host goes to its hash shard, or the next one with room.
// ponytail: modulo hashing moves most hosts when max changes; switch to jump
// consistent hashing if operators change max often on large services.
func customHostnameShards(hosts []string, limit int) map[int][]string {
	n := max(1, (limit+customHostnamesPerShard/2-1)/(customHostnamesPerShard/2), (len(hosts)+customHostnamesPerShard-1)/customHostnamesPerShard)
	out := map[int][]string{}
	for _, host := range hosts {
		h := fnv.New32a()
		h.Write([]byte(host))
		i := int(h.Sum32() % uint32(n))
		for len(out[i]) >= customHostnamesPerShard {
			i = (i + 1) % n
		}
		out[i] = append(out[i], host)
	}
	for _, members := range out {
		sort.Strings(members)
	}
	return out
}

// customHostnameIssuer is the cert-manager issuer for tenant certificates: the
// service's own issuer when it names one, otherwise the installation's.
func (c *Client) customHostnameIssuer(svc spec.Service) (string, string) {
	if svc.TLS != nil && svc.TLS.Issuer != "" {
		return svc.TLS.Issuer, tlsIssuerKind(svc.TLS.IssuerKind)
	}
	return c.options.TLSIssuer, "ClusterIssuer"
}

func customHostnameLabels(t Target, service string) map[string]string {
	labels := labelsFor(t, service)
	labels[customHostnameLabel] = "true"
	return labels
}

// ReconcileCustomHostnames makes the service's shard Ingresses, Certificates and
// TLS Secrets match hosts, given oldest first. Only shards whose content
// changed are written. A service that no longer routes tenant hostnames, or
// an empty list, removes everything.
func (c *Client) ReconcileCustomHostnames(ctx context.Context, t Target, service string, hosts []string) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	svc, ok := t.Spec.Services[service]
	if !ok || !spec.CustomHostnamesEnabled(svc) || !svc.Public || svc.Port == 0 {
		hosts = nil
	}
	issuer, kind := c.customHostnameIssuer(svc)
	ns := Namespace(t.ApplicationID)
	api := c.kube.NetworkingV1().Ingresses(ns)
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service + "," + customHostnameLabel
	existing := map[string]networkingv1.Ingress{}
	for next := ""; ; {
		list, err := api.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 500, Continue: next})
		if err != nil {
			return err
		}
		for _, item := range list.Items {
			existing[item.Name] = item
		}
		if next = list.Continue; next == "" {
			break
		}
	}
	if len(hosts) > 0 {
		base, err := c.ingressObject(ctx, t, service, svc)
		if err != nil {
			return err
		}
		if base, err = patchIngress(service, svc, base); err != nil {
			return err
		}
		for n, members := range customHostnameShards(hosts, svc.CustomHostnames.Limit()) {
			wanted := customHostnameShard(base, t, service, n, members, issuer != "")
			delete(existing, wanted.Name)
			if err := c.applyCustomHostnameShard(ctx, t, wanted); err != nil {
				return err
			}
		}
	}
	for name, stale := range existing {
		if err := owned(&stale, t); err != nil {
			return err
		}
		if err := api.Delete(ctx, name, deleteOptions(&stale)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if issuer == "" {
		hosts = nil
	}
	return c.reconcileCustomHostnameCertificates(ctx, t, service, hosts, issuer, kind)
}

// customHostnameShard mirrors the service's main ingress: same class, backend,
// path and annotations, one rule and one TLS entry per tenant host. The
// issuer annotations are dropped because Hakopod creates each Certificate.
func customHostnameShard(base *networkingv1.Ingress, t Target, service string, n int, hosts []string, tls bool) *networkingv1.Ingress {
	annotations := map[string]string{}
	for key, value := range base.Annotations {
		if key != "cert-manager.io/cluster-issuer" && key != "cert-manager.io/issuer" {
			annotations[key] = value
		}
	}
	shard := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: CustomHostnameShardName(service, n), Namespace: Namespace(t.ApplicationID), Labels: customHostnameLabels(t, service), Annotations: annotations}, Spec: networkingv1.IngressSpec{IngressClassName: base.Spec.IngressClassName}}
	for _, host := range hosts {
		rule := base.Spec.Rules[0].DeepCopy()
		rule.Host = host
		shard.Spec.Rules = append(shard.Spec.Rules, *rule)
		if tls {
			shard.Spec.TLS = append(shard.Spec.TLS, networkingv1.IngressTLS{Hosts: []string{host}, SecretName: customHostnameSecret(host)})
		}
	}
	return shard
}

func (c *Client) applyCustomHostnameShard(ctx context.Context, t Target, wanted *networkingv1.Ingress) error {
	api := c.kube.NetworkingV1().Ingresses(wanted.Namespace)
	current, err := api.Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err := owned(current, t); err != nil {
		return err
	}
	annotations := mergeIngressAnnotations(current.Annotations, wanted.Annotations)
	if apiequality.Semantic.DeepEqual(current.Spec, wanted.Spec) && maps.Equal(current.Labels, wanted.Labels) && maps.Equal(current.Annotations, annotations) {
		return nil
	}
	current.Spec, current.Labels, current.Annotations = wanted.Spec, wanted.Labels, annotations
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

// ownedCertificates lists the service's tenant Certificates by name.
func (c *Client) ownedCertificates(ctx context.Context, t Target, service string) (map[string]unstructured.Unstructured, error) {
	out := map[string]unstructured.Unstructured{}
	if c.dynamic == nil {
		return out, nil
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service + "," + customHostnameLabel
	api := c.dynamic.Resource(certificateResource).Namespace(Namespace(t.ApplicationID))
	for next := ""; ; {
		list, err := api.List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 500, Continue: next})
		if apierrors.IsNotFound(err) {
			return out, nil // cert-manager is not installed, so there is nothing to clean.
		}
		if err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			out[item.GetName()] = item
		}
		if next = list.GetContinue(); next == "" {
			return out, nil
		}
	}
}

func (c *Client) reconcileCustomHostnameCertificates(ctx context.Context, t Target, service string, hosts []string, issuer, kind string) error {
	existing, err := c.ownedCertificates(ctx, t, service)
	if err != nil {
		return err
	}
	if len(hosts) > 0 && c.dynamic == nil {
		return fmt.Errorf("cert-manager API client unavailable")
	}
	ns := Namespace(t.ApplicationID)
	api := c.dynamic
	policy := false
	for _, host := range hosts {
		name := customHostnameSecret(host)
		current, found := existing[name]
		delete(existing, name)
		issuerRef := map[string]any{"name": issuer, "kind": kind, "group": "cert-manager.io"}
		if found {
			ref, _, _ := unstructured.NestedMap(current.Object, "spec", "issuerRef")
			if fmt.Sprint(ref) == fmt.Sprint(issuerRef) {
				continue
			}
			if err := owned(&current, t); err != nil {
				return err
			}
			if err := unstructured.SetNestedMap(current.Object, issuerRef, "spec", "issuerRef"); err != nil {
				return err
			}
			if _, err := api.Resource(certificateResource).Namespace(ns).Update(ctx, &current, metav1.UpdateOptions{}); err != nil {
				return err
			}
			continue
		}
		if !policy {
			// The HTTP-01 solver pod must be reachable from the ingress proxy.
			if err := c.applyTLSChallengePolicy(ctx, t); err != nil {
				return err
			}
			policy = true
		}
		labels := map[string]any{}
		for key, value := range customHostnameLabels(t, service) {
			labels[key] = value
		}
		certificate := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
			"metadata": map[string]any{"name": name, "namespace": ns, "labels": labels},
			"spec": map[string]any{
				"secretName": name, "dnsNames": []any{host}, "issuerRef": issuerRef,
				// Labels on the Secret let cleanup find it by ownership.
				"secretTemplate": map[string]any{"labels": labels},
			},
		}}
		if _, err := api.Resource(certificateResource).Namespace(ns).Create(ctx, certificate, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	for name, stale := range existing {
		if err := owned(&stale, t); err != nil {
			return err
		}
		if err := api.Resource(certificateResource).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		// cert-manager leaves the issued Secret behind; it holds a private key.
		secret, err := c.kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := owned(secret, t); err != nil {
			return err
		}
		if err := c.kube.CoreV1().Secrets(ns).Delete(ctx, name, deleteOptions(secret)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// CustomHostnameCertificate is what cert-manager reports for one tenant host.
type CustomHostnameCertificate struct {
	Ready  bool
	Reason string
}

// CustomHostnameCertificates reads the service's tenant certificates by host.
// tls is false when no issuer is configured and hosts are served over HTTP.
func (c *Client) CustomHostnameCertificates(ctx context.Context, t Target, service string) (map[string]CustomHostnameCertificate, bool, error) {
	issuer, _ := c.customHostnameIssuer(t.Spec.Services[service])
	if issuer == "" {
		return nil, false, nil
	}
	items, err := c.ownedCertificates(ctx, t, service)
	if err != nil {
		return nil, true, err
	}
	out := map[string]CustomHostnameCertificate{}
	for _, item := range items {
		names, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "dnsNames")
		if len(names) != 1 {
			continue
		}
		state := CustomHostnameCertificate{Reason: "Waiting for cert-manager to request the certificate."}
		conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, _ := raw.(map[string]any)
			if condition["type"] != "Ready" {
				continue
			}
			state.Ready = condition["status"] == "True"
			if message, _ := condition["message"].(string); message != "" {
				state.Reason = message
			}
		}
		out[names[0]] = state
	}
	return out, true, nil
}

// cleanupCustomHostnames removes the shards, Certificates and Secrets of every
// service that no longer routes tenant hostnames, including removed services.
func (c *Client) cleanupCustomHostnames(ctx context.Context, t Target) error {
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + customHostnameLabel
	services := map[string]bool{}
	list, err := c.kube.NetworkingV1().Ingresses(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 500})
	if err != nil {
		return err
	}
	for _, item := range list.Items {
		services[item.Labels[serviceKey]] = true
	}
	if c.dynamic != nil {
		certificates, err := c.dynamic.Resource(certificateResource).Namespace(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 500})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			for _, item := range certificates.Items {
				services[item.GetLabels()[serviceKey]] = true
			}
		}
	}
	// A page of 500 objects names every service in practice; a later deploy
	// sees the rest once these are gone.
	names := make([]string, 0, len(services))
	for name := range services {
		if name != "" && !spec.CustomHostnamesEnabled(t.Spec.Services[name]) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := c.ReconcileCustomHostnames(ctx, t, name, nil); err != nil {
			return err
		}
	}
	return nil
}

// reconcileServiceCustomHostnames applies a service's tenant hostnames during
// a deployment, so the shards follow the main ingress's backend and annotations.
func (c *Client) reconcileServiceCustomHostnames(ctx context.Context, t Target, name string, svc spec.Service) error {
	if !spec.CustomHostnamesEnabled(svc) || c.options.CustomHostnames == nil {
		return nil
	}
	hosts, err := c.options.CustomHostnames(ctx, t.ApplicationID, name)
	if err != nil {
		return err
	}
	return c.ReconcileCustomHostnames(ctx, t, name, hosts)
}
