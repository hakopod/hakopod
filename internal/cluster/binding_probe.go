package cluster

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/bindingprobe"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// BindingTestResult identifies the exact pod tested. It contains no connection
// values, credential fingerprints, driver errors or application output.
type BindingTestResult = bindingprobe.TestResult

// TestServiceBinding runs one fixed helper command. The request may identify
// only a declared binding and an owned running pod, never a URL or command.
func (c *Client) TestServiceBinding(ctx context.Context, t Target, service, variable, podName string, authorize func(context.Context) error) (BindingTestResult, error) {
	result := BindingTestResult{SchemaVersion: 1, ApplicationID: t.ApplicationID, Service: service, Variable: variable, Revision: t.Revision, ObservedAt: time.Now().UTC(), Outcome: "unavailable", Stages: []bindingprobe.Stage{}}
	unavailable := func(code, message string) (BindingTestResult, error) {
		result.Stages = append(result.Stages, bindingprobe.Stage{Name: "runtime", Status: "failed", Code: code, Message: message})
		return result, nil
	}
	if authorize == nil {
		return result, fmt.Errorf("binding test requires a current authorization check")
	}
	if err := authorize(ctx); err != nil {
		return result, err
	}
	svc, exists := t.Spec.Services[service]
	binding, declared := svc.Bindings[variable]
	if !exists || !declared || svc.Actions != nil || svc.Session != nil {
		return unavailable("binding_unavailable", "Select a database binding in this service.")
	}
	if c.options.ReadinessProbeImage == "" {
		return unavailable("helper_not_configured", "The installation owner must configure the database connection helper in Infrastructure > Setup.")
	}
	ns := Namespace(t.ApplicationID)
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil || owned(namespace, t) != nil {
		return unavailable("runtime_unavailable", "The application runtime is unavailable.")
	}
	list, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service, Limit: 65})
	if err != nil || list.Continue != "" || len(list.Items) > 64 {
		return unavailable("pod_inventory_unavailable", "The running pod inventory is unavailable or exceeds 64 pods.")
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[j].CreationTimestamp.Before(&list.Items[i].CreationTimestamp) })
	var selected *corev1.Pod
	for i := range list.Items {
		pod := &list.Items[i]
		if podName != "" && pod.Name != podName || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "app" && status.State.Running != nil {
				selected = pod
				break
			}
		}
		if selected != nil {
			break
		}
	}
	if selected == nil {
		return unavailable("pod_not_running", "No matching application container is running. Start the service, then test again.")
	}
	result.Pod, result.PodUID = selected.Name, string(selected.UID)
	if !bindingProbeInstalled(selected, c.options.ReadinessProbeImage) {
		return unavailable("helper_not_installed", "Redeploy this service with the configured connection helper before testing.")
	}
	if len(selected.Spec.Containers) == 0 || selected.Spec.Containers[0].Name != "app" {
		return unavailable("container_unavailable", "The application container is unavailable.")
	}
	var ref *corev1.SecretKeySelector
	for _, env := range selected.Spec.Containers[0].Env {
		if env.Name == variable && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.Value == "" {
			if ref != nil {
				return unavailable("binding_reference_changed", "The runtime binding declaration changed. Redeploy the accepted revision.")
			}
			ref = env.ValueFrom.SecretKeyRef
		}
	}
	if ref == nil || ref.Key != variable || ref.Optional != nil && *ref.Optional {
		return unavailable("binding_not_loaded", "This pod does not reference the saved binding. Wait for its rollout or redeploy the service.")
	}
	loadedSecret, err := c.kube.CoreV1().Secrets(ns).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil || owned(loadedSecret, t) != nil || loadedSecret.Labels[serviceKey] != service || loadedSecret.DeletionTimestamp != nil {
		return unavailable("binding_snapshot_unavailable", "The pod's binding snapshot is unavailable.")
	}
	if ref.Name != service+"-environment" && environmentSnapshotOwned(loadedSecret, t, service) != nil {
		return unavailable("binding_reference_changed", "The runtime binding declaration changed. Redeploy the accepted revision.")
	}
	expected, resolutionErr := c.bindingProbeExpected(ctx, t, service, variable)
	result.SnapshotResolved = resolutionErr == nil
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return unavailable("comparison_unavailable", "The runtime comparison could not start. Try again.")
	}
	request := bindingprobe.Request{SchemaVersion: 1, Variable: variable, Protocol: binding.Protocol, TimeoutMS: 15000, Nonce: hex.EncodeToString(random[:]), CAFile: spec.ReadinessHelperDirectory + "/ca-certificates.crt"}
	request.Plaintext = binding.Service != "" && binding.ManagedDatabase == "" && binding.ExternalDatabase == "" || binding.SSLMode == "disable"
	if binding.ManagedDatabase != "" {
		request.CAFile = DatabaseTrustPath(binding.ManagedDatabase)
	}
	input, _ := json.Marshal(request)
	options := TerminalOptions{Pod: selected.Name, Container: "app", Command: []string{spec.ReadinessHelperDirectory + "/hakopod-probe", "connection"}}
	uid, err := c.TerminalPod(ctx, t, service, options)
	if err != nil || uid != selected.UID {
		return unavailable("pod_changed", "The selected pod changed before the test. Refresh and try again.")
	}
	if c.execConfig == nil || c.restClient() == nil {
		return unavailable("transport_unavailable", "The runtime connection test transport is unavailable.")
	}
	u := c.restClient().Post().Resource("pods").Namespace(ns).Name(selected.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "app", Command: options.Command, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return unavailable("transport_unavailable", "The runtime connection test transport is unavailable.")
	}
	if err = authorize(ctx); err != nil {
		return result, err
	}
	stdout := &databaseBoundedWriter{limit: 8192}
	// Stderr can contain application or loader output. Never return or persist
	// it. The fixed helper returns only structured stages on stdout.
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(input), Stdout: stdout, Stderr: io.Discard})
	if err != nil {
		return unavailable("probe_did_not_finish", "The connection helper did not finish. Check that this service uses the current helper, then try again.")
	}
	var measured bindingprobe.Result
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&measured) != nil || measured.SchemaVersion != 1 || measured.Variable != variable || measured.Protocol != binding.Protocol {
		return unavailable("probe_result_invalid", "The connection helper returned an invalid result.")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return unavailable("probe_result_invalid", "The connection helper returned an invalid result.")
	}
	stages, valid := bindingprobe.PublicStages(measured.Stages)
	if !valid {
		return unavailable("probe_result_invalid", "The connection helper returned an invalid result.")
	}
	for _, stage := range stages {
		if stage.Code == "tls_not_configured" && !request.Plaintext || stage.Code == "tls_verified" && request.Plaintext {
			return unavailable("probe_result_invalid", "The connection helper returned an invalid result.")
		}
	}
	if err = authorize(ctx); err != nil {
		return result, err
	}
	currentUID, err := c.TerminalPod(ctx, t, service, options)
	if err != nil || currentUID != selected.UID {
		return unavailable("pod_changed", "The tested pod was replaced or stopped. Test its replacement before using this result.")
	}
	result.Stages, result.ObservedAt = stages, time.Now().UTC()
	result.Evidence.SecretUID = string(loadedSecret.UID)
	result.Evidence.SecretVersion = loadedSecret.ResourceVersion
	for _, status := range selected.Status.ContainerStatuses {
		if status.Name == "app" && status.State.Running != nil {
			result.Evidence.ContainerID = status.ContainerID
		}
	}
	if result.SnapshotResolved && len(measured.Fingerprint) == 64 {
		mac := hmac.New(sha256.New, []byte(request.Nonce))
		_, _ = mac.Write([]byte(expected.URL))
		actual, err := hex.DecodeString(measured.Fingerprint)
		if err == nil {
			result.LoadedMatchesSnapshot = ptr(hmac.Equal(actual, mac.Sum(nil)) && c.bindingPodUsesRevision(ctx, t, service, selected) && c.bindingProbeTrustMatches(ctx, t, service, selected, binding, expected.CA) && bindingProbeProfileMatches(svc, variable, selected, loadedSecret, expected.CA))
		}
	}
	// A concurrent refresh makes this comparison stale. Keep the recorded
	// network evidence, but do not claim that it verifies the latest snapshot.
	if result.SnapshotResolved {
		fresh, e := c.bindingProbeExpected(ctx, t, service, variable)
		if e != nil || fresh.URL != expected.URL || fresh.CA != expected.CA {
			result.LoadedMatchesSnapshot = nil
		}
	}
	last := stages[len(stages)-1]
	switch {
	case last.Status == "unsupported":
		result.Outcome = "unsupported"
	case last.Status == "failed":
		result.Outcome = "failed"
	case result.LoadedMatchesSnapshot == nil || !*result.LoadedMatchesSnapshot:
		result.Outcome = "stale"
	default:
		result.Outcome = "passed"
	}
	return result, nil
}

// Resolve only the selected binding. Legacy environment Secrets may remain
// unchanged for retained jobs and cannot describe the latest saved settings.
func (c *Client) bindingProbeExpected(ctx context.Context, t Target, service, variable string) (DatabaseConnection, error) {
	binding := t.Spec.Services[service].Bindings[variable]
	selected := t
	selected.Spec.Env, selected.Spec.Secrets = nil, nil
	selected.Spec.Services = map[string]spec.Service{service: {Bindings: map[string]spec.Binding{variable: binding}}}
	selected.secretValues, selected.databaseConnections = nil, nil
	if err := c.snapshotWorkloadSecrets(ctx, &selected); err != nil {
		return DatabaseConnection{}, err
	}
	if err := c.snapshotDatabaseBindings(ctx, &selected); err != nil {
		return DatabaseConnection{}, err
	}
	if binding.ManagedDatabase != "" || binding.ExternalDatabase != "" {
		return selected.databaseConnections[service][variable], nil
	}
	password := selected.secretValues[service][spec.BindingSecretKey(variable)]
	return DatabaseConnection{URL: spec.BindingURL(binding, t.Spec.Services[binding.Service], password)}, nil
}

func (c *Client) bindingProbeTrustMatches(ctx context.Context, t Target, service string, pod *corev1.Pod, binding spec.Binding, expected string) bool {
	if binding.ManagedDatabase == "" {
		return true
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name != databaseTrustVolume || volume.ConfigMap == nil {
			continue
		}
		trust, err := c.kube.CoreV1().ConfigMaps(Namespace(t.ApplicationID)).Get(ctx, volume.ConfigMap.Name, metav1.GetOptions{})
		return err == nil && databaseTrustOwned(trust, t, service) == nil && trust.Data[binding.ManagedDatabase+".crt"] == expected
	}
	return expected == ""
}

func bindingProbeProfileMatches(svc spec.Service, variable string, pod *corev1.Pod, secret *corev1.Secret, expectedCA string) bool {
	if svc.Bindings[variable].ManagedDatabase == "" || spec.DatabaseClientProfile(svc, variable) != spec.DatabaseClientInfisicalPostgresV1 {
		return true
	}
	if expectedCA == "" || secret.Immutable == nil || !*secret.Immutable || string(secret.Data["DB_ROOT_CERT"]) != base64.StdEncoding.EncodeToString([]byte(expectedCA)) || len(pod.Spec.Containers) == 0 {
		return false
	}
	declarations := 0
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name != "DB_ROOT_CERT" {
			continue
		}
		if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			return false
		}
		ref := env.ValueFrom.SecretKeyRef
		if ref.Name != secret.Name || ref.Key != "DB_ROOT_CERT" || ref.Optional != nil && *ref.Optional {
			return false
		}
		declarations++
	}
	return declarations == 1
}

// Read the owning controller after the probe finishes. A concurrent rollout,
// including a trust-only renewal, must not make an old pod appear current.
func (c *Client) bindingPodUsesRevision(ctx context.Context, t Target, service string, pod *corev1.Pod) bool {
	ns := Namespace(t.ApplicationID)
	if t.Spec.Services[service].Job == nil {
		dep, err := c.kube.AppsV1().Deployments(ns).Get(ctx, service, metav1.GetOptions{})
		return err == nil && owned(dep, t) == nil && dep.DeletionTimestamp == nil && dep.Annotations["hakopod.io/revision"] == strconv.FormatInt(t.Revision, 10) && podUsesTemplate(pod, &dep.Spec.Template)
	}
	for _, owner := range pod.OwnerReferences {
		if owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Controller == nil || !*owner.Controller {
			continue
		}
		job, err := c.kube.BatchV1().Jobs(ns).Get(ctx, owner.Name, metav1.GetOptions{})
		return err == nil && owned(job, t) == nil && job.UID == owner.UID && job.DeletionTimestamp == nil && job.Labels[serviceKey] == service && job.Annotations[jobRevision] == strconv.FormatInt(t.Revision, 10) && podUsesTemplate(pod, &job.Spec.Template)
	}
	return false
}

func bindingProbeInstalled(pod *corev1.Pod, image string) bool {
	installed, mounted := false, false
	for _, init := range pod.Spec.InitContainers {
		if init.Name == "install-readiness-probe" && init.Image == image && len(init.Command) == 2 && init.Command[0] == "/hakopod-probe" && init.Command[1] == "install" {
			installed = true
		}
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != "app" {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if mount.Name == "hakopod-readiness-probe" && mount.MountPath == spec.ReadinessHelperDirectory && mount.ReadOnly && mount.SubPath == "" && mount.SubPathExpr == "" {
				mounted = true
			}
		}
	}
	return installed && mounted
}
