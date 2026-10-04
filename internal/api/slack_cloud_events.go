package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

// RunSlackCloudEvents forwards the committed Cloud-target outbox. A failed or
// unmapped workspace is handled by the sink outcome and never blocks a local
// self-hosted Slack worker.
func (s *Server) RunSlackCloudEvents(ctx context.Context) {
	if s.SlackCloudEvents == nil {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.forwardSlackCloudEvents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) forwardSlackCloudEvents(parent context.Context) {
	for i := 0; i < 2 && parent.Err() == nil; i++ {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		job, err := s.Store.ClaimSlackCloudEvent(ctx)
		if err != nil || job == nil {
			cancel()
			return
		}
		event, decodeErr := cloudSlackEvent(*job)
		// Store outcomes are durable delivery states. The Cloud protocol's retry
		// receipt means this event remains pending locally with a released lease.
		outcome, message := "pending", "Cloud event delivery failed; retry scheduled."
		if decodeErr != nil {
			outcome, message = "skipped", "Cloud event payload is invalid."
		} else if result, sendErr := s.SlackCloudEvents.Enqueue(ctx, event); sendErr == nil {
			switch result {
			case SlackCloudAccepted:
				outcome, message = "sent", ""
			case SlackCloudSkipped:
				outcome, message = "skipped", "Cloud did not map this event to an eligible workspace."
			default:
				outcome, message = "pending", "Cloud event delivery failed; retry scheduled."
			}
		}
		cancel()
		finish, stop := context.WithTimeout(parent, 3*time.Second)
		_ = s.Store.FinishSlackCloudEvent(finish, *job, outcome, message)
		stop()
	}
}
func cloudSlackEvent(job store.SlackCloudDelivery) (SlackCloudEvent, error) {
	var payload struct {
		Project     string `json:"project"`
		Environment string `json:"environment"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || payload.Project == "" || payload.Environment == "" || len(payload.Project) > 80 || len(payload.Environment) > 80 {
		return SlackCloudEvent{}, fmt.Errorf("invalid payload")
	}
	return SlackCloudEvent{ID: slackCloudEventID(job.SourceID, job.Kind, job.SourceEventID), Kind: job.Kind, SourceID: job.SourceID, Project: payload.Project, Environment: payload.Environment, Payload: json.RawMessage(job.Payload)}, nil
}

// slackCloudEventID is deterministic across retries but remains below the
// Cloud event identifier limit even when a customer chooses a 128-byte source.
func slackCloudEventID(sourceID, kind string, sourceEventID int64) string {
	input := fmt.Sprintf("%d:%s:%d:%s:%d", len(sourceID), sourceID, len(kind), kind, sourceEventID)
	digest := sha256.Sum256([]byte(input))
	return "slack:" + hex.EncodeToString(digest[:])
}
