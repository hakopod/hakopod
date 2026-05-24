package cluster

import (
	"context"
	"errors"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strconv"
	"strings"
	"time"
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
	return []ProxyField{
		{"maxconn", "Maximum concurrent connections; larger values increase memory use.", "1024"},
		{"nbthread", "HAProxy threads, bounded to 1–8.", "2"},
		{"timeout-connect", "Backend connection deadline.", "5s"},
		{"timeout-client", "Client inactivity timeout.", "30s"},
		{"timeout-server", "Backend inactivity timeout.", "30s"},
		{"timeout-tunnel", "WebSocket and upgraded-connection inactivity timeout.", "1h"},
		{"timeout-http-request", "Maximum time to receive an HTTP request.", "10s"},
		{"timeout-http-keep-alive", "Idle HTTP keep-alive connection timeout.", "5s"},
	}
}
func ValidateProxySettings(values map[string]string) error {
	if len(values) == 0 || len(values) > len(ProxyFields()) {
		return fmt.Errorf("provide between 1 and %d supported HAProxy settings", len(ProxyFields()))
	}
	supported := map[string]bool{}
	for _, f := range ProxyFields() {
		supported[f.Name] = true
	}
	for k, v := range values {
		if !supported[k] || len(v) > 32 || strings.TrimSpace(v) != v {
			return fmt.Errorf("unsupported HAProxy field or invalid value: %s", k)
		}
		if v == "" {
			continue
		} // An explicit empty value resets this controller setting.
		if k == "maxconn" || k == "nbthread" {
			n, e := strconv.Atoi(v)
			min, max := 16, 65536
			if k == "nbthread" {
				min, max = 1, 8
			}
			if e != nil || n < min || n > max {
				return fmt.Errorf("%s must be %d–%d", k, min, max)
			}
		} else {
			duration, e := time.ParseDuration(v)
			if e != nil || duration < time.Millisecond || duration > 24*time.Hour {
				return fmt.Errorf("%s must be a duration between 1ms and 24h", k)
			}
			// HAProxy accepts one integer+unit, while time.ParseDuration also accepts
			// compound and fractional units that this controller should not receive.
			unit := ""
			for _, suffix := range []string{"ms", "s", "m", "h"} {
				if strings.HasSuffix(v, suffix) {
					unit = suffix
					break
				}
			}
			if unit == "" {
				return fmt.Errorf("%s requires an integer with ms, s, m or h", k)
			}
			if _, e = strconv.ParseUint(strings.TrimSuffix(v, unit), 10, 32); e != nil {
				return fmt.Errorf("%s requires an integer duration", k)
			}
		}
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
