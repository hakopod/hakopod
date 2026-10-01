package cluster

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/mail"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func tlsIssuerKind(kind string) string {
	if kind == "" {
		return "ClusterIssuer"
	}
	return kind
}
func issuerNameValid(name string) bool {
	return name != "" && len(name) <= 63 && len(validation.IsDNS1123Label(name)) == 0
}
func (c *Client) issuerNamespace(ctx context.Context, t Target) error {
	if t.ApplicationID == "" {
		return fmt.Errorf("an existing application is required for certificate issuers")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("application namespace is unavailable")
	}
	return owned(ns, t)
}
func (c *Client) getIssuer(ctx context.Context, path string) (issuerDocument, error) {
	var result issuerDocument
	client := c.restClient()
	if client == nil {
		return result, fmt.Errorf("cert-manager is unavailable")
	}
	raw, err := client.Get().AbsPath(path).DoRaw(ctx)
	if err != nil {
		return result, err
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &result) != nil {
		return result, fmt.Errorf("invalid cert-manager response")
	}
	return result, nil
}
func applicationIssuerPath(t Target) string {
	return "/apis/cert-manager.io/v1/namespaces/" + Namespace(t.ApplicationID) + "/issuers"
}

// ValidateTLSIssuer applies the same scope rules to attachment, planning and reconciliation.
func (c *Client) ValidateTLSIssuer(ctx context.Context, t Target, name, kind string) error {
	kind = tlsIssuerKind(kind)
	if !issuerNameValid(name) || (kind != "Issuer" && kind != "ClusterIssuer") {
		return fmt.Errorf("invalid TLS issuer reference")
	}
	if kind == "ClusterIssuer" {
		if c.options.DeploymentMode == DeploymentManagedCloud && name != c.options.TLSIssuer {
			return fmt.Errorf("only the configured default ClusterIssuer is available in Cloud")
		}
		return c.TLSIssuerExists(ctx, name)
	}
	if err := c.issuerNamespace(ctx, t); err != nil {
		return err
	}
	item, err := c.getIssuer(ctx, applicationIssuerPath(t)+"/"+name)
	if err != nil {
		return fmt.Errorf("application certificate issuer is unavailable")
	}
	if item.Metadata.Namespace != Namespace(t.ApplicationID) {
		return fmt.Errorf("certificate issuer namespace mismatch")
	}
	return owned(&item.Metadata, t)
}

// DefaultTLSIssuers does not expose installation contact details or conditions.
func (c *Client) DefaultTLSIssuers(ctx context.Context) (TLSIssuers, error) {
	result := TLSIssuers{Items: []TLSIssuer{}}
	if c.options.TLSIssuer == "" {
		result.Message = "No default certificate issuer is configured."
		var err error
		result.Installed, err = c.certificateAPIInstalled(ctx)
		return result, err
	}
	item, err := c.getIssuer(ctx, "/apis/cert-manager.io/v1/clusterissuers/"+c.options.TLSIssuer)
	if apierrors.IsNotFound(err) {
		result.Message = "The default certificate issuer is unavailable."
		result.Installed, err = c.certificateAPIInstalled(ctx)
		return result, err
	}
	if err != nil {
		return result, err
	}
	view := issuerView(item)
	view.Kind = "ClusterIssuer"
	view.Default = true
	view.Email = ""
	view.Conditions = []RuntimeCondition{}
	result.Installed = true
	result.Items = append(result.Items, view)
	return result, nil
}

// Discover API availability without listing other tenants' issuer resources.
func (c *Client) certificateAPIInstalled(ctx context.Context) (bool, error) {
	client := c.restClient()
	if client == nil {
		return false, fmt.Errorf("cert-manager is unavailable")
	}
	raw, err := client.Get().AbsPath("/apis/cert-manager.io/v1").DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var resources metav1.APIResourceList
	if len(raw) > 1<<20 || json.Unmarshal(raw, &resources) != nil {
		return false, fmt.Errorf("invalid cert-manager discovery response")
	}
	for _, resource := range resources.APIResources {
		if resource.Name == "issuers" && resource.Namespaced {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) ApplicationTLSIssuers(ctx context.Context, t Target) (TLSIssuers, error) {
	var result TLSIssuers
	var err error
	if err = c.issuerNamespace(ctx, t); err != nil {
		return TLSIssuers{}, err
	}
	if c.options.DeploymentMode == DeploymentManagedCloud {
		result, err = c.DefaultTLSIssuers(ctx)
	} else {
		result, err = c.TLSIssuers(ctx)
	}
	if err != nil {
		return result, err
	}
	client := c.restClient()
	if client == nil {
		return result, fmt.Errorf("cert-manager is unavailable")
	}
	raw, err := client.Get().AbsPath(applicationIssuerPath(t)).Param("labelSelector", managedBy+"=hakopod,"+ownerKey+"="+ownerID(t.ApplicationID)).Param("limit", "16").DoRaw(ctx)
	if apierrors.IsNotFound(err) {
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
	if len(raw) > 1<<20 || json.Unmarshal(raw, &list) != nil || len(list.Items) > 16 {
		return result, fmt.Errorf("certificate issuer response exceeds supported bounds")
	}
	result.Installed = true
	for _, item := range list.Items {
		if item.Metadata.Namespace != Namespace(t.ApplicationID) || owned(&item.Metadata, t) != nil {
			continue
		}
		view := issuerView(item)
		view.Kind = "Issuer"
		result.Items = append(result.Items, view)
	}
	if list.Metadata.Continue != "" {
		result.Message = "Only the first 16 application issuers are displayed."
	}
	return result, nil
}

func applicationACMESpec(email, ingressClass string, production bool, secret string) map[string]any {
	server := "https://acme-staging-v02.api.letsencrypt.org/directory"
	if production {
		server = "https://acme-v02.api.letsencrypt.org/directory"
	}
	ingress := map[string]any{"ingressClassName": ingressClass, "serviceType": "ClusterIP", "podTemplate": map[string]any{"spec": map[string]any{"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 1001, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "resources": map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "100m", "memory": "64Mi"}}}}}
	return map[string]any{"acme": map[string]any{"email": email, "server": server, "privateKeySecretRef": map[string]string{"name": secret}, "solvers": []any{map[string]any{"http01": map[string]any{"ingress": ingress}}}}}
}

// Older managed issuers omitted seccompProfile. Accept that sole difference on
// retry without changing the stored issuer or weakening other immutable fields.
func applicationIssuerSpecMatches(existing, desired any) bool {
	if reflect.DeepEqual(existing, desired) {
		return true
	}
	// Clone the generated spec so compatibility does not alter the new default.
	raw, err := json.Marshal(desired)
	if err != nil {
		return false
	}
	var legacy map[string]any
	if json.Unmarshal(raw, &legacy) != nil {
		return false
	}
	acme, ok := legacy["acme"].(map[string]any)
	if !ok {
		return false
	}
	solvers, ok := acme["solvers"].([]any)
	if !ok || len(solvers) != 1 {
		return false
	}
	solver, ok := solvers[0].(map[string]any)
	if !ok {
		return false
	}
	var current any = solver
	for _, key := range []string{"http01", "ingress", "podTemplate", "spec", "securityContext"} {
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current = object[key]
	}
	security, ok := current.(map[string]any)
	if !ok {
		return false
	}
	delete(security, "seccompProfile")
	return reflect.DeepEqual(existing, legacy)
}

// Callers serialize creation per application across processes before entering this
// method so the 16 issuer limit also holds under concurrent requests.
func (c *Client) CreateApplicationTLSIssuer(ctx context.Context, t Target, name, email string, production bool) (TLSIssuer, error) {
	if !issuerNameValid(name) {
		return TLSIssuer{}, fmt.Errorf("invalid TLS issuer name")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return TLSIssuer{}, fmt.Errorf("a valid ACME account email is required")
	}
	if err = c.issuerNamespace(ctx, t); err != nil {
		return TLSIssuer{}, err
	}
	client := c.restClient()
	if client == nil {
		return TLSIssuer{}, fmt.Errorf("cert-manager is unavailable")
	}
	hash := sha256.Sum256([]byte(name))
	secretName := fmt.Sprintf("hp-acme-%x", hash[:16])
	desired := applicationACMESpec(email, c.options.IngressClass, production, secretName)
	// Compare canonical JSON maps, preserving immutability even if the caller retries.
	desiredRaw, _ := json.Marshal(desired)
	var desiredMap any
	_ = json.Unmarshal(desiredRaw, &desiredMap)
	raw, err := client.Get().AbsPath(applicationIssuerPath(t) + "/" + name).DoRaw(ctx)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return TLSIssuer{}, err
	}
	var existing issuerDocument
	if exists {
		var document struct {
			Spec any `json:"spec"`
		}
		if len(raw) > 1<<20 || json.Unmarshal(raw, &existing) != nil || json.Unmarshal(raw, &document) != nil {
			return TLSIssuer{}, fmt.Errorf("invalid cert-manager response")
		}
		if existing.Metadata.Namespace != Namespace(t.ApplicationID) || owned(&existing.Metadata, t) != nil {
			return TLSIssuer{}, fmt.Errorf("certificate issuer is not owned by this application")
		}
		if !applicationIssuerSpecMatches(document.Spec, desiredMap) {
			return TLSIssuer{}, fmt.Errorf("certificate issuer configuration is immutable; choose a new name")
		}
	} else {
		raw, err = client.Get().AbsPath(applicationIssuerPath(t)).Param("labelSelector", managedBy+"=hakopod,"+ownerKey+"="+ownerID(t.ApplicationID)).Param("limit", "16").DoRaw(ctx)
		if err != nil {
			return TLSIssuer{}, err
		}
		var list struct {
			Items    []json.RawMessage `json:"items"`
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
		}
		if len(raw) > 1<<20 || json.Unmarshal(raw, &list) != nil {
			return TLSIssuer{}, fmt.Errorf("invalid cert-manager response")
		}
		if len(list.Items) >= 16 || list.Metadata.Continue != "" {
			return TLSIssuer{}, fmt.Errorf("application already has 16 certificate issuers")
		}
	}
	secrets := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	secret, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if exists {
			return TLSIssuer{}, fmt.Errorf("certificate issuer account secret is unavailable")
		}
		// Partial failures can leave a reserved key; count those too so retries
		// with different names cannot create unbounded orphan account secrets.
		reserved, e := secrets.List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + ",hakopod.io/acme-issuer", Limit: 16})
		if e != nil {
			return TLSIssuer{}, e
		}
		if len(reserved.Items) >= 16 || reserved.Continue != "" {
			return TLSIssuer{}, fmt.Errorf("application already has 16 certificate issuer account keys")
		}
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			return TLSIssuer{}, e
		}
		labels := labelsFor(t, "")
		labels["hakopod.io/acme-issuer"] = name
		secret, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: Namespace(t.ApplicationID), Labels: labels}, Type: corev1.SecretTypeOpaque, Immutable: ptr(true), Data: map[string][]byte{corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}}, metav1.CreateOptions{})
	}
	if err != nil {
		return TLSIssuer{}, err
	}
	if owned(secret, t) != nil || secret.Labels["hakopod.io/acme-issuer"] != name || secret.Type != corev1.SecretTypeOpaque || secret.Immutable == nil || !*secret.Immutable || len(secret.Data[corev1.TLSPrivateKeyKey]) == 0 {
		return TLSIssuer{}, fmt.Errorf("certificate issuer account secret ownership mismatch")
	}
	if exists {
		view := issuerView(existing)
		view.Kind = "Issuer"
		return view, nil
	}
	body := map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Issuer", "metadata": map[string]any{"name": name, "namespace": Namespace(t.ApplicationID), "labels": labelsFor(t, "")}, "spec": desired}
	raw, _ = json.Marshal(body)
	raw, err = client.Post().AbsPath(applicationIssuerPath(t)).Body(raw).DoRaw(ctx)
	if err != nil {
		return TLSIssuer{}, err
	}
	var result issuerDocument
	if len(raw) > 1<<20 || json.Unmarshal(raw, &result) != nil {
		return TLSIssuer{}, fmt.Errorf("invalid cert-manager response")
	}
	view := issuerView(result)
	view.Kind = "Issuer"
	return view, nil
}
