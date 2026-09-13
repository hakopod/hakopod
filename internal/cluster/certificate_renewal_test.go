package cluster

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func automaticCertificateFixture(t *testing.T) (*Client, Target, *corev1.Secret) {
	t.Helper()
	target := testTarget(t)
	svc := target.Spec.Services["web"]
	svc.CertificateMounts = []spec.CertificateMount{{Source: "ingress", Hostname: "mail.example.com", MountPath: "/certificates/smtp"}}
	target.Spec.Services["web"] = svc
	target.Spec.Domains = map[string]string{"mail.example.com": "web"}
	cert, key := testTLSCertificate(t, "mail.example.com", time.Now().Add(time.Hour))
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ingress-certificate", Namespace: Namespace(target.ApplicationID)}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: source.Namespace, Labels: labelsFor(target, "web")}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"mail.example.com"}, SecretName: source.Name}}}}
	c := &Client{kube: fake.NewClientset(source, ingress), options: Options{TLSIssuer: "test-issuer"}}
	return c, target, source
}

func TestAutomaticCertificateRenewal(t *testing.T) {
	ctx := context.Background()
	c, target, source := automaticCertificateFixture(t)
	svc := target.Spec.Services["web"]
	resolved, err := c.resolveBackendCertificates(ctx, target, "web", svc, false)
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := c.kube.CoreV1().Secrets(source.Namespace).List(ctx, metav1.ListOptions{})
	if len(secrets.Items) != 1 || svc.CertificateMounts[0].Certificate != "" {
		t.Fatal("planning changed secrets or application spec")
	}
	resolved, err = c.resolveBackendCertificates(ctx, target, "web", svc, true)
	if err != nil {
		t.Fatal(err)
	}
	first := resolved.CertificateMounts[0].Certificate
	d := deployment(target, "web", resolved, time.Minute)
	d.Spec.Template.Annotations = map[string]string{"operator.example/annotation": "retain"}
	d.Spec.Replicas = ptr(int32(3))
	d, err = c.kube.AppsV1().Deployments(source.Namespace).Create(ctx, d, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := d.DeepCopy()
	events := 0
	emit := func(e Event) {
		events++
		if e.Type != "certificate_renewed" {
			t.Fatal(e)
		}
	}
	if err = c.RenewBackendCertificates(ctx, target, emit); err != nil || events != 0 {
		t.Fatal("unchanged certificate triggered rollout", err)
	}
	cert, key := testTLSCertificate(t, "mail.example.com", time.Now().Add(2*time.Hour))
	source.Data = map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}
	source, err = c.kube.CoreV1().Secrets(source.Namespace).Update(ctx, source, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if healthy, msg, err := c.serviceDeliveryHealthy(ctx, target, "web", d); err != nil || healthy || msg == "" {
		t.Fatal("renewal pending reported healthy", healthy, msg, err)
	}
	denied := target
	denied.BeforeStep = func(context.Context) error { return errors.New("claim lost") }
	if err = c.RenewBackendCertificates(ctx, denied, emit); err == nil {
		t.Fatal("renewal ignored claim loss")
	}
	if err = c.RenewBackendCertificates(ctx, target, emit); err != nil || events != 1 {
		t.Fatal("renewal failed", err, events)
	}
	after, _ := c.kube.AppsV1().Deployments(source.Namespace).Get(ctx, "web", metav1.GetOptions{})
	second := automaticCertificateName("web", "mail.example.com", cert)
	if !backendMountsPrepared(after.Spec.Template.Spec, func() spec.Service {
		v := svc
		v.CertificateMounts = append([]spec.CertificateMount(nil), svc.CertificateMounts...)
		v.CertificateMounts[0].Certificate = second
		return v
	}()) {
		t.Fatal("renewed snapshot not mounted")
	}
	copy := after.DeepCopy()
	for i, v := range copy.Spec.Template.Spec.Volumes {
		if v.Name == "backend-certificate-0" {
			copy.Spec.Template.Spec.Volumes[i].Secret.SecretName = first
		}
	}
	if !reflect.DeepEqual(before.Spec, copy.Spec) || !reflect.DeepEqual(before.Annotations, copy.Annotations) {
		t.Fatal("renewal changed unrelated deployment settings")
	}
	if _, err = c.kube.CoreV1().Secrets(source.Namespace).Get(ctx, first, metav1.GetOptions{}); err != nil {
		t.Fatal("renewal deleted previous mounted certificate", err)
	}
	// Bad source data must not touch a running pod or its valid immutable copy.
	source.Data[corev1.TLSPrivateKeyKey] = []byte("invalid")
	_, _ = c.kube.CoreV1().Secrets(source.Namespace).Update(ctx, source, metav1.UpdateOptions{})
	if err = c.RenewBackendCertificates(ctx, target, emit); err == nil {
		t.Fatal("invalid renewal accepted")
	}
	unchanged, _ := c.kube.AppsV1().Deployments(source.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if !reflect.DeepEqual(after.Spec, unchanged.Spec) || events != 1 {
		t.Fatal("invalid renewal changed deployment")
	}
	if healthy, _, _ := c.serviceDeliveryHealthy(ctx, target, "web", unchanged); healthy {
		t.Fatal("invalid source hidden from health")
	}
}

func TestAutomaticCertificateModeAndOwnership(t *testing.T) {
	ctx := context.Background()
	c, target, _ := automaticCertificateFixture(t)
	c.options.DeploymentMode = DeploymentManagedCloud
	c.kube.(*fake.Clientset).ClearActions()
	if err := c.ValidateBackendCertificates(ctx, target); err == nil {
		t.Fatal("cloud automatic source accepted")
	}
	if len(c.kube.(*fake.Clientset).Actions()) != 0 {
		t.Fatal("cloud gate ran after Kubernetes access")
	}
	c.options.DeploymentMode = DeploymentSelfHosted
	foreign := target
	foreign.ApplicationID = "foreign"
	if err := c.ValidateBackendCertificates(ctx, foreign); err == nil {
		t.Fatal("foreign namespace accepted")
	}
	wrong := target.Spec.Services["web"]
	wrong.CertificateMounts[0].Hostname = "another.example.com"
	target.Spec.Services["web"] = wrong
	if err := c.ValidateBackendCertificates(ctx, target); err == nil {
		t.Fatal("foreign hostname accepted")
	}
}

func TestAutomaticCertificatePruningRetainsWorkloadReferences(t *testing.T) {
	ctx := context.Background()
	c, target, source := automaticCertificateFixture(t)
	makeSecret := func(name string) *corev1.Secret {
		labels := labelsFor(target, "web")
		labels[backendCertificateKey], labels[automaticCertificateKey] = "true", "true"
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: source.Namespace, Labels: labels}, Immutable: ptr(true), Type: corev1.SecretTypeTLS}
	}
	pod := func(name string) corev1.PodSpec {
		return corev1.PodSpec{Volumes: []corev1.Volume{{Name: "cert", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}}}}
	}
	objects := []runtime.Object{makeSecret("old-unused"), makeSecret("rs-cert"), makeSecret("pod-cert"), makeSecret("desired-cert"), &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old-replica", Namespace: source.Namespace}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: pod("rs-cert")}}}, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "terminating", Namespace: source.Namespace}, Spec: pod("pod-cert")}}
	for _, o := range objects {
		if err := c.kube.(*fake.Clientset).Tracker().Add(o); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.pruneAutomaticCertificates(ctx, target, "web", map[string]bool{"desired-cert": true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rs-cert", "pod-cert", "desired-cert"} {
		if _, err := c.kube.CoreV1().Secrets(source.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Fatal("deleted referenced certificate", name)
		}
	}
	if _, err := c.kube.CoreV1().Secrets(source.Namespace).Get(ctx, "old-unused", metav1.GetOptions{}); err == nil {
		t.Fatal("unused automatic certificate retained")
	}
}

func TestAutomaticCertificateMultipleMounts(t *testing.T) {
	ctx := context.Background()
	c, target, source := automaticCertificateFixture(t)
	secondHost := "second.example.com"
	cert, key := testTLSCertificate(t, secondHost, time.Now().Add(time.Hour))
	second := source.DeepCopy()
	second.Name = "second-source"
	second.Data = map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}
	if _, err := c.kube.CoreV1().Secrets(source.Namespace).Create(ctx, second, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	ingress, err := c.kube.NetworkingV1().Ingresses(source.Namespace).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ingress.Spec.TLS = append(ingress.Spec.TLS, networkingv1.IngressTLS{Hosts: []string{secondHost}, SecretName: second.Name})
	if _, err = c.kube.NetworkingV1().Ingresses(source.Namespace).Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	target.Spec.Domains[secondHost] = "web"
	svc := target.Spec.Services["web"]
	svc.CertificateMounts = append(svc.CertificateMounts, spec.CertificateMount{Source: "ingress", Hostname: secondHost, MountPath: "/certificates/second"})
	target.Spec.Services["web"] = svc
	resolved, err := c.resolveBackendCertificates(ctx, target, "web", svc, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range resolved.CertificateMounts {
		if _, err := c.kube.CoreV1().Secrets(source.Namespace).Get(ctx, mount.Certificate, metav1.GetOptions{}); err != nil {
			t.Fatal("cleanup removed another pending mount", err)
		}
	}
	statuses, err := c.BackendCertificates(ctx, target, "web")
	if err != nil || len(statuses) != 2 {
		t.Fatal("automatic history duplicated mounted status", err, len(statuses))
	}
	for _, v := range statuses {
		if v.Source != "ingress-auto" || v.MountPath == "" || !v.Ready {
			t.Fatalf("incorrect source metadata: %+v", v)
		}
	}
}
