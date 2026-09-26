package cluster

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// The issued certificate cert-manager would place behind the ingress.
func issueCertificateOnlyIngressSecret(t *testing.T, c *Client, target Target, host string) {
	t.Helper()
	certificate, key := testTLSCertificate(t, host, time.Now().Add(time.Hour))
	labels := labelsFor(target, "tunnel")
	_, err := c.kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "hakopod-tls-tunnel", Namespace: Namespace(target.ApplicationID), Labels: labels},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: certificate, corev1.TLSPrivateKeyKey: key},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

// Deploy validates delivery before it reconciles private ingresses, and that
// validation reads the certificate a certificate-only ingress carries. The
// ingress therefore has to be applied before validation, or the first release of
// such a service can never create it and every retry fails the same way.
// This release stops inside delivery validation, which a fake cluster cannot
// satisfy for public TCP, so the ingress can only exist here if it was applied
// before validation started: the later reconcile never ran.
func TestDeployAppliesCertificateOnlyIngressBeforeDeliveryValidation(t *testing.T) {
	ctx := context.Background()
	c, kube, target, host := certificateOnlyTarget(t)
	issueCertificateOnlyIngressSecret(t, c, target, host)

	_, err := c.Deploy(ctx, target, nil)
	if err == nil || !strings.Contains(err.Error(), "public TCP requires the HAProxy v3 TCP API") {
		t.Fatalf("the release stopped somewhere other than delivery validation: %v", err)
	}
	ing, err := kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{})
	if err != nil {
		t.Fatal("delivery validation ran before the certificate-only ingress existed", err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != host || ing.Spec.Rules[0].HTTP != nil {
		t.Fatalf("certificate-only ingress lost its host-only rule: %v", ing.Spec.Rules)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "hakopod-tls-tunnel" {
		t.Fatalf("certificate-only ingress lost its TLS block: %v", ing.Spec.TLS)
	}
	// The deadlock this ordering removes: the mount resolves once the ingress
	// and its issued certificate exist.
	if err := c.ValidateBackendCertificates(ctx, target); err != nil {
		t.Fatal("the automatic mount still cannot be validated after the hoisted apply", err)
	}
}

// The hoisted apply and the later reconcile must write the same object, or every
// release would change the ingress and re-request its certificate.
func TestCertificateOnlyIngressReconcileIsIdempotentAcrossBothCallSites(t *testing.T) {
	ctx := context.Background()
	c, kube, target, host := certificateOnlyTarget(t)
	issueCertificateOnlyIngressSecret(t, c, target, host)
	if err := c.reconcileCertificateIngresses(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
		t.Fatal(err)
	}
	var specs []networkingv1.IngressSpec
	for _, action := range kube.Actions() {
		if action.GetResource().Resource != "ingresses" {
			continue
		}
		var object runtime.Object
		switch value := action.(type) {
		case k8stesting.CreateActionImpl:
			object = value.GetObject()
		case k8stesting.UpdateActionImpl:
			object = value.GetObject()
		}
		if written, ok := object.(*networkingv1.Ingress); ok && written.Name == "tunnel" {
			specs = append(specs, written.Spec)
		}
	}
	if len(specs) < 2 {
		t.Fatalf("both call sites must have written the ingress: %d writes", len(specs))
	}
	for _, written := range specs[1:] {
		if !reflect.DeepEqual(written, specs[0]) {
			t.Fatalf("a repeated apply churned the certificate-only ingress: %v vs %v", written, specs[0])
		}
	}
}

// Removing the automatic mount removes the ingress: nothing publishes HTTP for
// this service any more and no certificate needs renewing.
func TestCertificateOnlyIngressGoesAwayWhenTheMountIsRemoved(t *testing.T) {
	ctx := context.Background()
	c, _, target, _ := certificateOnlyTarget(t)
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["tunnel"]
	svc.CertificateMounts = nil
	target.Spec.Services["tunnel"] = svc
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", svc); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("the ingress outlived the automatic certificate mount", err)
	}
}

// A service that disappears from the spec loses its certificate-only ingress
// through the ordinary cleanup path.
func TestCleanupRemovesCertificateOnlyIngressOfARetiredService(t *testing.T) {
	ctx := context.Background()
	c, _, target, _ := certificateOnlyTarget(t)
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
		t.Fatal(err)
	}
	previous := target.Spec
	delete(target.Spec.Services, "tunnel")
	target.Previous = &previous
	if err := c.cleanup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.NetworkingV1().Ingresses(Namespace(target.ApplicationID)).Get(ctx, "tunnel", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("a retired service kept its certificate-only ingress", err)
	}
}

// A raw TCP service can never carry its own spec TLS block, because tls needs a
// public HTTP service, so only the installation issuer can renew its automatic
// mount. Without an issuer the mount must be refused rather than accepted and
// left to rot: nothing would ever rotate it.
func TestAutomaticMountNeedsTheInstallationIssuerForATCPOnlyService(t *testing.T) {
	ctx := context.Background()
	c, _, target, host := certificateOnlyTarget(t)
	issueCertificateOnlyIngressSecret(t, c, target, host)
	if target.Spec.Services["tunnel"].TLS != nil {
		t.Fatal("a service published only over raw TCP cannot configure its own TLS")
	}
	if err := c.reconcilePrivateIngress(ctx, target, "tunnel", target.Spec.Services["tunnel"]); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateBackendCertificates(ctx, target); err != nil {
		t.Fatal("a configured installation issuer must satisfy the automatic mount", err)
	}
	c.options.TLSIssuer = ""
	err := c.ValidateBackendCertificates(ctx, target)
	if err == nil || !strings.Contains(err.Error(), "configured TLS ingress") {
		t.Fatal("an automatic mount was accepted with no issuer that could ever renew it", err)
	}
}

// A certificate-only ingress carries a hostname too, so it needs the app domain
// just as a public HTTP service does.
func TestCertificateOnlyServiceRequiresTheAppDomain(t *testing.T) {
	c, _, target, _ := certificateOnlyTarget(t)
	// Leave only the raw TCP service, so the public HTTP guard cannot be the one
	// that reports the missing domain.
	delete(target.Spec.Services, "web")
	c.options.AppDomain = ""
	_, err := c.Deploy(context.Background(), target, nil)
	if err == nil || !strings.Contains(err.Error(), "HAKOPOD_APP_DOMAIN") {
		t.Fatal("a certificate-only service accepted an empty app domain", err)
	}
}
