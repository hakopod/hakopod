package bindingprobe

import "time"

// TestResult contains only fixed diagnostic messages and runtime identities.
// Evidence is private control-plane state and must never reach API responses.
type TestResult struct {
	SchemaVersion         int             `json:"schema_version"`
	ApplicationID         string          `json:"application_id"`
	Service               string          `json:"service"`
	Variable              string          `json:"variable"`
	Revision              int64           `json:"revision"`
	Pod                   string          `json:"pod,omitempty"`
	PodUID                string          `json:"pod_uid,omitempty"`
	ObservedAt            time.Time       `json:"observed_at"`
	Outcome               string          `json:"outcome"`
	SnapshotResolved      bool            `json:"snapshot_resolved"`
	LoadedMatchesSnapshot *bool           `json:"loaded_matches_snapshot"`
	Stages                []Stage         `json:"stages"`
	Evidence              RuntimeEvidence `json:"-"`
}

type RuntimeEvidence struct {
	ContainerID   string `json:"container_id"`
	SecretUID     string `json:"secret_uid"`
	SecretVersion string `json:"secret_version"`
}

type InspectionStep struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Inspection struct {
	SchemaVersion int              `json:"schema_version"`
	ApplicationID string           `json:"application_id"`
	Service       string           `json:"service"`
	Variable      string           `json:"variable"`
	Revision      int64            `json:"revision"`
	ObservedAt    time.Time        `json:"observed_at"`
	Steps         []InspectionStep `json:"steps"`
	LastTest      *TestResult      `json:"last_test,omitempty"`
}

// EvidenceExpires bounds claims made from an earlier authenticated probe.
const EvidenceExpires = 5 * time.Minute

func NewInspection(app, service, variable string, revision int64, now time.Time, last *TestResult) Inspection {
	return Inspection{SchemaVersion: 1, ApplicationID: app, Service: service, Variable: variable, Revision: revision, ObservedAt: now, LastTest: last, Steps: []InspectionStep{
		{Name: "saved", Status: "passed", Message: "This binding is saved in the accepted application revision."},
		{Name: "resolved", Status: "unknown", Message: "The current connection settings have not been resolved."},
		{Name: "loaded", Status: "unknown", Message: "Test the connection to verify what a running container loaded."},
		{Name: "connection", Status: "unknown", Message: "No recent connection test verifies the current running container."},
	}}
}
