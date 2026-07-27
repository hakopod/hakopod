package cluster

import (
	"bufio"
	"container/heap"
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/hakopod/hakopod/internal/logquery"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

type LogQueryOptions struct {
	Service      string `json:"service"`
	Pod          string `json:"pod,omitempty"`
	Container    string `json:"container,omitempty"`
	Query        string `json:"query,omitempty"`
	SinceSeconds int64  `json:"since_seconds,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Tail         int64  `json:"tail,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
}
type LogBucket struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int       `json:"count"`
}
type LogQueryResult struct {
	Entries       []logquery.Entry `json:"entries"`
	Histogram     []LogBucket      `json:"histogram"`
	Scanned       int              `json:"scanned"`
	Matched       int              `json:"matched"`
	Truncated     bool             `json:"truncated"`
	Pods          int              `json:"pods"`
	WindowSeconds int64            `json:"window_seconds"`
	Warnings      []string         `json:"warnings"`
}

type retainedLog struct {
	entry logquery.Entry
	bytes int
}
type recentLogs []retainedLog

func (h recentLogs) Len() int           { return len(h) }
func (h recentLogs) Less(i, j int) bool { return h[i].entry.Timestamp.Before(h[j].entry.Timestamp) }
func (h recentLogs) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *recentLogs) Push(v any)        { *h = append(*h, v.(retainedLog)) }
func (h *recentLogs) Pop() any {
	old := *h
	v := old[len(old)-1]
	old[len(old)-1] = retainedLog{}
	*h = old[:len(old)-1]
	return v
}

func (o *LogQueryOptions) Validate() error {
	if o.Container == "" {
		o.Container = "app"
	}
	if o.SinceSeconds == 0 {
		o.SinceSeconds = 3600
	}
	if o.Limit == 0 {
		o.Limit = 500
	}
	if o.Tail == 0 {
		o.Tail = 2000
	}
	if o.SinceSeconds < 1 || o.SinceSeconds > 86400 || o.Limit < 1 || o.Limit > 1000 || o.Tail < 1 || o.Tail > 2000 || len(o.Pod) > 253 || len(o.Container) > 63 {
		return fmt.Errorf("window must be 1–86400 seconds, limit 1–1000, and tail 1–2000")
	}
	if len(validation.IsDNS1123Label(o.Container)) > 0 || o.Pod != "" && len(validation.IsDNS1123Subdomain(o.Pod)) > 0 {
		return fmt.Errorf("select a valid pod and container name")
	}
	return nil
}

// QueryLogs reads at most eight owned pods, sequentially, without storing log
// history. The histogram describes the sampled lines, never an archived index.
func (c *Client) QueryLogs(ctx context.Context, t Target, o LogQueryOptions, match logquery.Predicate) (LogQueryResult, error) {
	result := LogQueryResult{Entries: []logquery.Entry{}, Histogram: []LogBucket{}, WindowSeconds: o.SinceSeconds, Warnings: []string{"Search covers recent logs retained by Kubernetes, up to 2,000 lines per pod and eight pods. It is not a historical log archive."}}
	if _, ok := t.Spec.Services[o.Service]; !ok {
		return result, fmt.Errorf("service does not exist")
	}
	ns := Namespace(t.ApplicationID)
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	if err = owned(namespace, t); err != nil {
		return result, err
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + o.Service
	options := metav1.ListOptions{LabelSelector: selector, Limit: 64}
	if o.Pod != "" {
		options.FieldSelector = "metadata.name=" + o.Pod
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, options)
	if err != nil {
		return result, err
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.After(pods.Items[j].CreationTimestamp.Time)
	})
	result.Truncated = pods.Continue != "" || len(pods.Items) > 8
	if len(pods.Items) > 8 {
		pods.Items = pods.Items[:8]
	}
	if len(pods.Items) == 0 {
		return result, fmt.Errorf("no pods match this service and pod selector")
	}
	end := time.Now().UTC()
	bucketWidth := time.Duration(o.SinceSeconds) * time.Second / 30
	if bucketWidth < time.Second {
		bucketWidth = time.Second
	}
	start := end.Add(-time.Duration(o.SinceSeconds) * time.Second).Truncate(bucketWidth)
	for i := 0; i < 32; i++ {
		stamp := start.Add(time.Duration(i) * bucketWidth)
		if stamp.After(end) {
			break
		}
		result.Histogram = append(result.Histogram, LogBucket{Timestamp: stamp})
	}
	bytesRetained := 0
	metadataOmitted := false
	recent := recentLogs{}
	for _, pod := range pods.Items {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if err = owned(&pod, t); err != nil {
			return result, err
		}
		if pod.Labels[serviceKey] != o.Service || o.Pod != "" && pod.Name != o.Pod {
			return result, fmt.Errorf("pod does not belong to selected service")
		}
		found := false
		for _, container := range pod.Spec.Containers {
			if container.Name == o.Container {
				found = true
			}
		}
		if !found {
			result.Warnings = append(result.Warnings, "Container "+o.Container+" is absent in "+pod.Name)
			continue
		}
		stream, err := c.kube.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{Container: o.Container, Previous: o.Previous, Timestamps: true, TailLines: &o.Tail, SinceSeconds: &o.SinceSeconds, LimitBytes: ptr(int64(1 << 20))}).Stream(ctx)
		if err != nil {
			result.Warnings = append(result.Warnings, "Logs are currently unavailable for "+pod.Name)
			continue
		}
		result.Pods++
		scanner := bufio.NewScanner(io.LimitReader(stream, 1<<20))
		scanner.Buffer(make([]byte, 4096), 64<<10)
		lines, totalBytes := 0, 0
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				stream.Close()
				return result, err
			}
			lines++
			result.Scanned++
			line := scanner.Text()
			totalBytes += len(line) + 1
			e := logquery.ParseEntry(line, o.Service, pod.Name, o.Container)
			metadataOmitted = metadataOmitted || e.MetadataOmitted
			if !match(e) {
				continue
			}
			result.Matched++
			if !e.Timestamp.IsZero() {
				bucket := int(e.Timestamp.Sub(start) / bucketWidth)
				if bucket >= 0 && bucket < len(result.Histogram) {
					result.Histogram[bucket].Count++
				}
			}
			// Count parsed maps/arrays/strings as well as the raw message, so
			// compact structured JSON cannot bypass the retained-memory budget.
			entryBytes := e.RetainedBytes()
			if recent.Len() >= o.Limit || bytesRetained+entryBytes > 2<<20 {
				result.Truncated = true
				if recent.Len() > 0 && !e.Timestamp.After(recent[0].entry.Timestamp) {
					continue
				}
				for recent.Len() > 0 && (recent.Len() >= o.Limit || bytesRetained+entryBytes > 2<<20) {
					bytesRetained -= heap.Pop(&recent).(retainedLog).bytes
				}
			}
			heap.Push(&recent, retainedLog{entry: e, bytes: entryBytes})
			bytesRetained += entryBytes
		}
		if scanner.Err() != nil {
			result.Truncated = true
			result.Warnings = append(result.Warnings, "A log stream was interrupted or contained a line larger than 64 KiB: "+pod.Name)
		}
		stream.Close()
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if lines >= int(o.Tail) || totalBytes >= 1<<20-65536 {
			result.Truncated = true
		}
	}
	if result.Pods == 0 {
		return result, fmt.Errorf("no readable container logs yet")
	}
	if metadataOmitted {
		result.Warnings = append(result.Warnings, "Structured fields were omitted from some lines exceeding eight JSON levels, 128 tokens, or 64 KiB. Their original messages remain searchable.")
	}
	for _, value := range recent {
		result.Entries = append(result.Entries, value.entry)
	}
	sort.SliceStable(result.Entries, func(i, j int) bool { return result.Entries[i].Timestamp.Before(result.Entries[j].Timestamp) })
	return result, nil
}
