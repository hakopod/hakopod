// Package applogs keeps application logs after the pod that wrote them is
// gone. On the Hakopod server node the API process tails the kubelet log
// files under /var/log/pods directly; pods scheduled on other nodes are
// polled through the Kubernetes API instead. Lines are masked and stored in
// compressed per-application segment files, indexed in PostgreSQL, and read
// back by the logs API, the MCP tools, the CLI and the dashboard.
package applogs

import "time"

// Limits shared by every writer and reader. They are deliberately small and
// named so reviewers can find them.
const (
	MaxLineBytes       = 16 << 10 // longer lines are cut and marked Truncated
	SegmentSealBytes   = 4 << 20  // uncompressed bytes before a segment is sealed
	SegmentSealAge     = 5 * time.Minute
	DefaultRetention   = 7 * 24 * time.Hour
	DefaultMaxAppBytes = 512 << 20 // compressed bytes kept per application
	MaxQueryLines      = 2000
	MaxQueryScanBytes  = 64 << 20 // uncompressed bytes a single query may read
	PollInterval       = 5 * time.Second
)

// Source says how a line reached Hakopod.
type Source string

const (
	SourceFile Source = "file" // tailed from /var/log/pods on the server node
	SourceAPI  Source = "api"  // polled through the Kubernetes API (other nodes)
)

// Line is one stored log line. It is written as one JSON object per line
// (NDJSON) inside a segment file. Message is already masked.
type Line struct {
	Time      time.Time `json:"ts"`
	Stream    string    `json:"stream"` // stdout or stderr
	Message   string    `json:"msg"`
	Truncated bool      `json:"trunc,omitempty"`
}

// Instance identifies one container run inside one pod. Restart is the
// container restart count, so a crash loop produces one Instance per run.
type Instance struct {
	ApplicationID string `json:"application_id"`
	Service       string `json:"service"`
	PodUID        string `json:"pod_uid"`
	PodName       string `json:"pod_name"`
	Container     string `json:"container"`
	Restart       int    `json:"restart"`
	Revision      int64  `json:"revision"` // 0 when the ReplicaSet carried no hakopod.io/revision
	Node          string `json:"node"`
}

// Segment is the PostgreSQL index row for one segment file.
type Segment struct {
	ID        string    `json:"id"`
	Instance  Instance  `json:"instance"`
	Path      string    `json:"-"` // relative to the store root, never shown to users
	FirstTime time.Time `json:"first_time"`
	LastTime  time.Time `json:"last_time"`
	Lines     int64     `json:"lines"`
	Bytes     int64     `json:"bytes"` // on-disk (compressed once sealed)
	Sealed    bool      `json:"sealed"`
}

// EndReason explains why a pod or container stopped serving.
type EndReason string

const (
	EndRunning   EndReason = ""          // still running
	EndCompleted EndReason = "completed" // exited 0
	EndError     EndReason = "error"     // non-zero exit
	EndOOMKilled EndReason = "oom_killed"
	EndEvicted   EndReason = "evicted"
	EndReplaced  EndReason = "replaced" // removed by a rollout or scale-down
	EndDeleted   EndReason = "deleted"  // deleted for another or unknown reason
)

// PodRecord is what Hakopod remembers about a pod that served an app. It
// outlives the pod for the retention window.
type PodRecord struct {
	PodUID    string     `json:"pod_uid"`
	PodName   string     `json:"pod_name"`
	Service   string     `json:"service"`
	Revision  int64      `json:"revision"`
	Node      string     `json:"node"`
	Source    Source     `json:"source"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	EndReason EndReason  `json:"end_reason"`
	Restarts  int        `json:"restarts"`
	// GapSeconds is an upper bound on output Hakopod could not collect, for
	// example after a hard kill on a polled node or a rotation that outran
	// the tailer. Zero means no known gap. Never shown as zero when unknown.
	GapSeconds int    `json:"gap_seconds"`
	GapNote    string `json:"gap_note,omitempty"`
}
