package cluster

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/logmask"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AppLogPod describes one application pod for the historic log collector.
// Reason is the pod's Status.Reason, which is "Evicted" after an eviction.
type AppLogPod struct {
	ApplicationID, Service, Namespace, Name, UID, Node, Reason string
	Revision                                                   int64
	StartedAt                                                  time.Time
	Terminating                                                bool
	Containers                                                 []AppLogContainer
}

// AppLogContainer is one container's current run and, when it restarted, the
// run before it.
type AppLogContainer struct {
	Name             string
	Restart          int
	Running          bool
	Terminated       bool
	Reason           string
	ExitCode         int32
	PreviousRestart  int
	PreviousReason   string
	PreviousExitCode int32
}

// AppLogPods lists pods Hakopod owns in hp-* namespaces, at most limit of them.
// Pods past the limit are left for a later tick, never guessed at.
func (c *Client) AppLogPods(ctx context.Context, limit int) ([]AppLogPod, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 2000 {
		limit = 2000
	}
	list, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod", Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	revisions := map[string]int64{}
	out := make([]AppLogPod, 0, len(list.Items))
	for _, p := range list.Items {
		if !strings.HasPrefix(p.Namespace, "hp-") || p.Labels[ownerKey] == "" || managedRunnerDiagnostics(p) {
			continue
		}
		out = append(out, c.appLogPod(ctx, p, revisions))
	}
	return out, nil
}

func (c *Client) appLogPod(ctx context.Context, p corev1.Pod, revisions map[string]int64) AppLogPod {
	started := p.CreationTimestamp.Time
	if p.Status.StartTime != nil {
		started = p.Status.StartTime.Time
	}
	pod := AppLogPod{ApplicationID: p.Labels[ownerKey], Service: p.Labels[serviceKey], Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), Node: p.Spec.NodeName,
		Reason: p.Status.Reason, StartedAt: started, Terminating: p.DeletionTimestamp != nil, Revision: c.podRevision(ctx, p, revisions)}
	for _, s := range p.Status.ContainerStatuses {
		k := AppLogContainer{Name: s.Name, Restart: int(s.RestartCount), Running: s.State.Running != nil}
		if t := s.State.Terminated; t != nil {
			k.Terminated, k.Reason, k.ExitCode = true, t.Reason, t.ExitCode
		}
		if t := s.LastTerminationState.Terminated; t != nil && s.RestartCount > 0 {
			k.PreviousRestart, k.PreviousReason, k.PreviousExitCode = int(s.RestartCount)-1, t.Reason, t.ExitCode
		}
		pod.Containers = append(pod.Containers, k)
	}
	return pod
}

// podRevision reads hakopod.io/revision from the owning ReplicaSet, which
// Kubernetes copies from the Deployment. Lookups are cached for the call.
func (c *Client) podRevision(ctx context.Context, p corev1.Pod, cache map[string]int64) int64 {
	for _, o := range p.OwnerReferences {
		if o.Kind != "ReplicaSet" {
			continue
		}
		key := p.Namespace + "/" + o.Name
		if v, ok := cache[key]; ok {
			return v
		}
		var rev int64
		if rs, err := c.kube.AppsV1().ReplicaSets(p.Namespace).Get(ctx, o.Name, metav1.GetOptions{}); err == nil {
			rev, _ = strconv.ParseInt(rs.Annotations["hakopod.io/revision"], 10, 64)
		}
		cache[key] = rev
		return rev
	}
	return 0
}

// PodLogSince reads timestamped output newer than since, at most limitBytes.
// It returns raw output; the caller masks it before anything is stored.
func (c *Client) PodLogSince(ctx context.Context, namespace, pod, container string, previous bool, since time.Time, limitBytes int64) ([]byte, error) {
	if limitBytes < 1 || limitBytes > 8<<20 {
		limitBytes = 8 << 20
	}
	o := &corev1.PodLogOptions{Container: container, Previous: previous, Timestamps: true, LimitBytes: &limitBytes}
	if !since.IsZero() {
		o.SinceTime = &metav1.Time{Time: since}
	}
	stream, err := c.kube.CoreV1().Pods(namespace).GetLogs(pod, o).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return io.ReadAll(io.LimitReader(stream, limitBytes))
}

// AppLogMasking masks lines for every container of one pod. It exposes the
// private-key block state so a reader can carry it across a restart.
type AppLogMasking struct{ all []*logmask.Masker }

func (m *AppLogMasking) Line(s string) string {
	for _, k := range m.all {
		s = k.Line(s)
	}
	return s
}

func (m *AppLogMasking) Added() []string {
	var out []string
	for _, k := range m.all {
		out = append(out, k.Added()...)
	}
	return out
}

func (m *AppLogMasking) Add(v string) {
	for _, k := range m.all {
		k.Add(v)
	}
}

func (m *AppLogMasking) InPrivateKey() bool {
	for _, k := range m.all {
		if k.InPrivateKey() {
			return true
		}
	}
	return false
}

func (m *AppLogMasking) ResumePrivateKey() {
	for _, k := range m.all {
		k.ResumePrivateKey()
	}
}

// AppLogMasker builds the masker from the live pod. If any container's masker
// cannot be built it fails, so the caller never stores unmasked lines.
func (c *Client) AppLogMasker(ctx context.Context, namespace, pod string) (*AppLogMasking, error) {
	p, err := c.kube.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := &AppLogMasking{}
	for _, k := range p.Spec.Containers {
		m, err := c.podLogMasker(ctx, *p, k.Name)
		if err != nil {
			return nil, err
		}
		out.all = append(out.all, m)
	}
	return out, nil
}
