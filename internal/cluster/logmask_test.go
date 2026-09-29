package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/logquery"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestPodLogsMaskScopedSecretBeforeSearchAndRejectSecretReadFailure(t *testing.T) {
	target := testTarget(t)
	const secretValue = "opaque-application-value"
	failed := false
	reads := 0
	labels := labelsFor(target, "web")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/log"):
			reads++
			fmt.Fprintf(w, "2026-09-30T00:00:00Z {\"message\":\"%s\",\"password\":\"second-secret\"}\n", secretValue)
		case strings.Contains(r.URL.Path, "/secrets/"):
			if failed {
				http.Error(w, "unavailable", 503)
				return
			}
			json.NewEncoder(w).Encode(corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "web-environment", Namespace: Namespace(target.ApplicationID), Labels: labels}, Data: map[string][]byte{"VALUE": []byte(secretValue)}})
		case strings.HasSuffix(r.URL.Path, "/pods"):
			json.NewEncoder(w).Encode(corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "web-test", Namespace: Namespace(target.ApplicationID), Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "web-environment"}}}}}}}}}})
		default:
			json.NewEncoder(w).Encode(corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labelsFor(target, "")}})
		}
	}))
	defer server.Close()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{kube: kube}
	options := LogQueryOptions{Service: "web"}
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	result, err := client.QueryLogs(context.Background(), target, options, func(entry logquery.Entry) bool {
		if strings.Contains(entry.Message, secretValue) || strings.Contains(entry.Message, "second-secret") {
			t.Fatal("filter observed unmasked credentials")
		}
		return true
	})
	if err != nil || len(result.Entries) != 1 {
		t.Fatal("masked log query failed", err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), secretValue) || strings.Contains(string(encoded), "second-secret") {
		t.Fatal("API result exposed a secret through structured metadata")
	}
	failed = true
	_, err = client.QueryLogs(context.Background(), target, options, func(logquery.Entry) bool { return true })
	if err == nil || reads != 1 {
		t.Fatal("secret read failure fell back to raw logs")
	}
}

func TestRawManagedRunnerDiagnosticsAreNotWorkflowLogs(t *testing.T) {
	for _, pod := range []corev1.Pod{{Spec: corev1.PodSpec{RuntimeClassName: ptr(ActionsRuntime)}}, {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{gitlabActionsProviderLabel: "gitlab"}}}, {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{gitlabActionsProviderLabel: "bitbucket"}}}} {
		if !managedRunnerDiagnostics(pod) {
			t.Fatal("credential-bearing runner diagnostics became public workflow output")
		}
	}
	if managedRunnerDiagnostics(corev1.Pod{}) {
		t.Fatal("ordinary application logs were blocked")
	}
}
