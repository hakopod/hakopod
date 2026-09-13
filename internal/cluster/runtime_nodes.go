package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

type NodeMetrics struct {
	Available bool       `json:"available"`
	Reason    string     `json:"reason,omitempty"`
	CPU       *float64   `json:"cpu_millicores,omitempty"`
	Memory    *int64     `json:"memory_bytes,omitempty"`
	SampledAt *time.Time `json:"sampled_at,omitempty"`
}

func controlPlane(node corev1.Node) bool {
	for _, key := range []string{"node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master", "node-role.kubernetes.io/etcd"} {
		if _, ok := node.Labels[key]; ok {
			return true
		}
	}
	return false
}
func (c *Client) nodeMetrics(ctx context.Context, nodes []Node) {
	for i := range nodes {
		nodes[i].Metrics = NodeMetrics{Reason: "metrics-server has no current sample"}
	}
	client := c.restClient()
	if client == nil {
		return
	}
	data, err := client.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").Param("limit", "200").DoRaw(ctx)
	if err != nil || len(data) > 1<<20 {
		return
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Timestamp time.Time         `json:"timestamp"`
			Usage     map[string]string `json:"usage"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil || len(list.Items) > 200 {
		return
	}
	index := map[string]int{}
	for i, node := range nodes {
		index[node.Name] = i
	}
	for _, metric := range list.Items {
		i, ok := index[metric.Metadata.Name]
		if !ok || metric.Timestamp.Before(time.Now().Add(-2*time.Minute)) || metric.Timestamp.After(time.Now().Add(30*time.Second)) {
			continue
		}
		cpu, e1 := resource.ParseQuantity(metric.Usage["cpu"])
		memory, e2 := resource.ParseQuantity(metric.Usage["memory"])
		if e1 != nil || e2 != nil || cpu.Sign() < 0 || memory.Sign() < 0 {
			continue
		}
		cpuValue := cpu.AsApproximateFloat64() * 1000
		memoryValue := memory.Value()
		stamp := metric.Timestamp
		nodes[i].Metrics = NodeMetrics{Available: true, CPU: &cpuValue, Memory: &memoryValue, SampledAt: &stamp}
	}
}

type NodeDrain struct {
	Node            string   `json:"node"`
	ResourceVersion string   `json:"resource_version"`
	Cordoned        bool     `json:"cordoned"`
	Complete        bool     `json:"complete"`
	Evicted         []string `json:"evicted"`
	Blockers        []string `json:"blockers"`
	Remaining       int      `json:"remaining"`
}

func (c *Client) nodeForAction(ctx context.Context, name, version string) (*corev1.Node, error) {
	if version == "" || len(validation.IsDNS1123Subdomain(name)) > 0 {
		return nil, fmt.Errorf("valid node name and expected_resource_version are required")
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if node.ResourceVersion != version {
		return nil, apierrors.NewConflict(corev1.Resource("nodes"), name, fmt.Errorf("node changed; refresh its resource version"))
	}
	if controlPlane(*node) {
		return nil, fmt.Errorf("control-plane and datastore nodes cannot be cordoned or drained through this API")
	}
	return node, nil
}
func (c *Client) requireOtherSchedulableNode(ctx context.Context, name string) error {
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 200})
	if err != nil {
		return err
	}
	if nodes.Continue != "" {
		return fmt.Errorf("node action exceeds 200-node limit")
	}
	for _, node := range nodes.Items {
		if node.Name == name || node.Spec.Unschedulable {
			continue
		}
		blocked := false
		for _, taint := range node.Spec.Taints {
			if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return nil
			}
		}
	}
	return fmt.Errorf("refusing to remove the last ready schedulable node")
}
func (c *Client) CordonNode(ctx context.Context, name, version string, unschedulable bool) (NodeDrain, error) {
	result := NodeDrain{Node: name, Evicted: []string{}, Blockers: []string{}}
	node, err := c.nodeForAction(ctx, name, version)
	if err != nil {
		return result, err
	}
	if unschedulable {
		if err = c.requireOtherSchedulableNode(ctx, name); err != nil {
			return result, err
		}
	}
	if node.Spec.Unschedulable != unschedulable {
		node.Spec.Unschedulable = unschedulable
		node, err = c.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		if err != nil {
			return result, err
		}
	}
	result.ResourceVersion = node.ResourceVersion
	result.Cordoned = node.Spec.Unschedulable
	return result, nil
}
func (c *Client) DrainNode(ctx context.Context, name, version string) (NodeDrain, error) {
	result := NodeDrain{Node: name, Evicted: []string{}, Blockers: []string{}}
	node, err := c.nodeForAction(ctx, name, version)
	if err != nil {
		return result, err
	}
	result.ResourceVersion = node.ResourceVersion
	result.Cordoned = node.Spec.Unschedulable
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name + ",status.phase!=Succeeded,status.phase!=Failed", Limit: 200})
	if err != nil {
		return result, err
	}
	if pods.Continue != "" {
		return result, fmt.Errorf("node has more than 200 active pods; operator drain is required")
	}
	candidates := []corev1.Pod{}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if pod.Annotations[corev1.MirrorPodAnnotationKey] != "" {
			continue
		}
		daemon, controller := false, false
		for _, owner := range pod.OwnerReferences {
			if owner.Controller != nil && *owner.Controller {
				controller = true
				daemon = owner.Kind == "DaemonSet"
			}
		}
		if daemon {
			continue
		}
		result.Remaining++
		ref := pod.Namespace + "/" + pod.Name
		if pod.Labels[managedBy] != "hakopod" {
			result.Blockers = append(result.Blockers, ref+": workload is not managed by Hakopod")
			continue
		}
		if !controller {
			result.Blockers = append(result.Blockers, ref+": pod has no controller")
			continue
		}
		blocked := false
		for _, volume := range pod.Spec.Volumes {
			if volume.EmptyDir != nil || volume.HostPath != nil {
				result.Blockers = append(result.Blockers, ref+": local pod data requires an operator drain")
				blocked = true
				break
			}
			if volume.PersistentVolumeClaim != nil {
				claim, err := c.kube.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
				if err != nil {
					return result, err
				}
				if claim.Spec.VolumeName != "" {
					pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
					if err != nil {
						return result, err
					}
					if pv.Spec.NodeAffinity != nil || pv.Spec.HostPath != nil || pv.Spec.Local != nil {
						result.Blockers = append(result.Blockers, ref+": persistent volume is bound to node-local storage")
						blocked = true
						break
					}
				}
			}
		}
		if !blocked {
			candidates = append(candidates, pod)
		}
	}
	if len(result.Blockers) > 0 {
		return result, nil
	}
	resultCordon, err := c.CordonNode(ctx, name, version, true)
	if err != nil {
		return result, err
	}
	result.Cordoned = true
	result.ResourceVersion = resultCordon.ResourceVersion
	for _, pod := range candidates {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if len(result.Evicted) >= 20 {
			result.Blockers = append(result.Blockers, "batch limit reached; refresh and drain again")
			break
		}
		eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}, DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}}}
		if err = c.kube.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction); err != nil {
			if apierrors.IsTooManyRequests(err) {
				result.Blockers = append(result.Blockers, pod.Namespace+"/"+pod.Name+": eviction blocked by disruption budget; retry later")
				continue
			}
			return result, err
		}
		result.Evicted = append(result.Evicted, pod.Namespace+"/"+pod.Name)
	}
	// Eviction acceptance is not deletion completion. A subsequent observation
	// with zero non-DaemonSet pods is the only completion signal.
	result.Complete = result.Remaining == 0
	return result, nil
}

const enrollmentLabel = "hakopod.io/enrollment"

type Enrollment struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Expired   bool      `json:"expired"`
	Token     string    `json:"token,omitempty"`
	Server    string    `json:"server,omitempty"`
}
type Enrollments struct {
	Configured bool         `json:"configured"`
	Server     string       `json:"server,omitempty"`
	Message    string       `json:"message,omitempty"`
	Items      []Enrollment `json:"items"`
}

func caBundleHash(data []byte) (string, error) {
	if len(data) == 0 || len(data) > 128<<10 {
		return "", fmt.Errorf("trusted K3s CA bundle is unavailable")
	}
	remaining := data
	certs := []*x509.Certificate{}
	for len(bytes.TrimSpace(remaining)) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" {
			return "", fmt.Errorf("invalid K3s CA certificate bundle")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		certs = append(certs, cert)
		remaining = rest
		if len(certs) > 16 {
			return "", fmt.Errorf("K3s CA bundle exceeds bounds")
		}
	}
	if len(certs) > 1 {
		roots := x509.NewCertPool()
		intermediates := x509.NewCertPool()
		for _, cert := range certs[1:] {
			if len(cert.AuthorityKeyId) == 0 || bytes.Equal(cert.AuthorityKeyId, cert.SubjectKeyId) {
				roots.AddCert(cert)
			} else {
				intermediates.AddCert(cert)
			}
		}
		chains, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
		if err != nil {
			return "", fmt.Errorf("K3s CA chain is not valid")
		}
		data = chains[0][len(chains[0])-1].Raw
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
func (c *Client) enrollmentConfiguration(ctx context.Context) (string, error) {
	u, err := url.Parse(c.options.SupervisorURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("configure HAKOPOD_K3S_SUPERVISOR_URL with the worker-reachable HTTPS K3s server URL")
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 200})
	if err != nil {
		return "", err
	}
	if len(nodes.Items) == 0 || nodes.Continue != "" {
		return "", fmt.Errorf("a supported K3s cluster is required")
	}
	for _, node := range nodes.Items {
		if !strings.Contains(node.Status.NodeInfo.KubeletVersion, "+k3s") {
			return "", fmt.Errorf("worker enrollment requires K3s")
		}
	}
	return caBundleHash(c.clusterCA)
}
func (c *Client) Enrollments(ctx context.Context) (Enrollments, error) {
	result := Enrollments{Items: []Enrollment{}}
	_, err := c.enrollmentConfiguration(ctx)
	if err != nil {
		result.Message = err.Error()
	} else {
		result.Configured = true
		result.Server = c.options.SupervisorURL
	}
	secrets, err := c.kube.CoreV1().Secrets("kube-system").List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + enrollmentLabel + "=true", Limit: 64})
	if err != nil {
		return result, err
	}
	if secrets.Continue != "" {
		return result, fmt.Errorf("more than 64 enrollment credentials exist")
	}
	for _, secret := range secrets.Items {
		if secret.Type != corev1.SecretTypeBootstrapToken {
			continue
		}
		expiry, err := time.Parse(time.RFC3339, string(secret.Data["expiration"]))
		if err != nil {
			continue
		}
		result.Items = append(result.Items, Enrollment{ID: string(secret.Data["token-id"]), CreatedAt: secret.CreationTimestamp.Time, ExpiresAt: expiry, Expired: time.Now().After(expiry)})
	}
	return result, nil
}
func randomBootstrapToken() (string, string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	value := make([]byte, 0, 22)
	for len(value) < 22 {
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", "", err
		}
		for _, b := range raw {
			if b < 252 {
				value = append(value, alphabet[int(b)%len(alphabet)])
				if len(value) == 22 {
					break
				}
			}
		}
	}
	return string(value[:6]), string(value[6:]), nil
}
func (c *Client) CreateEnrollment(ctx context.Context, ttl time.Duration) (Enrollment, error) {
	if c.CloudMode() {
		return Enrollment{}, fmt.Errorf("%w: the initial Cloud plan supports its existing node only; additional node enrollment is unavailable", ErrCloudLimit)
	}
	if ttl < 5*time.Minute || ttl > time.Hour {
		return Enrollment{}, fmt.Errorf("enrollment TTL must be between 5 and 60 minutes")
	}
	hash, err := c.enrollmentConfiguration(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	existing, err := c.Enrollments(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	active := 0
	for _, item := range existing.Items {
		if item.Expired {
			if err = c.RevokeEnrollment(ctx, item.ID); err != nil {
				return Enrollment{}, err
			}
		} else {
			active++
		}
	}
	if active >= 64 {
		return Enrollment{}, fmt.Errorf("at most 64 enrollment credentials are supported; revoke old entries first")
	}
	id, secret, err := randomBootstrapToken()
	if err != nil {
		return Enrollment{}, err
	}
	expiry := time.Now().UTC().Add(ttl).Truncate(time.Second)
	token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-token-" + id, Namespace: "kube-system", Labels: map[string]string{managedBy: "hakopod", enrollmentLabel: "true"}}, Type: corev1.SecretTypeBootstrapToken, Data: map[string][]byte{"token-id": []byte(id), "token-secret": []byte(secret), "expiration": []byte(expiry.Format(time.RFC3339)), "description": []byte("Hakopod short-lived worker enrollment"), "usage-bootstrap-authentication": []byte("true"), "usage-bootstrap-signing": []byte("true"), "auth-extra-groups": []byte("system:bootstrappers:k3s:default-node-token")}}
	saved, err := c.kube.CoreV1().Secrets("kube-system").Create(ctx, token, metav1.CreateOptions{})
	if err != nil {
		return Enrollment{}, err
	}
	return Enrollment{ID: id, CreatedAt: saved.CreationTimestamp.Time, ExpiresAt: expiry, Server: c.options.SupervisorURL, Token: "K10" + hash + "::" + id + "." + secret}, nil
}
func (c *Client) RevokeEnrollment(ctx context.Context, id string) error {
	if !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(id) {
		return fmt.Errorf("invalid enrollment ID")
	}
	api := c.kube.CoreV1().Secrets("kube-system")
	secret, err := api.Get(ctx, "bootstrap-token-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if secret.Type != corev1.SecretTypeBootstrapToken || secret.Labels[managedBy] != "hakopod" || secret.Labels[enrollmentLabel] != "true" {
		return fmt.Errorf("enrollment secret is not owned by Hakopod")
	}
	return api.Delete(ctx, secret.Name, deleteOptions(secret))
}
