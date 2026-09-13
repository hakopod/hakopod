package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var ErrProxyConflict = errors.New("HAProxy configuration changed after review")

type ProxyField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Example     string `json:"example"`
}
type ProxyConfiguration struct {
	Namespace       string            `json:"namespace"`
	Name            string            `json:"name"`
	ResourceVersion string            `json:"resource_version"`
	Settings        map[string]string `json:"settings"`
	Fields          []ProxyField      `json:"fields"`
	AppliedRevision string            `json:"applied_revision"`
}

func ProxyFields() []ProxyField {
	// ConfigMap keys supported by HAProxy Technologies Kubernetes Ingress 3.2.
	// Keep this allowlist separate from arbitrary controller annotations/snippets.
	return []ProxyField{
		{"maxconn", "Maximum concurrent connections, 16–65536; larger values increase memory use.", "1024"},
		{"nbthread", "HAProxy threads, bounded to 1–8.", "2"},
		{"timeout-connect", "Backend connection deadline, 1ms–24h.", "5s"},
		{"timeout-client", "Client inactivity timeout, 1ms–24h.", "30s"},
		{"timeout-server", "Backend inactivity timeout, 1ms–24h.", "30s"},
		{"timeout-tunnel", "WebSocket and upgraded-connection inactivity timeout, 1ms–24h.", "1h"},
		{"timeout-http-request", "Maximum time to receive an HTTP request, 1ms–24h.", "10s"},
		{"timeout-http-keep-alive", "Idle HTTP keep-alive connection timeout, 1ms–24h.", "5s"},
		{"timeout-check", "Health-check response deadline, 100ms–5m. Service or ingress settings can override it.", "5s"},
		{"timeout-queue", "Maximum wait for an available backend connection, 1ms–1h.", "5s"},
		{"timeout-client-fin", "Client half-closed connection timeout, 1ms–1h.", "30s"},
		{"timeout-server-fin", "Backend half-closed connection timeout, 1ms–1h.", "30s"},
		{"hard-stop-after", "Reload drain deadline, 1s–24h. Remaining connections close at this deadline; longer drains retain old processes and memory.", "30m"},
		{"check-interval", "Time between enabled backend health checks, 1s–10m. Short intervals increase probe traffic.", "10s"},
		{"pod-maxconn", "Backend connection cap, 16–65536, divided across ingress replicas. Keep it above the replica count; excess requests queue.", "128"},
		{"load-balance", "Default backend algorithm: roundrobin, static-rr, leastconn, first, source or random. Service or ingress settings can override it.", "leastconn"},
		{"http-connection-mode", "HTTP connection reuse: http-keep-alive, http-server-close or httpclose. Closing connections increases connection setup work.", "http-keep-alive"},
		{"dontlognull", "Use true to omit connections that send no data; false records them and can increase log volume.", "true"},
		{"logasap", "Use true to log when response headers arrive. Final response size and duration are unavailable in these early logs; false waits for completion.", "false"},
		{"abortonclose", "Use true to cancel pending backend work after a client disconnects; false lets it finish. Service or ingress settings can override it.", "false"},
	}
}
func ValidateProxySettings(values map[string]string) error {
	fields := ProxyFields()
	if len(values) == 0 || len(values) > len(fields) {
		return fmt.Errorf("provide between 1 and %d supported HAProxy settings", len(fields))
	}
	supported := make(map[string]bool, len(fields))
	for _, f := range fields {
		supported[f.Name] = true
	}
	for k, v := range values {
		if !supported[k] || len(v) > 32 || strings.TrimSpace(v) != v {
			return fmt.Errorf("unsupported HAProxy field or invalid value: %s", k)
		}
		// An explicit empty value resets this controller setting.
		if v == "" {
			continue
		}
		switch k {
		case "maxconn", "nbthread", "pod-maxconn":
			n, e := strconv.ParseUint(v, 10, 32)
			min, max := uint64(16), uint64(65536)
			if k == "nbthread" {
				min, max = 1, 8
			}
			if e != nil || n < min || n > max {
				return fmt.Errorf("%s must be %d–%d", k, min, max)
			}
		case "dontlognull", "logasap", "abortonclose":
			if err := validateProxyChoice(k, v, "true", "false"); err != nil {
				return err
			}
		case "load-balance":
			if err := validateProxyChoice(k, v, "roundrobin", "static-rr", "leastconn", "first", "source", "random"); err != nil {
				return err
			}
		case "http-connection-mode":
			if err := validateProxyChoice(k, v, "http-keep-alive", "http-server-close", "httpclose"); err != nil {
				return err
			}
		default:
			min, max := "1ms", "24h"
			switch k {
			case "timeout-check":
				min, max = "100ms", "5m"
			case "timeout-queue", "timeout-client-fin", "timeout-server-fin":
				max = "1h"
			case "hard-stop-after":
				min = "1s"
			case "check-interval":
				min, max = "1s", "10m"
			}
			if err := validateProxyDuration(k, v, min, max); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProxyChoice(name, value string, choices ...string) error {
	if !slices.Contains(choices, value) {
		return fmt.Errorf("%s must be one of: %s", name, strings.Join(choices, ", "))
	}
	return nil
}

func validateProxyDuration(name, value, minimum, maximum string) error {
	// HAProxy accepts one integer and unit; Go also accepts compound/fractional durations.
	unit := ""
	for _, suffix := range []string{"ms", "s", "m", "h"} {
		if strings.HasSuffix(value, suffix) {
			unit = suffix
			break
		}
	}
	if unit == "" {
		return fmt.Errorf("%s requires an integer with ms, s, m or h", name)
	}
	if _, err := strconv.ParseUint(strings.TrimSuffix(value, unit), 10, 32); err != nil {
		return fmt.Errorf("%s requires an integer duration", name)
	}
	duration, err := time.ParseDuration(value)
	min, _ := time.ParseDuration(minimum)
	max, _ := time.ParseDuration(maximum)
	if err != nil || duration < min || duration > max {
		return fmt.Errorf("%s must be a duration between %s and %s", name, minimum, maximum)
	}
	return nil
}
func (c *Client) proxyConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	if c.options.ProxyNamespace == "" || c.options.ProxyConfigMap == "" || c.options.ProxyRelease == "" {
		return nil, fmt.Errorf("HAProxy configuration target is not configured")
	}
	cm, err := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if cm.Labels["app.kubernetes.io/instance"] != c.options.ProxyRelease || cm.Labels["app.kubernetes.io/name"] != "kubernetes-ingress" || cm.Annotations["meta.helm.sh/release-name"] != c.options.ProxyRelease || cm.Annotations["meta.helm.sh/release-namespace"] != c.options.ProxyNamespace {
		return nil, fmt.Errorf("HAProxy ConfigMap does not belong to the configured platform release")
	}
	return cm, nil
}
func (c *Client) ProxyConfiguration(ctx context.Context) (ProxyConfiguration, error) {
	cm, err := c.proxyConfigMap(ctx)
	if err != nil {
		return ProxyConfiguration{}, err
	}
	values := map[string]string{}
	for _, field := range ProxyFields() {
		if value, ok := cm.Data[field.Name]; ok {
			values[field.Name] = value
		}
	}
	return ProxyConfiguration{Namespace: cm.Namespace, Name: cm.Name, ResourceVersion: cm.ResourceVersion, Settings: values, Fields: ProxyFields(), AppliedRevision: cm.Annotations["hakopod.io/proxy-revision"]}, nil
}
func (c *Client) ApplyProxyConfiguration(ctx context.Context, values map[string]string, expected string, revision int64) (string, error) {
	if err := ValidateProxySettings(values); err != nil {
		return "", err
	}
	cm, err := c.proxyConfigMap(ctx)
	if err != nil {
		return "", err
	}
	revisionValue := strconv.FormatInt(revision, 10)
	matches := true
	for k, v := range values {
		matches = matches && cm.Data[k] == v
	}
	if cm.Annotations["hakopod.io/proxy-revision"] == revisionValue && matches {
		return cm.ResourceVersion, nil
	}
	if expected == "" || cm.ResourceVersion != expected {
		return "", ErrProxyConflict
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	for k, v := range values {
		if v == "" {
			delete(cm.Data, k)
		} else {
			cm.Data[k] = v
		}
	}
	cm.Annotations["hakopod.io/proxy-revision"] = revisionValue
	changed, err := c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return "", err
	}
	return changed.ResourceVersion, nil
}
