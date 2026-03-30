package cluster

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
)

func ptr[T any](v T) *T { return &v }

// Deploy is idempotent and serialized by the durable worker. It never performs
// automatic rollback: a failed group can be partial and is returned for the
// worker to record and recover as a separate auditable action.
func (c *Client) Deploy(ctx context.Context, target Target, emit func(Event)) (Observation, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	normalized, err := spec.Normalize(target.Spec)
	if err != nil {
		return Observation{}, err
	}
	target.Spec = normalized
	for name, svc := range target.Spec.Services {
		if !strings.Contains(svc.Image, "@sha256:") {
			return Observation{}, fmt.Errorf("%s: deployment requires a resolved immutable image digest", name)
		}
		if svc.Public && c.options.AppDomain == "" {
			return Observation{}, fmt.Errorf("%s: configure HAKOPOD_APP_DOMAIN before exposing a public service", name)
		}
	}
	if target.ApplicationID == "" {
		return Observation{}, fmt.Errorf("application ID is required")
	}
	if err := c.bootstrap(ctx, target); err != nil {
		return Observation{}, err
	}
	emit(Event{Type: "policy", Message: "Namespace, resource budgets and network isolation policies applied before workload changes"})
	if err := sleepContext(ctx, c.options.PolicySettleTime); err != nil {
		return Observation{}, err
	}
	// Establish stable DNS before starting containers. No service selects pods
	// outside this application; no private port is published as a NodePort.
	for _, name := range spec.Names(target.Spec) {
		svc := target.Spec.Services[name]
		if err := c.applyService(ctx, target, name, svc); err != nil {
			return c.observationAfterFailure(target), err
		}
		if !svc.Public {
			if err := c.deleteIngress(ctx, target, name); err != nil {
				return c.observationAfterFailure(target), err
			}
		}
	}
	order, _ := spec.Order(target.Spec)
	for _, name := range order {
		svc := target.Spec.Services[name]
		if err := ctx.Err(); err != nil {
			return c.observationAfterFailure(target), err
		}
		emit(Event{Type: "applying", Service: name, Message: "Applying digest-pinned Deployment with readiness-gated rolling update"})
		// Remove an old HPA before taking manual ownership of replicas.
		if svc.Autoscaling == nil {
			if err := c.applyHPA(ctx, target, name, svc); err != nil {
				return c.observationAfterFailure(target), fmt.Errorf("%s autoscaling: %w", name, err)
			}
		}
		generation, err := c.applyDeployment(ctx, target, name, svc)
		if err != nil {
			return c.observationAfterFailure(target), fmt.Errorf("%s: %w", name, err)
		}
		if svc.Autoscaling != nil {
			if err := c.applyHPA(ctx, target, name, svc); err != nil {
				return c.observationAfterFailure(target), fmt.Errorf("%s autoscaling: %w", name, err)
			}
		}
		if err := c.waitReady(ctx, target, name, generation); err != nil {
			emit(Event{Type: "failed", Service: name, Message: err.Error()})
			return c.observationAfterFailure(target), err
		}
		if svc.Public {
			if err := c.applyIngress(ctx, target, name, svc); err != nil {
				return c.observationAfterFailure(target), err
			}
		}
		emit(Event{Type: "ready", Service: name, Message: "All desired replicas are ready on the new revision"})
	}
	if err := c.cleanup(ctx, target); err != nil {
		return c.observationAfterFailure(target), err
	}
	return c.Observe(ctx, target)
}

func (c *Client) observationAfterFailure(target Target) Observation {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, err := c.Observe(ctx, target)
	if err != nil {
		return Observation{Status: "unknown", Services: []ServiceStatus{}, ObservedAt: time.Now().UTC()}
	}
	return observed
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) bootstrap(ctx context.Context, t Target) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	ns := Namespace(t.ApplicationID)
	api := c.kube.CoreV1().Namespaces()
	current, err := api.Get(ctx, ns, metav1.GetOptions{})
	labels := labelsFor(t, "")
	labels["pod-security.kubernetes.io/enforce"] = "restricted"
	labels["pod-security.kubernetes.io/enforce-version"] = "v1.35"
	labels["pod-security.kubernetes.io/warn"] = "restricted"
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels}}, metav1.CreateOptions{})
	} else if err == nil {
		if err := owned(current, t); err != nil {
			return err
		}
		for key, value := range labels {
			current.Labels[key] = value
		}
		_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("prepare application namespace: %w", err)
	}
	if err := c.applyPolicies(ctx, t); err != nil {
		return err
	}
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "hakopod-budget", Namespace: ns, Labels: labelsFor(t, "")}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourceRequestsCPU: resource.MustParse("8"), corev1.ResourceLimitsCPU: resource.MustParse("16"),
		corev1.ResourceRequestsMemory: resource.MustParse("8Gi"), corev1.ResourceLimitsMemory: resource.MustParse("16Gi"),
		corev1.ResourceRequestsEphemeralStorage: resource.MustParse("4Gi"), corev1.ResourceLimitsEphemeralStorage: resource.MustParse("8Gi"),
		corev1.ResourcePods: resource.MustParse("64"), corev1.ResourceServices: resource.MustParse("25"), corev1.ResourcePersistentVolumeClaims: resource.MustParse("0"),
	}}}
	quotaAPI := c.kube.CoreV1().ResourceQuotas(ns)
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	old, err := quotaAPI.Get(ctx, quota.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = quotaAPI.Create(ctx, quota, metav1.CreateOptions{})
	} else if err == nil {
		if err := owned(old, t); err != nil {
			return err
		}
		old.Spec = quota.Spec
		_, err = quotaAPI.Update(ctx, old, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("apply resource budget: %w", err)
	}
	return nil
}

func deployment(t Target, name string, svc spec.Service, deadline time.Duration) *appsv1.Deployment {
	labels := labelsFor(t, name)
	podLabels := labelsFor(t, name)
	for _, network := range svc.Networks {
		podLabels[networkKey(network)] = "true"
	}
	profile := spec.Profiles[svc.Size]
	container := corev1.Container{Name: "app", Image: svc.Image, ImagePullPolicy: corev1.PullIfNotPresent,
		Command: svc.Command, Args: svc.Args,
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(profile.CPURequest), corev1.ResourceMemory: resource.MustParse(profile.MemoryRequest), corev1.ResourceEphemeralStorage: resource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(profile.CPULimit), corev1.ResourceMemory: resource.MustParse(profile.MemoryLimit), corev1.ResourceEphemeralStorage: resource.MustParse("128Mi")}},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
	}
	keys := make([]string, 0, len(svc.Env))
	for key := range svc.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		container.Env = append(container.Env, corev1.EnvVar{Name: key, Value: svc.Env[key]})
	}
	if svc.Port != 0 {
		container.Ports = []corev1.ContainerPort{{Name: "service", ContainerPort: svc.Port, Protocol: corev1.ProtocolTCP}}
		handler := corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(svc.Port)}}
		if svc.Healthcheck != "" {
			handler = corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: svc.Healthcheck, Port: intstr.FromInt32(svc.Port), Scheme: corev1.URISchemeHTTP}}
		}
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 3, TimeoutSeconds: 2, FailureThreshold: 3, SuccessThreshold: 1}
		// A startup probe is deliberately TCP: a failing readiness endpoint must
		// not be silently reused as a liveness restart policy.
		container.StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(svc.Port)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 60}
	}
	seconds := int32(deadline.Seconds())
	if seconds < 30 {
		seconds = 30
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labels, Annotations: map[string]string{"hakopod.io/revision": strconv.FormatInt(t.Revision, 10), "hakopod.io/operation": t.OperationID}},
		Spec: appsv1.DeploymentSpec{Replicas: ptr(svc.Replicas), Selector: &metav1.LabelSelector{MatchLabels: labels}, RevisionHistoryLimit: ptr(int32(2)), MinReadySeconds: 2, ProgressDeadlineSeconds: &seconds,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: ptr(intstr.FromInt32(1)), MaxUnavailable: ptr(intstr.FromInt32(0))}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr(false), TerminationGracePeriodSeconds: ptr(int64(30)), EnableServiceLinks: ptr(false),
				SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(10001)), RunAsGroup: ptr(int64(10001)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{container},
			}},
		},
	}
}

func (c *Client) applyDeployment(ctx context.Context, t Target, name string, svc spec.Service) (int64, error) {
	if err := beforeStep(ctx, t); err != nil {
		return 0, err
	}
	api := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID))
	wanted := deployment(t, name, svc, c.options.RolloutTimeout)
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, err := api.Create(ctx, wanted, metav1.CreateOptions{})
		if err != nil {
			return 0, err
		}
		return created.Generation, nil
	}
	if err != nil {
		return 0, err
	}
	if err := owned(current, t); err != nil {
		return 0, err
	}
	// HPA owns /spec/replicas after creation. Secret-reload and other controller
	// annotations on both Deployment and PodTemplate retain their ownership.
	if svc.Autoscaling != nil {
		wanted.Spec.Replicas = current.Spec.Replicas
	}
	wanted.Spec.Template.Annotations = current.Spec.Template.Annotations
	for key, value := range wanted.Annotations {
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[key] = value
	}
	current.Labels = wanted.Labels
	current.Spec = wanted.Spec
	updated, err := api.Update(ctx, current, metav1.UpdateOptions{})
	if err != nil {
		return 0, err
	}
	return updated.Generation, nil
}

func (c *Client) applyService(ctx context.Context, t Target, name string, svc spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.CoreV1().Services(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if svc.Port == 0 {
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := owned(current, t); err != nil {
			return err
		}
		return api.Delete(ctx, name, deleteOptions(current))
	}
	wanted := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, name)}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: labelsFor(t, name), Ports: []corev1.ServicePort{{Name: "service", Port: svc.Port, TargetPort: intstr.FromInt32(svc.Port), Protocol: corev1.ProtocolTCP}}}}
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err := owned(current, t); err != nil {
		return err
	}
	current.Spec.Selector, current.Spec.Ports, current.Spec.Type = wanted.Spec.Selector, wanted.Spec.Ports, wanted.Spec.Type
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (c *Client) hostname(t Target, service string) string {
	// One label before the operator domain works with a wildcard certificate.
	return service + "-" + ownerID(t.ApplicationID)[:12] + "." + strings.Trim(c.options.AppDomain, ".")
}

func (c *Client) serviceURL(t Target, service string) string {
	scheme := "http"
	if c.options.TLSIssuer != "" {
		scheme = "https"
	}
	host := c.hostname(t, service)
	if c.options.PublicPort > 0 && !((scheme == "http" && c.options.PublicPort == 80) || (scheme == "https" && c.options.PublicPort == 443)) {
		host = net.JoinHostPort(host, strconv.Itoa(c.options.PublicPort))
	}
	return scheme + "://" + host
}

func (c *Client) applyIngress(ctx context.Context, t Target, name string, svc spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.NetworkingV1().Ingresses(Namespace(t.ApplicationID))
	pathType := networkingv1.PathTypePrefix
	wanted := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, name)}, Spec: networkingv1.IngressSpec{IngressClassName: ptr(c.options.IngressClass), Rules: []networkingv1.IngressRule{{Host: c.hostname(t, name), IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: name, Port: networkingv1.ServiceBackendPort{Number: svc.Port}}}}}}}}}}}
	if c.options.TLSIssuer != "" {
		wanted.Annotations = map[string]string{"cert-manager.io/cluster-issuer": c.options.TLSIssuer, "haproxy.org/ssl-redirect": "true"}
		wanted.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{c.hostname(t, name)}, SecretName: "hakopod-tls-" + name}}
	}
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err := owned(current, t); err != nil {
		return err
	}
	current.Spec, current.Labels = wanted.Spec, wanted.Labels
	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	delete(current.Annotations, "cert-manager.io/cluster-issuer")
	delete(current.Annotations, "haproxy.org/ssl-redirect")
	for key, value := range wanted.Annotations {
		current.Annotations[key] = value
	}
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func deleteOptions(meta metav1.Object) metav1.DeleteOptions {
	uid, rv := meta.GetUID(), meta.GetResourceVersion()
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
}

func (c *Client) deleteIngress(ctx context.Context, t Target, name string) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.NetworkingV1().Ingresses(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := owned(current, t); err != nil {
		return err
	}
	return api.Delete(ctx, name, deleteOptions(current))
}

func (c *Client) applyHPA(ctx context.Context, t Target, name string, svc spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.AutoscalingV2().HorizontalPodAutoscalers(Namespace(t.ApplicationID))
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if svc.Autoscaling == nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := owned(current, t); err != nil {
			return err
		}
		return api.Delete(ctx, name, deleteOptions(current))
	}
	a := svc.Autoscaling
	wanted := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, name)}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: name}, MinReplicas: &a.MinReplicas, MaxReplicas: a.MaxReplicas,
		Metrics:  []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &a.TargetCPU}}}},
		Behavior: &autoscalingv2.HorizontalPodAutoscalerBehavior{ScaleDown: &autoscalingv2.HPAScalingRules{StabilizationWindowSeconds: ptr(int32(300))}},
	}}
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, wanted, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err := owned(current, t); err != nil {
		return err
	}
	current.Spec = wanted.Spec
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (c *Client) waitReady(ctx context.Context, t Target, service string, generation int64) error {
	ctx, cancel := context.WithTimeout(ctx, c.options.RolloutTimeout)
	defer cancel()
	api := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID))
	for {
		current, err := api.Get(ctx, service, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return c.readinessError(t, service, ctx.Err())
			}
			return fmt.Errorf("%s: read rollout: %w", service, err)
		}
		if current.Generation >= generation && readyDeployment(current) {
			ready, err := c.readyPods(ctx, t, service, current)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
		// A previous ProgressDeadlineExceeded/ReplicaFailure condition can
		// survive briefly even after observedGeneration advances on recovery.
		// This operation's own bounded deadline, plus actual pod diagnostics,
		// determines failure; stale aggregate conditions cannot abort recovery.
		// Watch exactly one Deployment. Re-list on timeout, transient disconnect
		// or an expired resource version; no cluster-wide object cache exists.
		stream, err := api.Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + service, ResourceVersion: current.ResourceVersion, TimeoutSeconds: ptr(int64(25)), AllowWatchBookmarks: true})
		if err != nil {
			if err := sleepContext(ctx, time.Second); err != nil {
				return c.readinessError(t, service, err)
			}
			continue
		}
		result := func() error {
			defer stream.Stop()
			// Pod status can settle after the last Deployment event. Recheck a
			// bounded pod page periodically as well as on Deployment changes.
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					return nil
				case event, open := <-stream.ResultChan():
					if !open || event.Type == watch.Error {
						return nil
					}
					dep, ok := event.Object.(*appsv1.Deployment)
					if !ok {
						continue
					}
					if dep.Generation >= generation && readyDeployment(dep) {
						ready, err := c.readyPods(ctx, t, service, dep)
						if err != nil {
							return err
						}
						if ready {
							return errReady
						}
					}
				}
			}
		}()
		if result == errReady {
			return nil
		}
		if result != nil {
			if ctx.Err() != nil {
				return c.readinessError(t, service, ctx.Err())
			}
			return result
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			return c.readinessError(t, service, err)
		}
	}
}

// Deployment aggregate counters may temporarily refer to the old ready
// ReplicaSet as a new generation starts. Independently require actual ready
// pods to use the current PodTemplate before reporting the release succeeded.
func (c *Client) readyPods(ctx context.Context, t Target, service string, dep *appsv1.Deployment) (bool, error) {
	pods, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service, Limit: 64})
	if err != nil {
		return false, fmt.Errorf("%s: verify ready pod template: %w", service, err)
	}
	if pods.Continue != "" {
		return false, fmt.Errorf("%s: more than 64 pods exceed the supported verification limit", service)
	}
	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	active := int32(0)
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		active++
		if !podReady(pod) || !reflect.DeepEqual(pod.Spec.Containers, dep.Spec.Template.Spec.Containers) {
			return false, nil
		}
		for key, value := range dep.Spec.Template.Labels {
			if pod.Labels[key] != value {
				return false, nil
			}
		}
	}
	return active == desired, nil
}

var errReady = fmt.Errorf("ready")

func readyDeployment(d *appsv1.Deployment) bool {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == desired && d.Status.ReadyReplicas == desired && d.Status.AvailableReplicas == desired && d.Status.Replicas == desired
}

func deploymentFailure(d *appsv1.Deployment) string {
	// A newly applied recovery revision can retain the previous failed status
	// until the Deployment controller observes its generation.
	if d.Status.ObservedGeneration < d.Generation {
		return ""
	}
	for _, condition := range d.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
			return "rollout deadline exceeded; inspect readiness, image startup and available capacity"
		}
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return condition.Reason + ": " + condition.Message
		}
	}
	return ""
}

func (c *Client) readinessError(t Target, service string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	message := c.podMessage(ctx, t, service)
	if message == "" {
		message = "pods did not become ready; check application health and cluster capacity"
	}
	return fmt.Errorf("%s: rollout stopped (%v): %s", service, cause, message)
}

// cleanup discovers bounded, owned stale resources as well as the previous
// revision. Discovery matters after cancellation of a first/partial release,
// where there may be no last healthy revision to enumerate.
func (c *Client) cleanup(ctx context.Context, t Target) error {
	ns := Namespace(t.ApplicationID)
	names := make(map[string]bool)
	if t.Previous != nil {
		for _, name := range spec.Names(*t.Previous) {
			names[name] = true
		}
	}
	options := metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID), Limit: 100}
	deployments, err := c.kube.AppsV1().Deployments(ns).List(ctx, options)
	if err != nil {
		return err
	}
	if deployments.Continue != "" {
		return fmt.Errorf("too many owned Deployments for bounded cleanup")
	}
	for _, item := range deployments.Items {
		if item.Labels[serviceKey] == item.Name {
			names[item.Name] = true
		}
	}
	services, err := c.kube.CoreV1().Services(ns).List(ctx, options)
	if err != nil {
		return err
	}
	if services.Continue != "" {
		return fmt.Errorf("too many owned Services for bounded cleanup")
	}
	for _, item := range services.Items {
		if item.Labels[serviceKey] == item.Name {
			names[item.Name] = true
		}
	}
	ingresses, err := c.kube.NetworkingV1().Ingresses(ns).List(ctx, options)
	if err != nil {
		return err
	}
	if ingresses.Continue != "" {
		return fmt.Errorf("too many owned Ingresses for bounded cleanup")
	}
	for _, item := range ingresses.Items {
		if item.Labels[serviceKey] == item.Name {
			names[item.Name] = true
		}
	}
	autoscalers, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, options)
	if err != nil {
		return err
	}
	if autoscalers.Continue != "" {
		return fmt.Errorf("too many owned HPAs for bounded cleanup")
	}
	for _, item := range autoscalers.Items {
		if item.Labels[serviceKey] == item.Name {
			names[item.Name] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		if _, ok := t.Spec.Services[name]; ok {
			continue
		}
		if err := c.deleteIngress(ctx, t, name); err != nil {
			return err
		}
		if err := c.applyHPA(ctx, t, name, spec.Service{}); err != nil {
			return err
		}
		if err := c.applyService(ctx, t, name, spec.Service{}); err != nil {
			return err
		}
		api := c.kube.AppsV1().Deployments(ns)
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		current, err := api.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := owned(current, t); err != nil {
			return err
		}
		if err := api.Delete(ctx, name, deleteOptions(current)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
