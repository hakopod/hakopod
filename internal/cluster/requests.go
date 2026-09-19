package cluster

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/requestlog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfigureRequestLogs only manages the pinned platform ingress, and never
// replaces an operator's custom access logger. Serverless activation remains
// behind this ingress, so its wait time is included in request duration.
func (c *Client) ConfigureRequestLogs(ctx context.Context) error {
	cm, err := c.proxyConfigMap(ctx)
	if err != nil {
		return fmt.Errorf("Managed HAProxy ingress is unavailable; request collection is not configured.")
	}
	wanted := map[string]string{"log-format": requestlog.Format, "syslog-server": requestlog.Syslog, "logasap": "false"}
	for key, val := range wanted {
		if current := cm.Data[key]; current != "" && current != val {
			return fmt.Errorf("HAProxy has custom logging settings. Configure the documented Requests log format to enable collection.")
		}
	}
	changed := false
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	for key, val := range wanted {
		if cm.Data[key] != val {
			cm.Data[key] = val
			changed = true
		}
	}
	if changed {
		_, err = c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	return err
}

type RequestBatch struct {
	Entries []requestlog.Entry
	Cursors map[string]time.Time
	Gaps    int
}

func (c *Client) CollectRequests(ctx context.Context, cursors map[string]time.Time) (RequestBatch, error) {
	result := RequestBatch{Entries: []requestlog.Entry{}, Cursors: map[string]time.Time{}}
	pods, err := c.kube.CoreV1().Pods(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kubernetes-ingress,app.kubernetes.io/instance=" + c.options.ProxyRelease, FieldSelector: "status.phase=Running", Limit: 8})
	if err != nil {
		return result, err
	}
	if len(pods.Items) == 0 {
		return result, fmt.Errorf("No ingress pods are available for collection.")
	}
	if pods.Continue != "" {
		result.Gaps++
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			result.Gaps++
			continue
		}
		// Container names come from the pinned ingress pod rather than an assumption.
		container := ""
		for _, v := range pod.Spec.Containers {
			for _, arg := range v.Args {
				if arg == "--ingress.class="+c.options.IngressClass {
					container = v.Name
				}
			}
		}
		if container == "" {
			result.Gaps++
			continue
		}
		source := string(pod.UID)
		since := cursors[source]
		if since.IsZero() {
			since = time.Now().Add(-time.Minute)
		} else {
			since = since.Add(-2 * time.Second)
		}
		if since.Before(time.Now().Add(-24 * time.Hour)) {
			since = time.Now().Add(-24 * time.Hour)
			result.Gaps++
		}
		limit := int64(4 << 20)
		tail := int64(20000)
		stamp := metav1.NewTime(since)
		stream, e := c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, Timestamps: true, SinceTime: &stamp, LimitBytes: &limit, TailLines: &tail}).Stream(ctx)
		if e != nil {
			result.Gaps++
			continue
		}
		scanner := bufio.NewScanner(io.LimitReader(stream, limit))
		scanner.Buffer(make([]byte, 4096), 16384)
		lines, bytes := 0, 0
		latest := since
		for scanner.Scan() {
			line := scanner.Text()
			lines++
			bytes += len(line) + 1
			if token, _, ok := strings.Cut(line, " "); ok {
				if at, e := time.Parse(time.RFC3339Nano, token); e == nil && at.After(latest) {
					latest = at
				}
			}
			if !strings.Contains(line, "HAKOPOD_REQUEST_V1|") {
				continue
			}
			entry, e := requestlog.Parse(line, source, pod.Name)
			if e != nil {
				result.Gaps++
				continue
			}
			if len(result.Entries) >= 5000 {
				result.Gaps++
				continue
			}
			result.Entries = append(result.Entries, entry)
		}
		if scanner.Err() != nil || lines >= int(tail) || bytes >= int(limit)-16384 {
			result.Gaps++
		}
		stream.Close()
		result.Cursors[source] = latest
	}
	return result, nil
}

type RequestRoute struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Port    int32  `json:"port"`
	TLS     bool   `json:"tls"`
	Ingress string `json:"ingress"`
}
type RequestEndpoint struct {
	Pod     string `json:"pod"`
	Address string `json:"address"`
	Ready   bool   `json:"ready"`
}
type RequestRouting struct {
	ObservedAt time.Time         `json:"observed_at"`
	Routes     []RequestRoute    `json:"routes"`
	Endpoints  []RequestEndpoint `json:"endpoints"`
	Service    string            `json:"service"`
	Namespace  string            `json:"namespace"`
	Warnings   []string          `json:"warnings"`
}

func (c *Client) RequestRouting(ctx context.Context, t Target, service string) (RequestRouting, error) {
	out := RequestRouting{ObservedAt: time.Now().UTC(), Routes: []RequestRoute{}, Endpoints: []RequestEndpoint{}, Warnings: []string{}, Service: service, Namespace: Namespace(t.ApplicationID)}
	if _, ok := t.Spec.Services[service]; !ok {
		return out, fmt.Errorf("service does not exist")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, out.Namespace, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	if err = owned(ns, t); err != nil {
		return out, err
	}
	ingress, err := c.kube.NetworkingV1().Ingresses(out.Namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return out, err
	}
	if err == nil {
		if err = owned(ingress, t); err != nil {
			return out, err
		}
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			tls := false
			for _, v := range ingress.Spec.TLS {
				for _, host := range v.Hosts {
					tls = tls || host == rule.Host
				}
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service != nil && (path.Backend.Service.Name == service || t.Spec.Services[service].Serverless != nil && path.Backend.Service.Name == ActivationServiceName(service)) {
					out.Routes = append(out.Routes, RequestRoute{Host: rule.Host, Path: path.Path, Port: path.Backend.Service.Port.Number, TLS: tls, Ingress: ingress.Name})
				}
			}
		}
	}
	svc, err := c.kube.CoreV1().Services(out.Namespace).Get(ctx, service, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		out.Warnings = append(out.Warnings, "No Kubernetes Service is currently deployed.")
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = owned(svc, t); err != nil {
		return out, err
	}
	slices, err := c.kube.DiscoveryV1().EndpointSlices(out.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + service, Limit: 8})
	if err != nil {
		return out, err
	}
	for _, slice := range slices.Items {
		ownedSlice := false
		for _, ref := range slice.OwnerReferences {
			ownedSlice = ownedSlice || ref.UID == svc.UID
		}
		if !ownedSlice {
			continue
		}
		for _, ep := range slice.Endpoints {
			for _, ip := range ep.Addresses {
				if len(out.Endpoints) >= 64 {
					break
				}
				pod := ""
				if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" {
					pod = ep.TargetRef.Name
				}
				out.Endpoints = append(out.Endpoints, RequestEndpoint{Pod: pod, Address: ip, Ready: ep.Conditions.Ready != nil && *ep.Conditions.Ready})
			}
		}
	}
	if slices.Continue != "" || len(out.Endpoints) >= 64 {
		out.Warnings = append(out.Warnings, "Endpoint view is limited to 64 addresses and eight slices.")
	}
	if t.Spec.Services[service].Serverless != nil {
		out.Warnings = append(out.Warnings, "Public requests pass through the activation gateway, which wakes sleeping containers before forwarding. Cold-start time is included in request duration.")
	}
	if len(out.Routes) == 0 {
		out.Warnings = append(out.Warnings, "This service has no HTTP ingress route. Internal and raw TCP traffic is not captured by Requests.")
	}
	return out, nil
}
