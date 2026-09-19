package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

type TLSStatus struct {
	Hostname   string     `json:"hostname"`
	Enabled    bool       `json:"enabled"`
	Ready      bool       `json:"ready"`
	Source     string     `json:"source"`
	SecretName string     `json:"secret_name,omitempty"`
	Issuer     string     `json:"issuer,omitempty"`
	NotBefore  *time.Time `json:"not_before,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Message    string     `json:"message,omitempty"`
}
type TLSIssuer struct {
	Name       string             `json:"name"`
	Email      string             `json:"email"`
	Server     string             `json:"server"`
	Ready      bool               `json:"ready"`
	Conditions []RuntimeCondition `json:"conditions"`
}
type TLSIssuers struct {
	Installed bool        `json:"installed"`
	Items     []TLSIssuer `json:"items"`
	Message   string      `json:"message,omitempty"`
}

func validateTLSCertificate(certificate, key []byte, hostname string, now time.Time) (*x509.Certificate, error) {
	if len(certificate) == 0 || len(certificate) > 256<<10 || len(key) == 0 || len(key) > 32<<10 {
		return nil, fmt.Errorf("certificate chain must be at most 256 KiB and key at most 32 KiB")
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return nil, fmt.Errorf("certificate and private key are not a valid matching PEM key pair")
	}
	if len(pair.Certificate) == 0 || len(pair.Certificate) > 8 {
		return nil, fmt.Errorf("certificate chain must contain between one and eight certificates")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("invalid certificate")
	}
	if err = leaf.VerifyHostname(hostname); err != nil {
		return nil, fmt.Errorf("certificate must cover service hostname %s", hostname)
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("certificate is expired or not yet valid")
	}
	if leaf.IsCA {
		return nil, fmt.Errorf("service certificate must be a leaf certificate")
	}
	switch key := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return nil, fmt.Errorf("RSA certificate key must be at least 2048 bits")
		}
	case *ecdsa.PublicKey:
		if key.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("ECDSA certificate key must be at least 256 bits")
		}
	case ed25519.PublicKey:
	default:
		return nil, fmt.Errorf("unsupported certificate key algorithm")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		server := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				server = true
			}
		}
		if !server {
			return nil, fmt.Errorf("certificate does not allow TLS server authentication")
		}
	}
	for _, der := range pair.Certificate[1:] {
		if _, err = x509.ParseCertificate(der); err != nil {
			return nil, fmt.Errorf("invalid certificate chain")
		}
	}
	return leaf, nil
}

func (c *Client) PutTLSCertificate(ctx context.Context, t Target, service string, certificate, key []byte) (string, error) {
	svc, ok := t.Spec.Services[service]
	if !ok || !svc.Public || c.options.AppDomain == "" {
		return "", fmt.Errorf("TLS requires an existing public service and application domain")
	}
	_, err := c.validateServiceCertificate(ctx, t, service, certificate, key)
	if err != nil {
		return "", err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if err = owned(ns, t); err != nil {
		return "", err
	}
	// Include the whole PEM chain so corrected intermediates produce a new
	// immutable secret even when the leaf certificate stays the same.
	hash := sha256.Sum256(certificate)
	shortService := service
	if len(shortService) > 23 {
		shortService = shortService[:23]
	}
	name := fmt.Sprintf("hp-tls-%s-%x", shortService, hash[:16])
	api := c.kube.CoreV1().Secrets(ns.Name)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if err = owned(existing, t); err != nil {
			return "", err
		}
		if existing.Labels[serviceKey] != service || existing.Type != corev1.SecretTypeTLS || existing.Immutable == nil || !*existing.Immutable {
			return "", fmt.Errorf("TLS secret ownership or immutability mismatch")
		}
		if _, err = c.validateServiceCertificate(ctx, t, service, existing.Data[corev1.TLSCertKey], existing.Data[corev1.TLSPrivateKeyKey]); err != nil {
			return "", err
		}
		return name, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	items, err := api.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service + ",hakopod.io/tls-upload=true", Limit: 64})
	if err != nil {
		return "", err
	}
	if len(items.Items) >= 64 || items.Continue != "" {
		return "", fmt.Errorf("service already has 64 uploaded certificates; an operator must retire unused historical secrets")
	}
	labels := labelsFor(t, service)
	labels["hakopod.io/tls-upload"] = "true"
	_, err = api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name, Labels: labels}, Immutable: ptr(true), Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return c.PutTLSCertificate(ctx, t, service, certificate, key)
	}
	return name, err
}

func (c *Client) configureTLSIngress(ctx context.Context, t Target, name string, svc spec.Service, wanted *networkingv1.Ingress) error {
	issuer := c.options.TLSIssuer
	secret := "hakopod-tls-" + name
	if svc.TLS != nil {
		issuer = svc.TLS.Issuer
		if svc.TLS.Certificate != "" {
			secret = svc.TLS.Certificate
			stored, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, secret, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("uploaded TLS certificate is unavailable")
			}
			if err = owned(stored, t); err != nil {
				return err
			}
			if stored.Type != corev1.SecretTypeTLS || stored.Labels[serviceKey] != name || stored.Labels["hakopod.io/tls-upload"] != "true" {
				return fmt.Errorf("TLS certificate is not owned by this service")
			}
			if _, err = c.validateServiceCertificate(ctx, t, name, stored.Data[corev1.TLSCertKey], stored.Data[corev1.TLSPrivateKeyKey]); err != nil {
				return err
			}
		} else if err := c.TLSIssuerExists(ctx, issuer); err != nil {
			return err
		}
	}
	if issuer == "" && (svc.TLS == nil || svc.TLS.Certificate == "") {
		return nil
	}
	if issuer != "" {
		if err := c.applyTLSChallengePolicy(ctx, t); err != nil {
			return err
		}
	}
	if wanted.Annotations == nil {
		wanted.Annotations = map[string]string{}
	}
	wanted.Annotations["haproxy.org/ssl-redirect"] = "true"
	if issuer != "" {
		wanted.Annotations["cert-manager.io/cluster-issuer"] = issuer
	}
	hosts, err := c.serviceHostnames(ctx, t, name)
	if err != nil {
		return err
	}
	wanted.Spec.TLS = []networkingv1.IngressTLS{{Hosts: hosts, SecretName: secret}}
	return nil
}

func (c *Client) ServiceTLS(ctx context.Context, t Target, service string) (TLSStatus, error) {
	value := TLSStatus{Hostname: c.hostname(t, service), Source: "none"}
	ingress, err := c.kube.NetworkingV1().Ingresses(Namespace(t.ApplicationID)).Get(ctx, service, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		value.Message = "No ingress has been applied for this service."
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if err = owned(ingress, t); err != nil {
		return value, err
	}
	for _, entry := range ingress.Spec.TLS {
		for _, host := range entry.Hosts {
			if host == value.Hostname {
				value.Enabled = true
				value.SecretName = entry.SecretName
			}
		}
	}
	if !value.Enabled {
		value.Message = "Service ingress uses HTTP."
		return value, nil
	}
	value.Issuer = ingress.Annotations["cert-manager.io/cluster-issuer"]
	value.Source = "uploaded"
	if value.Issuer != "" {
		value.Source = "cert-manager"
	}
	secret, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, value.SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		value.Message = "TLS is configured; the certificate secret has not been issued or uploaded."
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if value.Source == "uploaded" {
		if err = owned(secret, t); err != nil {
			return value, err
		}
		if secret.Labels[serviceKey] != service {
			value.Message = "Certificate ownership mismatch."
			return value, nil
		}
	}
	leaf, err := c.validateServiceCertificate(ctx, t, service, secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
	if err != nil {
		value.Message = err.Error()
		return value, nil
	}
	value.Ready = true
	value.NotBefore = &leaf.NotBefore
	value.ExpiresAt = &leaf.NotAfter
	value.Message = "A currently valid hostname-matching certificate is attached to the ingress; external DNS and routing are not probed."
	return value, nil
}

type issuerDocument struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		ACME struct {
			Email  string `json:"email"`
			Server string `json:"server"`
		} `json:"acme"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type               string    `json:"type"`
			Status             string    `json:"status"`
			Reason             string    `json:"reason"`
			Message            string    `json:"message"`
			LastTransitionTime time.Time `json:"lastTransitionTime"`
		} `json:"conditions"`
	} `json:"status"`
}

func issuerView(item issuerDocument) TLSIssuer {
	value := TLSIssuer{Name: item.Metadata.Name, Email: item.Spec.ACME.Email, Server: item.Spec.ACME.Server, Conditions: []RuntimeCondition{}}
	for i, condition := range item.Status.Conditions {
		if i >= 16 {
			break
		}
		value.Conditions = append(value.Conditions, RuntimeCondition{Type: condition.Type, Status: condition.Status, Reason: condition.Reason, Message: condition.Message, LastTransitionTime: condition.LastTransitionTime})
	}
	if value.Conditions == nil {
		value.Conditions = []RuntimeCondition{}
	}
	if len(value.Conditions) > 16 {
		value.Conditions = value.Conditions[:16]
	}
	for _, condition := range value.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			value.Ready = true
		}
	}
	return value
}
func (c *Client) TLSIssuers(ctx context.Context) (TLSIssuers, error) {
	result := TLSIssuers{Items: []TLSIssuer{}}
	client := c.restClient()
	if client == nil {
		return result, fmt.Errorf("cert-manager API client unavailable")
	}
	data, err := client.Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers").Param("limit", "64").DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		result.Message = "cert-manager CRDs are not installed. Install the optional certificate module to request Let's Encrypt certificates."
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var list struct {
		Items    []issuerDocument `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	if len(data) > 1<<20 || json.Unmarshal(data, &list) != nil {
		return result, fmt.Errorf("cert-manager response exceeds supported bounds")
	}
	result.Installed = true
	for _, item := range list.Items {
		result.Items = append(result.Items, issuerView(item))
	}
	if list.Metadata.Continue != "" {
		result.Message = "Only the first 64 issuers are displayed."
	}
	return result, nil
}
func (c *Client) TLSIssuerExists(ctx context.Context, name string) error {
	if len(name) > 63 || len(validation.IsDNS1123Subdomain(name)) > 0 {
		return fmt.Errorf("invalid TLS issuer name")
	}
	client := c.restClient()
	if client == nil {
		return fmt.Errorf("cert-manager is unavailable")
	}
	_, err := client.Get().AbsPath("/apis/cert-manager.io/v1/clusterissuers/" + name).DoRaw(ctx)
	if err != nil {
		return fmt.Errorf("cert-manager ClusterIssuer is unavailable; install the optional module and create the issuer first")
	}
	return nil
}
func (c *Client) CreateTLSIssuer(ctx context.Context, name, email string, production bool) (TLSIssuer, error) {
	if len(name) > 63 || len(validation.IsDNS1123Subdomain(name)) > 0 {
		return TLSIssuer{}, fmt.Errorf("invalid TLS issuer name")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return TLSIssuer{}, fmt.Errorf("a valid ACME account email is required")
	}
	server := "https://acme-staging-v02.api.letsencrypt.org/directory"
	if production {
		server = "https://acme-v02.api.letsencrypt.org/directory"
	}
	ingress := map[string]any{"ingressClassName": c.options.IngressClass, "serviceType": "ClusterIP", "podTemplate": map[string]any{"spec": map[string]any{"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 1001}, "resources": map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "100m", "memory": "64Mi"}}}}}
	body := map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": name, "labels": map[string]string{managedBy: "hakopod"}}, "spec": map[string]any{"acme": map[string]any{"email": email, "server": server, "privateKeySecretRef": map[string]string{"name": "hp-acme-" + name}, "solvers": []any{map[string]any{"http01": map[string]any{"ingress": ingress}}}}}}
	data, _ := json.Marshal(body)
	client := c.restClient()
	if client == nil {
		return TLSIssuer{}, fmt.Errorf("cert-manager is unavailable")
	}
	response, err := client.Post().AbsPath("/apis/cert-manager.io/v1/clusterissuers").Body(data).DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		return TLSIssuer{}, fmt.Errorf("cert-manager CRDs are not installed")
	}
	if err != nil {
		return TLSIssuer{}, err
	}
	var document issuerDocument
	if json.Unmarshal(response, &document) != nil {
		return TLSIssuer{}, fmt.Errorf("invalid cert-manager response")
	}
	return issuerView(document), nil
}

func TLSIssuerProduction(server string) bool { return strings.Contains(server, "//acme-v02.") }

func (c *Client) applyTLSChallengePolicy(ctx context.Context, t Target) error {
	port := intstr.FromInt32(8089)
	tcp := corev1.ProtocolTCP
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-acme-http01", Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, "")}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"acme.cert-manager.io/http01-solver": "true"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "haproxy-controller"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": "hakopod-ingress"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}}}}}
	api := c.kube.NetworkingV1().NetworkPolicies(policy.Namespace)
	old, err := api.Get(ctx, policy.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, policy, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = owned(old, t); err != nil {
		return err
	}
	old.Spec = policy.Spec
	_, err = api.Update(ctx, old, metav1.UpdateOptions{})
	return err
}
