package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const backendCertificateKey = "hakopod.io/backend-certificate"
const backendCertificateHostname = "hakopod.io/certificate-hostname"
const backendCertificateSource = "hakopod.io/certificate-source"

type BackendCertificateStatus struct {
	Certificate string     `json:"certificate"`
	Hostname    string     `json:"hostname"`
	MountPath   string     `json:"mount_path,omitempty"`
	Source      string     `json:"source"`
	Ready       bool       `json:"ready"`
	NotBefore   *time.Time `json:"not_before,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Message     string     `json:"message,omitempty"`
}

func backendCertificateView(secret *corev1.Secret, hostname string) BackendCertificateStatus {
	value := BackendCertificateStatus{Certificate: secret.Name, Hostname: hostname, Source: secret.Annotations[backendCertificateSource]}
	// Preserve dates for expired certificates so the inbox and UI can explain
	// what needs rotation, even when current validity checks fail.
	if certificate := secret.Data[corev1.TLSCertKey]; len(certificate) <= 256<<10 {
		if block, _ := pem.Decode(certificate); block != nil && block.Type == "CERTIFICATE" {
			if leaf, err := x509.ParseCertificate(block.Bytes); err == nil {
				value.NotBefore, value.ExpiresAt = &leaf.NotBefore, &leaf.NotAfter
			}
		}
	}
	leaf, err := validateTLSCertificate(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey], hostname, time.Now())
	if err != nil {
		value.Message = err.Error()
		return value
	}
	value.Ready, value.NotBefore, value.ExpiresAt = true, &leaf.NotBefore, &leaf.NotAfter
	return value
}

func checkBackendCertificate(secret *corev1.Secret, t Target, service, hostname string) error {
	if err := owned(secret, t); err != nil {
		return err
	}
	if secret.Type != corev1.SecretTypeTLS || secret.Immutable == nil || !*secret.Immutable || secret.Labels[serviceKey] != service || secret.Labels[backendCertificateKey] != "true" || secret.Annotations[backendCertificateHostname] != hostname {
		return fmt.Errorf("backend certificate ownership, hostname or immutability mismatch")
	}
	_, err := validateTLSCertificate(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey], hostname, time.Now())
	return err
}

func (c *Client) PutBackendCertificate(ctx context.Context, t Target, service, hostname string, certificate, key []byte) (BackendCertificateStatus, error) {
	return c.putBackendCertificate(ctx, t, service, hostname, certificate, key, "upload")
}

func (c *Client) putBackendCertificate(ctx context.Context, t Target, service, hostname string, certificate, key []byte, source string) (BackendCertificateStatus, error) {
	if _, ok := t.Spec.Services[service]; !ok || t.ApplicationID == "" || !spec.ValidHostname(hostname) {
		return BackendCertificateStatus{}, fmt.Errorf("backend certificates require an existing service and a lowercase DNS hostname")
	}
	if _, err := validateTLSCertificate(certificate, key, hostname, time.Now()); err != nil {
		return BackendCertificateStatus{}, err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return BackendCertificateStatus{}, fmt.Errorf("application namespace is unavailable; deploy the application privately before uploading certificates")
	}
	if err = owned(ns, t); err != nil {
		return BackendCertificateStatus{}, err
	}
	hasher := sha256.New()
	hasher.Write([]byte(service + "\x00" + hostname + "\x00"))
	hasher.Write(certificate)
	hash := hasher.Sum(nil)
	shortService := service
	if len(shortService) > 22 {
		shortService = shortService[:22]
	}
	name := fmt.Sprintf("hp-cert-%s-%x", shortService, hash[:16])
	api := c.kube.CoreV1().Secrets(ns.Name)
	// One retry handles a concurrent upload without unbounded recursion.
	for attempt := 0; attempt < 2; attempt++ {
		existing, getErr := api.Get(ctx, name, metav1.GetOptions{})
		if getErr == nil {
			if err = checkBackendCertificate(existing, t, service, hostname); err != nil {
				return BackendCertificateStatus{}, err
			}
			return backendCertificateView(existing, hostname), nil
		}
		if !apierrors.IsNotFound(getErr) {
			return BackendCertificateStatus{}, fmt.Errorf("backend certificate storage is unavailable")
		}
		items, listErr := api.List(ctx, metav1.ListOptions{LabelSelector: backendCertificateSelector(t, service) + "," + automaticCertificateKey + "!=true", Limit: 64})
		if listErr != nil {
			return BackendCertificateStatus{}, fmt.Errorf("backend certificate storage is unavailable")
		}
		if len(items.Items) >= 64 || items.Continue != "" {
			return BackendCertificateStatus{}, fmt.Errorf("service already has 64 backend certificates; an operator must retire unused historical certificates")
		}
		labels := labelsFor(t, service)
		labels[backendCertificateKey] = "true"
		created, createErr := api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name, Labels: labels, Annotations: map[string]string{backendCertificateHostname: hostname, backendCertificateSource: source}}, Immutable: ptr(true), Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
		if createErr == nil {
			return backendCertificateView(created, hostname), nil
		}
		if !apierrors.IsAlreadyExists(createErr) {
			return BackendCertificateStatus{}, fmt.Errorf("backend certificate could not be stored")
		}
	}
	return BackendCertificateStatus{}, fmt.Errorf("backend certificate changed concurrently; retry the upload")
}

func backendCertificateSelector(t Target, service string) string {
	return managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service + "," + backendCertificateKey + "=true"
}

// ImportBackendIngressCertificate snapshots this service's active ingress
// certificate. Callers cannot name arbitrary namespaces, Secrets or services.
func (c *Client) ImportBackendIngressCertificate(ctx context.Context, t Target, service, hostname string) (BackendCertificateStatus, error) {
	secret, err := c.backendIngressSource(ctx, t, service, hostname)
	if err != nil {
		return BackendCertificateStatus{}, err
	}
	return c.putBackendCertificate(ctx, t, service, hostname, secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey], "ingress")
}

// A service published only over raw TCP keeps an owned ingress that carries a
// hostname and TLS but no HTTP backend, so its automatic certificate mount
// follows cert-manager renewals exactly like a public HTTP service does.
func certificateOnlyIngress(svc spec.Service) bool {
	if svc.Public || len(svc.PublicTCP) == 0 {
		return false
	}
	for _, mount := range svc.CertificateMounts {
		if mount.Source == "ingress" {
			return true
		}
	}
	return false
}

// The owned ingress chooses the source; callers never name arbitrary Secrets.
func (c *Client) backendIngressSource(ctx context.Context, t Target, service, hostname string) (*corev1.Secret, error) {
	svc, ok := t.Spec.Services[service]
	// A service published only over raw TCP terminates TLS itself and has no
	// HTTP backend, but it still owns a certificate-only ingress to read from.
	if !ok || (!svc.Public && len(svc.PublicTCP) == 0) || !spec.ValidHostname(hostname) {
		return nil, fmt.Errorf("ingress import requires this service's public HTTP or public TCP certificate")
	}
	ingress, err := c.kube.NetworkingV1().Ingresses(Namespace(t.ApplicationID)).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("service ingress is unavailable")
	}
	if err = owned(ingress, t); err != nil {
		return nil, err
	}
	if ingress.Labels[serviceKey] != service {
		return nil, fmt.Errorf("ingress is not owned by this service")
	}
	for _, tls := range ingress.Spec.TLS {
		for _, host := range tls.Hosts {
			if host != hostname || tls.SecretName == "" {
				continue
			}
			secret, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, tls.SecretName, metav1.GetOptions{})
			if err != nil || secret.Type != corev1.SecretTypeTLS {
				return nil, fmt.Errorf("ingress certificate has not been issued or uploaded")
			}
			if _, err = validateTLSCertificate(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey], hostname, time.Now()); err != nil {
				return nil, err
			}
			return secret, nil
		}
	}
	return nil, fmt.Errorf("hostname is not covered by this service's configured ingress TLS")
}

func (c *Client) BackendCertificates(ctx context.Context, t Target, service string) ([]BackendCertificateStatus, error) {
	svc, ok := t.Spec.Services[service]
	if !ok {
		return nil, fmt.Errorf("service not found")
	}
	items, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: backendCertificateSelector(t, service) + "," + automaticCertificateKey + "!=true", Limit: 64})
	if err != nil {
		return nil, fmt.Errorf("backend certificate observations are unavailable")
	}
	result := make([]BackendCertificateStatus, 0, len(items.Items))
	seen := map[string]bool{}
	for _, secret := range items.Items {
		hostname := secret.Annotations[backendCertificateHostname]
		value := backendCertificateView(&secret, hostname)
		if err := checkBackendCertificate(&secret, t, service, hostname); err != nil {
			value.Ready, value.Message = false, err.Error()
		}
		for _, mount := range svc.CertificateMounts {
			if mount.Certificate == secret.Name {
				value.MountPath = mount.MountPath
				if mount.Hostname != hostname {
					value.Ready, value.Message = false, "Mounted hostname differs from the uploaded certificate reference."
				}
			}
		}
		result = append(result, value)
		seen[secret.Name] = true
	}
	for _, mount := range svc.CertificateMounts {
		if mount.Source == "ingress" {
			value := BackendCertificateStatus{Hostname: mount.Hostname, MountPath: mount.MountPath, Source: "ingress-auto"}
			secret, sourceErr := c.backendIngressSource(ctx, t, service, mount.Hostname)
			if sourceErr != nil {
				value.Message = sourceErr.Error()
			} else {
				value = backendCertificateView(secret, mount.Hostname)
				value.Certificate = automaticCertificateName(service, mount.Hostname, secret.Data[corev1.TLSCertKey])
				value.Source, value.MountPath = "ingress-auto", mount.MountPath
			}
			result = append(result, value)
			continue
		}
		if !seen[mount.Certificate] {
			value := BackendCertificateStatus{Certificate: mount.Certificate, Hostname: mount.Hostname, MountPath: mount.MountPath, Source: "unknown", Message: "Certificate is missing or is not owned by this service."}
			// A concurrent upload can cross the list page boundary. Inspect at
			// most four active references so an old mounted certificate is not
			// reported missing merely because history filled that page.
			secret, getErr := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, mount.Certificate, metav1.GetOptions{})
			if getErr == nil && owned(secret, t) == nil && secret.Labels[serviceKey] == service && secret.Labels[backendCertificateKey] == "true" {
				value = backendCertificateView(secret, mount.Hostname)
				value.MountPath = mount.MountPath
				if err = checkBackendCertificate(secret, t, service, mount.Hostname); err != nil {
					value.Ready, value.Message = false, err.Error()
				}
			}
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Certificate < result[j].Certificate })
	return result, nil
}

func (c *Client) ValidateBackendCertificates(ctx context.Context, t Target) error {
	for _, service := range spec.Names(t.Spec) {
		if err := c.prepareBackendCertificates(ctx, t, service, t.Spec.Services[service]); err != nil {
			return fmt.Errorf("%s: %w", service, err)
		}
	}
	return nil
}

func (c *Client) prepareBackendCertificates(ctx context.Context, t Target, service string, svc spec.Service) error {
	_, err := c.resolveBackendCertificates(ctx, t, service, svc, false)
	return err
}

func applyBackendCertificateMounts(svc spec.Service, pod *corev1.PodSpec) {
	for index, mount := range svc.CertificateMounts {
		name := fmt.Sprintf("backend-certificate-%d", index)
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: mount.Certificate, DefaultMode: ptr(int32(0440)), Items: []corev1.KeyToPath{{Key: corev1.TLSCertKey, Path: "tls.crt"}, {Key: corev1.TLSPrivateKeyKey, Path: "tls.key"}}}}})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: name, MountPath: mount.MountPath, ReadOnly: true})
	}
}
