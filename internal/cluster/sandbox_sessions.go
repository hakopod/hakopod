package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/sessionguard"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const sandboxSessionLabel = "hakopod.io/sandbox-session"
const sandboxGeneration = "hakopod.io/session-generation"
const sandboxOwner = "hakopod.io/session-owner"
const sandboxRuntime = "hakopod.io/session-runtime"
const sandboxTemplateHash = "hakopod.io/session-template-sha256"
const sandboxPodName = "kernel"

// SandboxNamespace is derived only from the durable session identifier.
func SandboxNamespace(id string) string { return "hs-" + id }

func sandboxTarget(r sandbox.Record) (Target, spec.Service, error) {
	if !sandbox.ValidID(r.ID) || !sandbox.ValidID(r.Generation) || r.ApplicationID == "" || r.Revision < 1 || len(r.OwnerHash) != 64 || len(r.RuntimeHash) != 64 {
		return Target{}, spec.Service{}, fmt.Errorf("session receipt identity is invalid")
	}
	app, err := spec.Normalize(r.Source)
	if err != nil {
		return Target{}, spec.Service{}, fmt.Errorf("session template is invalid")
	}
	svc, ok := app.Services[r.Service]
	if !ok || svc.Session == nil || svc.Image != r.Image || !sandbox.ValidImage(r.Image) {
		return Target{}, spec.Service{}, fmt.Errorf("session requires its recorded immutable template")
	}
	return Target{Project: r.Project, Environment: r.Environment, ApplicationID: r.ApplicationID, OperationID: r.ID, Revision: r.Revision, Spec: app}, svc, nil
}
func sandboxLabels(t Target, r sandbox.Record) map[string]string {
	labels := labelsFor(t, r.Service)
	labels[sandboxSessionLabel] = r.ID
	labels[sandboxGeneration] = r.Generation
	labels[sandboxOwner] = r.OwnerHash[:32]
	labels[sandboxRuntime] = r.RuntimeHash[:32]
	return labels
}
func sandboxAnnotations(r sandbox.Record, s spec.Service) map[string]string {
	data, _ := json.Marshal(s)
	hash := sha256.Sum256(data)
	return map[string]string{sandboxTemplateHash: fmt.Sprintf("%x", hash), "hakopod.io/session-expires": r.ExpiresAt.UTC().Format(time.RFC3339Nano)}
}
func sandboxOwned(obj metav1.Object, t Target, r sandbox.Record) error {
	if err := owned(obj, t); err != nil {
		return err
	}
	for key, value := range sandboxLabels(t, r) {
		if obj.GetLabels()[key] != value {
			return fmt.Errorf("session resource ownership does not match its receipt")
		}
	}
	return nil
}
func (c *Client) sandboxTemplate(ctx context.Context, r sandbox.Record) (Target, spec.Service, RuntimeProfileBinding, error) {
	t, s, err := sandboxTarget(r)
	if err != nil {
		return t, s, RuntimeProfileBinding{}, err
	}
	if s.Suspended || !sandbox.AllowsIdentity(s.Session, r.IdentityID) || !time.Now().Before(r.ExpiresAt) || !time.Now().Before(r.IdleUntil) {
		return t, s, RuntimeProfileBinding{}, fmt.Errorf("session authority is paused or expired")
	}
	marker, err := c.observeSessionTemplate(ctx, t, r.Service, s)
	if err != nil {
		return t, s, RuntimeProfileBinding{}, err
	}
	if marker.Status != "configured" {
		return t, s, RuntimeProfileBinding{}, fmt.Errorf("session template revision is not active")
	}
	binding, err := c.resolveRuntimeProfile(ctx, r.Project, r.Environment, t.Spec.Name, r.Service, s)
	return t, s, binding, err
}
func (c *Client) sandboxNamespace(ctx context.Context, t Target, r sandbox.Record) (*corev1.Namespace, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, SandboxNamespace(r.ID), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err = sandboxOwned(ns, t, r); err != nil {
		return nil, err
	}
	if ns.UID == "" || r.NamespaceUID != "" && string(ns.UID) != r.NamespaceUID || ns.DeletionTimestamp != nil {
		return nil, fmt.Errorf("session namespace was replaced or is terminating")
	}
	return ns, nil
}

// StartSession creates one Pod. A recorded Pod or namespace is never recreated.
func (c *Client) StartSession(ctx context.Context, r sandbox.Record, before func(context.Context) error) (sandbox.RuntimeState, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state := sandbox.RuntimeState{}
	if before == nil {
		return state, fmt.Errorf("session creation requires a live authority check")
	}
	t, s, binding, err := c.sandboxTemplate(ctx, r)
	if err != nil {
		return state, err
	}
	t.BeforeStep = before
	if err = beforeStep(ctx, t); err != nil {
		return state, err
	}
	ns, err := c.sandboxNamespace(ctx, t, r)
	if apierrors.IsNotFound(err) {
		if r.NamespaceUID != "" || r.PodUID != "" {
			return state, fmt.Errorf("recorded session namespace is missing")
		}
		labels := sandboxLabels(t, r)
		labels["pod-security.kubernetes.io/enforce"] = "restricted"
		labels["pod-security.kubernetes.io/enforce-version"] = "latest"
		if err = beforeStep(ctx, t); err != nil {
			return state, err
		}
		ns, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SandboxNamespace(r.ID), Labels: labels}}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			ns, err = c.sandboxNamespace(ctx, t, r)
		}
	}
	if err != nil {
		return state, err
	}
	state.NamespaceUID = string(ns.UID)
	if state.NamespaceUID == "" {
		return state, fmt.Errorf("session namespace has no UID")
	}
	if err = c.ensureSandboxBoundaries(ctx, t, r, s); err != nil {
		return state, err
	}
	pods := c.kube.CoreV1().Pods(ns.Name)
	if err = c.sandboxRegistry(ctx, t, r, s, true); err != nil {
		return state, err
	}
	current, err := pods.Get(ctx, sandboxPodName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if r.PodUID != "" || r.ContainerID != "" {
			return state, fmt.Errorf("recorded session Pod is missing")
		}
		wanted := c.sandboxPod(t, r, s, binding)
		if err = beforeStep(ctx, t); err != nil {
			return state, err
		}
		current, err = pods.Create(ctx, wanted, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, err = pods.Get(ctx, sandboxPodName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return state, err
	}
	return c.sandboxState(t, r, s, binding, current, state)
}
func (c *Client) ensureSandboxBoundaries(ctx context.Context, t Target, r sandbox.Record, s spec.Service) error {
	ns := SandboxNamespace(r.ID)
	labels := sandboxLabels(t, r)
	wanted := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: ns, Labels: labels}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	policies := c.kube.NetworkingV1().NetworkPolicies(ns)
	policy, err := policies.Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		policy, err = policies.Create(ctx, wanted, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			policy, err = policies.Get(ctx, wanted.Name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return err
	}
	if err = sandboxOwned(policy, t, r); err != nil {
		return err
	}
	if !reflect.DeepEqual(policy.Spec, wanted.Spec) {
		return fmt.Errorf("session network policy changed")
	}
	quotaWanted := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "session", Namespace: ns, Labels: labels}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("1")}}}
	quotas := c.kube.CoreV1().ResourceQuotas(ns)
	quota, err := quotas.Get(ctx, quotaWanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		quota, err = quotas.Create(ctx, quotaWanted, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			quota, err = quotas.Get(ctx, quotaWanted.Name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return err
	}
	if err = sandboxOwned(quota, t, r); err != nil {
		return err
	}
	if !reflect.DeepEqual(quota.Spec, quotaWanted.Spec) {
		return fmt.Errorf("session Pod quota changed")
	}
	return c.checkSandboxPolicies(ctx, t, r)
}
func (c *Client) checkSandboxPolicies(ctx context.Context, t Target, r sandbox.Record) error {
	list, err := c.kube.NetworkingV1().NetworkPolicies(SandboxNamespace(r.ID)).List(ctx, metav1.ListOptions{Limit: 2})
	if err != nil {
		return err
	}
	if len(list.Items) != 1 || list.Continue != "" {
		return fmt.Errorf("session requires exactly one deny-all network policy")
	}
	p := &list.Items[0]
	if err = sandboxOwned(p, t, r); err != nil {
		return err
	}
	want := networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}
	if p.Name != "deny-all" || !reflect.DeepEqual(p.Spec, want) {
		return fmt.Errorf("session network isolation changed")
	}
	return nil
}

func (c *Client) ObserveSession(ctx context.Context, r sandbox.Record) (sandbox.RuntimeState, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := sandbox.RuntimeState{}
	t, s, binding, err := c.sandboxTemplate(ctx, r)
	if err != nil {
		return state, err
	}
	ns, err := c.sandboxNamespace(ctx, t, r)
	if err != nil {
		return state, err
	}
	state.NamespaceUID = string(ns.UID)
	if err = c.checkSandboxPolicies(ctx, t, r); err != nil {
		return state, err
	}
	if err = c.sandboxRegistry(ctx, t, r, s, false); err != nil {
		return state, err
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, sandboxPodName, metav1.GetOptions{})
	if err != nil {
		return state, err
	}
	return c.sandboxState(t, r, s, binding, pod, state)
}

// CallSession executes only the template helper. Caller input waits for the guard identity handshake.
func (c *Client) CallSession(ctx context.Context, r sandbox.Record, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, sandbox.CallTimeout)
	defer cancel()
	if input == nil || output == nil {
		return fmt.Errorf("session call requires input and output streams")
	}
	if r.PodUID == "" || r.ContainerID == "" || r.ImageID == "" {
		return fmt.Errorf("session call requires a recorded container identity")
	}
	state, err := c.ObserveSession(ctx, r)
	if err != nil {
		return err
	}
	if !state.Ready {
		return fmt.Errorf("session kernel is not ready")
	}
	_, s, err := sandboxTarget(r)
	if err != nil {
		return err
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("session exec transport is unavailable")
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return fmt.Errorf("cannot create the session handshake token")
	}
	token := hex.EncodeToString(nonce)
	command := append([]string{sessionguard.Path, "run", "--expected-pod-uid", r.PodUID, "--expected-generation", r.Generation, "--ready-token", token, "--"}, s.Session.HelperCommand...)
	url := c.restClient().Post().Resource("pods").Namespace(SandboxNamespace(r.ID)).Name(sandboxPodName).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "app", Command: command, Stdin: true, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, url)
	if err != nil {
		return fmt.Errorf("cannot create the session exec transport")
	}
	budget := &sandboxOutputBudget{remaining: sandbox.MaxOutputBytes, cancel: cancel}
	handshake := &sandboxHandshake{expected: []byte("HAKOPOD_SESSION_READY " + token + "\n"), ready: make(chan struct{}), output: &sandboxOutputBound{writer: output, budget: budget}, cancel: cancel}
	in := &sandboxInputBound{reader: &sandboxGatedInput{ctx: ctx, ready: handshake.ready, reader: input}, remaining: sandbox.MaxInputBytes, cancel: cancel}
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: in, Stdout: handshake, Stderr: &sandboxOutputBound{writer: io.Discard, budget: budget}, Tty: false})
	if in.exceeded.Load() || budget.Exceeded() {
		return fmt.Errorf("session call exceeded its byte limit")
	}
	if !handshake.Verified() {
		return fmt.Errorf("session guard did not confirm the recorded identity")
	}
	if streamErr != nil {
		return fmt.Errorf("session call outcome is uncertain")
	}
	after, err := c.ObserveSession(ctx, r)
	if err != nil || !after.Ready {
		return fmt.Errorf("session container changed during the call")
	}
	return nil
}
