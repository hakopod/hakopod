package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A template cannot know a service's generated hostname, so an ingress mount
// without a hostname follows it.
func TestIngressMountWithoutHostnameFollowsTheGeneratedHostname(t *testing.T) {
	ctx := context.Background()
	c, kube, target, host := certificateOnlyTarget(t)
	svc := target.Spec.Services["tunnel"]
	svc.CertificateMounts = []spec.CertificateMount{{Source: "ingress", MountPath: "/certificates/tunnel"}}
	target.Spec.Services["tunnel"] = svc
	if _, err := spec.Normalize(target.Spec); err != nil {
		t.Fatal("an ingress mount without a hostname must be valid", err)
	}
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", svc); err != nil {
		t.Fatal(err)
	}
	ingress, err := kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{})
	if err != nil || len(ingress.Spec.TLS) == 0 {
		t.Fatal("certificate-only ingress missing", err)
	}
	cert, key := testTLSCertificate(t, host, time.Now().Add(time.Hour))
	if _, err = kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ingress.Spec.TLS[0].SecretName}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	resolved, err := c.resolveBackendCertificates(ctx, target, "tunnel", svc, true)
	if err != nil {
		t.Fatal(err)
	}
	mount := resolved.CertificateMounts[0]
	if mount.Hostname != host || !strings.HasPrefix(mount.Certificate, "hp-auto-cert-") {
		t.Fatalf("mount was not resolved to the generated hostname: %+v", mount)
	}
	if svc.CertificateMounts[0].Hostname != "" {
		t.Fatal("resolution must not change the accepted specification")
	}
}

func TestOnlyIngressMountsMayOmitTheHostname(t *testing.T) {
	_, _, target, _ := certificateOnlyTarget(t)
	svc := target.Spec.Services["tunnel"]
	svc.CertificateMounts = []spec.CertificateMount{{Certificate: "uploaded-cert", MountPath: "/certificates/tunnel"}}
	target.Spec.Services["tunnel"] = svc
	if _, err := spec.Normalize(target.Spec); err == nil {
		t.Fatal("an uploaded certificate mount without a hostname was accepted")
	}
}
