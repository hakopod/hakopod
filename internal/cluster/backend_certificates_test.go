package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBackendCertificatesOwnershipRotationAndMounts(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	client := &Client{kube: fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})}
	hostname := "smtp.example.com"
	cert, key := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	uploaded, err := client.PutBackendCertificate(ctx, target, "api", hostname, cert, key)
	if err != nil || !uploaded.Ready {
		t.Fatalf("upload failed: %+v %v", uploaded, err)
	}
	if again, err := client.PutBackendCertificate(ctx, target, "api", hostname, cert, key); err != nil || again.Certificate != uploaded.Certificate {
		t.Fatal("repeat upload did not reuse immutable certificate")
	}
	svc := target.Spec.Services["api"]
	svc.CertificateMounts = []spec.CertificateMount{{Certificate: uploaded.Certificate, Hostname: hostname, MountPath: "/certs/smtp"}}
	svc.FSGroup = 2001
	target.Spec.Services["api"] = svc
	if err = client.ValidateBackendCertificates(ctx, target); err != nil {
		t.Fatal(err)
	}
	other := svc
	if err = client.prepareBackendCertificates(ctx, target, "web", other); err == nil {
		t.Fatal("another service can mount a private key")
	}
	foreign := target
	foreign.ApplicationID = "other-application"
	if err = client.ValidateBackendCertificates(ctx, foreign); err == nil {
		t.Fatal("another application can mount a private key")
	}
	other.CertificateMounts = []spec.CertificateMount{{Certificate: uploaded.Certificate, Hostname: "other.example.com", MountPath: "/certs/smtp"}}
	if err = client.prepareBackendCertificates(ctx, target, "api", other); err == nil {
		t.Fatal("wrong hostname accepted")
	}
	wanted := deployment(target, "api", svc, time.Minute)
	// The helper is also tested alone so its mount contract stays explicit.
	pod := corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	applyBackendCertificateMounts(svc, &pod)
	volume, mount := pod.Volumes[0], pod.Containers[0].VolumeMounts[0]
	if volume.Secret.SecretName != uploaded.Certificate || *volume.Secret.DefaultMode != 0440 || !mount.ReadOnly || mount.SubPath != "" || mount.MountPath != "/certs/smtp" || *wanted.Spec.Template.Spec.SecurityContext.FSGroup != 2001 {
		t.Fatal("certificate mount permissions are unsafe")
	}
	cert2, key2 := testTLSCertificate(t, hostname, time.Now().Add(2*time.Hour))
	rotated, err := client.PutBackendCertificate(ctx, target, "api", hostname, cert2, key2)
	if err != nil || rotated.Certificate == uploaded.Certificate {
		t.Fatal("rotation did not create a fresh immutable reference", err)
	}
	if err = client.ValidateBackendCertificates(ctx, target); err != nil {
		t.Fatal("rotation removed the previous certificate needed for rollback", err)
	}
	statuses, err := client.BackendCertificates(ctx, target, "api")
	if err != nil || len(statuses) != 2 {
		t.Fatal("certificate observations missing", err)
	}
	encoded, _ := json.Marshal(statuses)
	if strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), "BEGIN CERTIFICATE") {
		t.Fatal("certificate status leaked PEM data")
	}
	for _, status := range statuses {
		if status.Certificate == uploaded.Certificate && status.MountPath != "/certs/smtp" {
			t.Fatal("status omitted mount location")
		}
	}
	secret, _ := client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, uploaded.Certificate, metav1.GetOptions{})
	secret.Immutable = ptr(false)
	_, _ = client.kube.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err = client.ValidateBackendCertificates(ctx, target); err == nil {
		t.Fatal("mutable certificate accepted for deployment")
	}
}

func TestBackendCertificateValidationAndIngressSnapshot(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	client := &Client{kube: fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}}), options: Options{AppDomain: "example.com"}}
	hostname := client.hostname(target, "web")
	cert, key := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	if _, err := client.PutBackendCertificate(ctx, target, "api", "other.example.com", cert, key); err == nil {
		t.Fatal("wrong hostname accepted")
	}
	expiredCert, expiredKey := testTLSCertificate(t, hostname, time.Now().Add(-time.Second))
	if _, err := client.PutBackendCertificate(ctx, target, "api", hostname, expiredCert, expiredKey); err == nil {
		t.Fatal("expired certificate accepted")
	}
	_, wrongKey := testTLSCertificate(t, hostname, time.Now().Add(time.Hour))
	if _, err := client.PutBackendCertificate(ctx, target, "api", hostname, cert, wrongKey); err == nil {
		t.Fatal("mismatched private key accepted")
	}
	_, _ = client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager-source"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Labels: labelsFor(target, "web")}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{hostname}, SecretName: "cert-manager-source"}}}}
	_, _ = client.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Create(ctx, ingress, metav1.CreateOptions{})
	snapshot, err := client.ImportBackendIngressCertificate(ctx, target, "web", hostname)
	if err != nil || snapshot.Source != "ingress" || !snapshot.Ready {
		t.Fatal("own managed ingress certificate import failed", err)
	}
	if _, err = client.ImportBackendIngressCertificate(ctx, target, "api", hostname); err == nil {
		t.Fatal("private service imported another service's ingress certificate")
	}
	if _, err = client.ImportBackendIngressCertificate(ctx, target, "web", "other.example.com"); err == nil {
		t.Fatal("unconfigured hostname accepted")
	}
	renewedCert, renewedKey := testTLSCertificate(t, hostname, time.Now().Add(2*time.Hour))
	source, _ := client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "cert-manager-source", metav1.GetOptions{})
	source.Data[corev1.TLSCertKey], source.Data[corev1.TLSPrivateKeyKey] = renewedCert, renewedKey
	if _, err = client.kube.CoreV1().Secrets(source.Namespace).Update(ctx, source, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	renewed, err := client.ImportBackendIngressCertificate(ctx, target, "web", hostname)
	if err != nil || renewed.Certificate == snapshot.Certificate {
		t.Fatal("managed renewal did not create a new snapshot", err)
	}
	previous, _ := client.kube.CoreV1().Secrets(source.Namespace).Get(ctx, snapshot.Certificate, metav1.GetOptions{})
	if string(previous.Data[corev1.TLSCertKey]) != string(cert) {
		t.Fatal("managed renewal silently changed the old backend certificate")
	}
	if err = client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Delete(ctx, "cert-manager-source", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, snapshot.Certificate, metav1.GetOptions{}); err != nil {
		t.Fatal("source deletion removed immutable snapshot")
	}
}
