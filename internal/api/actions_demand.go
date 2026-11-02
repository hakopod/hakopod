package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

var errInvalidJobEvent = errors.New("invalid GitHub workflow_job payload")

// recordActionsJob wakes scale-to-zero pools from a verified workflow_job
// delivery. The connection's scope bounds which pools the delivery can reach.
func (s *Server) recordActionsJob(ctx context.Context, connectionID string, body []byte) error {
	var payload struct {
		Action      string `json:"action"`
		WorkflowJob struct {
			ID     int64    `json:"id"`
			Labels []string `json:"labels"`
		} `json:"workflow_job"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.WorkflowJob.ID < 1 || !actions.ValidRepository(payload.Repository.FullName) || len(payload.WorkflowJob.Labels) > 16 {
		return errInvalidJobEvent
	}
	event := store.ActionsJobEvent{Action: payload.Action, JobID: payload.WorkflowJob.ID, Repository: payload.Repository.FullName, Labels: payload.WorkflowJob.Labels}
	if connectionID != defaultGitConnection("github") {
		c, err := s.readGitConnection(ctx, connectionID)
		if err != nil {
			return err
		}
		event.Project, event.Environment = c.Project, c.Environment
	}
	return s.Store.RecordActionsJobEvent(ctx, event)
}

// scaledActionsReplicas sizes a scale-to-zero pool: one slot per busy or queued
// job, held at its current size during the idle window, within [min, max].
func scaledActionsReplicas(a *spec.Actions, limit, busy, queued, current int, warm bool) int {
	want := busy + queued
	if warm && current > want {
		want = current
	}
	return min(limit, max(int(a.MinReplicas), want))
}
