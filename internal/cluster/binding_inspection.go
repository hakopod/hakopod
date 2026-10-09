package cluster

import (
	"context"
	"time"

	"github.com/hakopod/hakopod/internal/bindingprobe"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InspectServiceBinding performs no exec and exposes no resolved values. Earlier
// probe evidence is current only while its container, immutable input and trust
// still match. A ready Deployment alone is never authentication evidence.
func (c *Client) InspectServiceBinding(ctx context.Context, t Target, service, variable string, last *bindingprobe.TestResult) bindingprobe.Inspection {
	now := time.Now().UTC()
	result := bindingprobe.NewInspection(t.ApplicationID, service, variable, t.Revision, now, last)
	expected, err := c.bindingProbeExpected(ctx, t, service, variable)
	if err != nil {
		result.Steps[1] = bindingprobe.InspectionStep{Name: "resolved", Status: "failed", Message: "The current binding cannot be resolved. Check its database and secret references."}
		return result
	}
	result.Steps[1] = bindingprobe.InspectionStep{Name: "resolved", Status: "passed", Message: "The current database and secret references resolve successfully."}
	if last == nil {
		return result
	}
	stale := func() bindingprobe.Inspection {
		result.Steps[2].Status = "stale"
		result.Steps[2].Message = "The saved test no longer verifies this container and its current settings. Test again."
		result.Steps[3].Status = "stale"
		return result
	}
	if last.Revision != t.Revision || last.ApplicationID != t.ApplicationID || last.Service != service || last.Variable != variable || last.PodUID == "" || last.Evidence.ContainerID == "" || now.Before(last.ObservedAt) || now.Sub(last.ObservedAt) > bindingprobe.EvidenceExpires {
		return stale()
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil || owned(ns, t) != nil {
		return stale()
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, last.Pod, metav1.GetOptions{})
	if err != nil || owned(pod, t) != nil || pod.Labels[serviceKey] != service || string(pod.UID) != last.PodUID || pod.DeletionTimestamp != nil || len(pod.Spec.Containers) == 0 || pod.Spec.Containers[0].Name != "app" || !c.bindingPodUsesRevision(ctx, t, service, pod) {
		return stale()
	}
	containerMatches := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "app" && status.State.Running != nil && status.ContainerID == last.Evidence.ContainerID {
			containerMatches = true
		}
	}
	if !containerMatches {
		return stale()
	}
	svc := t.Spec.Services[service]
	binding := svc.Bindings[variable]
	matches := 0
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name != variable {
			continue
		}
		if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			return stale()
		}
		ref := env.ValueFrom.SecretKeyRef
		if ref.Key != variable || ref.Optional != nil && *ref.Optional {
			return stale()
		}
		secret, e := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, ref.Name, metav1.GetOptions{})
		if e != nil || environmentSnapshotOwned(secret, t, service) != nil || string(secret.UID) != last.Evidence.SecretUID || secret.ResourceVersion != last.Evidence.SecretVersion || string(secret.Data[variable]) != expected.URL || !c.bindingProbeTrustMatches(ctx, t, service, pod, binding, expected.CA) || !bindingProbeProfileMatches(svc, variable, pod, secret, expected.CA) {
			return stale()
		}
		matches++
	}
	if matches != 1 {
		return stale()
	}
	// Check references again after inventory. Rotating credentials cannot carry
	// forward the earlier success merely because the pod has not rolled yet.
	fresh, err := c.bindingProbeExpected(ctx, t, service, variable)
	if err != nil || fresh.URL != expected.URL || fresh.CA != expected.CA {
		return stale()
	}
	if last.LoadedMatchesSnapshot != nil && *last.LoadedMatchesSnapshot {
		result.Steps[2] = bindingprobe.InspectionStep{Name: "loaded", Status: "passed", Message: "The tested container loaded the current binding and trust settings."}
	}
	switch last.Outcome {
	case "passed":
		if result.Steps[2].Status == "passed" {
			result.Steps[3] = bindingprobe.InspectionStep{Name: "connection", Status: "passed", Message: "The recent test authenticated and ran a minimal query from this container."}
		}
	case "failed":
		result.Steps[3] = bindingprobe.InspectionStep{Name: "connection", Status: "failed", Message: "The most recent connection test failed. Inspect its checks below."}
	case "unsupported":
		result.Steps[3] = bindingprobe.InspectionStep{Name: "connection", Status: "unsupported", Message: "The connection helper cannot verify this protocol."}
	}
	return result
}
