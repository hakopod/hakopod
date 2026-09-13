package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func deliveryObservationFixture(t *testing.T) (Target, *Client, *kubefake.Clientset) {
	t.Helper()
	target := testTarget(t)
	target.Spec.Services = map[string]spec.Service{"api": target.Spec.Services["api"]}
	kube := kubefake.NewClientset()
	return target, &Client{kube: kube}, kube
}

func installReadyDelivery(t *testing.T, target Target, c *Client, kube *kubefake.Clientset, change func(*appsv1.Deployment)) {
	t.Helper()
	svc := target.Spec.Services["api"]
	d := deployment(target, "api", svc, time.Minute)
	if svc.AWSIdentity != "" && len(c.options.AWSIdentityBindings) > 0 {
		awsFakeAudience(kube, false)
		if err := c.prepareAWSIdentity(context.Background(), target, "api", svc, d); err != nil {
			t.Fatal(err)
		}
	}
	if change != nil {
		change(d)
	}
	d.Generation = 1
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	if _, err := kube.AppsV1().Deployments(d.Namespace).Create(context.Background(), d, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-ready", Namespace: d.Namespace, Labels: d.Spec.Template.Labels}, Spec: d.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if _, err := kube.CoreV1().Pods(d.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryObservationRejectsMissingAndDriftedTCP(t *testing.T) {
	for _, state := range []string{"missing", "drifted", "pending", "configured"} {
		t.Run(state, func(t *testing.T) {
			target, c, kube := deliveryObservationFixture(t)
			svc := target.Spec.Services["api"]
			svc.PublicTCP = []spec.PublicTCPListener{{Port: 587, TargetPort: svc.Port, SourceCIDRs: []string{"0.0.0.0/0"}}}
			target.Spec.Services["api"] = svc
			c.options.IngressClass = "haproxy"
			obj := publicTCPObject(target, "haproxy")
			if state == "configured" {
				annotations := obj.GetAnnotations()
				annotations["hakopod.io/tcp-acknowledged"] = publicTCPHash(publicTCPModels(target))
				obj.SetAnnotations(annotations)
			}
			if state == "drifted" {
				obj.Object["spec"] = []any{}
			}
			if state == "missing" {
				c.dynamic = fake.NewSimpleDynamicClient(runtime.NewScheme())
			} else {
				c.dynamic = fake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			}
			installReadyDelivery(t, target, c, kube, nil)
			observed, err := c.Observe(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if state == "configured" {
				if observed.Status != "healthy" || !strings.Contains(observed.Services[0].Message, "unverified") {
					t.Fatal("configured route lost external-verification qualification", observed)
				}
			} else if observed.Status == "healthy" || observed.Services[0].Status != "failed" || !strings.Contains(observed.Services[0].Message, "Public TCP") {
				t.Fatal("ready pods concealed missing/drifted listener", observed)
			}
			for _, action := range kube.Actions() {
				if action.GetSubresource() == "exec" {
					t.Fatal("background observation executed a container probe")
				}
			}
		})
	}
}

func TestDeliveryObservationRejectsMissingExpiredAndUnmountedCertificate(t *testing.T) {
	for _, state := range []string{"missing", "expired", "unmounted", "valid"} {
		t.Run(state, func(t *testing.T) {
			target, c, kube := deliveryObservationFixture(t)
			svc := target.Spec.Services["api"]
			svc.CertificateMounts = []spec.CertificateMount{{Certificate: "smtp-certificate", Hostname: "mail.example.com", MountPath: "/certs/smtp"}}
			target.Spec.Services["api"] = svc
			if state != "missing" {
				expiry := time.Now().Add(time.Hour)
				if state == "expired" {
					expiry = time.Now().Add(-time.Minute)
				}
				cert, key := testTLSCertificate(t, "mail.example.com", expiry)
				labels := labelsFor(target, "api")
				labels[backendCertificateKey] = "true"
				_, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "smtp-certificate", Namespace: Namespace(target.ApplicationID), Labels: labels, Annotations: map[string]string{backendCertificateHostname: "mail.example.com"}}, Type: corev1.SecretTypeTLS, Immutable: ptr(true), Data: map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			installReadyDelivery(t, target, c, kube, func(d *appsv1.Deployment) {
				if state == "unmounted" {
					d.Spec.Template.Spec.Containers[0].VolumeMounts = nil
				}
			})
			observed, err := c.Observe(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if (observed.Status == "healthy") != (state == "valid") {
				t.Fatal("certificate health did not reflect actual configuration", observed)
			}
			if state != "valid" && (observed.Services[0].Status != "failed" || !strings.Contains(strings.ToLower(observed.Services[0].Message), "certificate")) {
				t.Fatal("certificate failure missing from service status", observed)
			}
		})
	}
}

func TestDeliveryObservationRejectsRevokedMissingAndDriftedAWSIdentity(t *testing.T) {
	for _, state := range []string{"revoked", "account-missing", "account-tampered", "token-missing", "role-drifted", "prepared"} {
		t.Run(state, func(t *testing.T) {
			target, c, kube := deliveryObservationFixture(t)
			binding := awsBindingFixture(target)
			c.options.AWSIdentityBindings = []AWSIdentityBinding{binding}
			svc := target.Spec.Services["api"]
			svc.AWSIdentity = binding.Name
			target.Spec.Services["api"] = svc
			installReadyDelivery(t, target, c, kube, func(d *appsv1.Deployment) {
				if state == "token-missing" {
					d.Spec.Template.Spec.Volumes = nil
				}
				if state == "role-drifted" {
					for i := range d.Spec.Template.Spec.Containers[0].Env {
						if d.Spec.Template.Spec.Containers[0].Env[i].Name == "AWS_ROLE_ARN" {
							d.Spec.Template.Spec.Containers[0].Env[i].Value = "arn:aws:iam::123456789012:role/other"
						}
					}
				}
			})
			if state == "revoked" {
				c.options.AWSIdentityBindings = nil
			}
			accounts := kube.CoreV1().ServiceAccounts(Namespace(target.ApplicationID))
			if state == "account-missing" {
				if err := accounts.Delete(context.Background(), AWSIdentityServiceAccount(binding), metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "account-tampered" {
				account, err := accounts.Get(context.Background(), AWSIdentityServiceAccount(binding), metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				account.AutomountServiceAccountToken = ptr(true)
				if _, err := accounts.Update(context.Background(), account, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			kube.ClearActions()
			observed, err := c.Observe(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			if (observed.Status == "healthy") != (state == "prepared") {
				t.Fatal("AWS identity problem concealed by ready pods", observed)
			}
			if state == "prepared" && !strings.Contains(observed.Services[0].Message, "unverified") {
				t.Fatal("prepared Kubernetes identity claimed AWS verification")
			}
			if state != "prepared" && (observed.Services[0].Status != "failed" || !strings.Contains(observed.Services[0].Message, "AWS")) {
				t.Fatal("AWS failure missing from runtime status", observed)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatal("background observation created a token or changed resources", action)
				}
			}
		})
	}
}

func TestAWSIdentityStatusSeparatesUnavailableFromReadFailures(t *testing.T) {
	target, c, kube := deliveryObservationFixture(t)
	binding := awsBindingFixture(target)
	svc := target.Spec.Services["api"]
	svc.AWSIdentity = binding.Name
	target.Spec.Services["api"] = svc
	state, err := c.AWSIdentityStatus(context.Background(), target, "api")
	if err != nil || state.Status != "unavailable" || state.Message == "" || state.RoleARN != "" {
		t.Fatal("missing binding should be a safe state, not hide other delivery observations", state, err)
	}
	c.options.AWSIdentityBindings = []AWSIdentityBinding{binding}
	kube.PrependReactor("get", "serviceaccounts", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("test transport unavailable")
	})
	if _, err := c.AWSIdentityStatus(context.Background(), target, "api"); err == nil {
		t.Fatal("transport failure misreported as configured identity")
	}
}
