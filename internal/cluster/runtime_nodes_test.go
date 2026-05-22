package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
)

func readyNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, ResourceVersion: "1"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.35.8+k3s1"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}
func TestNodeDrainProtectsControlPlaneLocalDataAndDisruptionBudgets(t *testing.T) {
	ctx := context.Background()
	server := readyNode("server")
	server.Labels = map[string]string{"node-role.kubernetes.io/control-plane": "true"}
	worker := readyNode("worker")
	kube := fake.NewClientset(server, worker)
	client := &Client{kube: kube}
	if _, err := client.CordonNode(ctx, "server", "1", true); err == nil {
		t.Fatal("control plane cordoned")
	}
	if _, err := client.CordonNode(ctx, "worker", "stale", true); err == nil {
		t.Fatal("stale node revision accepted")
	}
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "stateful", Namespace: "fixture", UID: "pod-uid", Labels: map[string]string{managedBy: "hakopod"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "fixture-rs", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "worker", Volumes: []corev1.Volume{{Name: "local", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if _, err := kube.CoreV1().Pods("fixture").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := client.DrainNode(ctx, "worker", "1")
	if err != nil || len(result.Blockers) != 1 || result.Cordoned {
		t.Fatalf("local-data preflight failed: %+v %v", result, err)
	}
	pod.Spec.Volumes = nil
	if _, err = kube.CoreV1().Pods("fixture").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "eviction" {
			return true, nil, apierrors.NewTooManyRequests("PDB budget exhausted", 1)
		}
		return false, nil, nil
	})
	result, err = client.DrainNode(ctx, "worker", "1")
	if err != nil || !result.Cordoned || result.Complete || len(result.Evicted) != 0 || len(result.Blockers) != 1 {
		t.Fatalf("disruption budget was not respected: %+v %v", result, err)
	}
}
func TestEnrollmentUsesK3sBootstrapFormatAndScopedRevocation(t *testing.T) {
	ctx := context.Background()
	cert, _ := testTLSCertificate(t, "test.example.com", time.Now().Add(time.Hour))
	server := readyNode("server")
	kube := fake.NewClientset(server)
	client := &Client{kube: kube, clusterCA: cert, options: Options{SupervisorURL: "https://server.example.com:6443"}}
	value, err := client.CreateEnrollment(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("K10%x::", sha256.Sum256(cert))
	if !strings.HasPrefix(value.Token, prefix) || len(value.ID) != 6 {
		t.Fatal("incorrect secure token format")
	}
	stored, err := kube.CoreV1().Secrets("kube-system").Get(ctx, "bootstrap-token-"+value.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.Data["auth-extra-groups"]) != "system:bootstrappers:k3s:default-node-token" || string(stored.Data["usage-bootstrap-authentication"]) != "true" {
		t.Fatal("incorrect K3s bootstrap permissions")
	}
	list, err := client.Enrollments(ctx)
	if err != nil || len(list.Items) != 1 || list.Items[0].Token != "" {
		t.Fatal("list exposed token or failed")
	}
	stored.Labels[managedBy] = "other"
	if _, err = kube.CoreV1().Secrets("kube-system").Update(ctx, stored, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = client.RevokeEnrollment(ctx, value.ID); err == nil {
		t.Fatal("unowned bootstrap token revoked")
	}
}
func TestEnrollmentLiveTokenAuthenticationAndRevocation(t *testing.T) {
	kubeconfig := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set HAKOPOD_TEST_KUBECONFIG for temporary live bootstrap token validation")
	}
	configuration, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil || configuration.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("live enrollment test requires the named k3d-hakopod-dev context")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(kubeconfig, Options{SupervisorURL: config.Host})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	value, err := client.CreateEnrollment(ctx, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.RevokeEnrollment(clean, value.ID)
	}()
	_, bare, _ := strings.Cut(value.Token, "::")
	check := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			review, err := client.kube.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: bare}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal("bootstrap token authentication check failed")
			}
			if review.Status.Authenticated == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("bootstrap authentication wanted %v, got %v", want, review.Status.Authenticated)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	check(true)
	if err = client.RevokeEnrollment(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
	nodes, err := client.Nodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	samples := 0
	for _, node := range nodes {
		if node.Metrics.Available {
			samples++
		}
	}
	t.Logf("real K3s bootstrap authentication accepted then rejected after revocation; %d of %d nodes have current metrics", samples, len(nodes))
}
