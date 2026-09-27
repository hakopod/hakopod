package database

import "time"

// Recovery keeps the source archive and inspection separate from the saved
// connection. Finishing a restore never switches an application's connection.
type Recovery struct {
	ArtifactID     string     `json:"artifact_id"`
	JobID          string     `json:"job_id"`
	SourceID       string     `json:"source_id,omitempty"`
	SourceRevision int64      `json:"source_revision,omitempty"`
	CapturedAt     *time.Time `json:"captured_at,omitempty"`
	RestoredAt     *time.Time `json:"restored_at,omitempty"`
	InspectedAt    *time.Time `json:"inspected_at,omitempty"`
}
