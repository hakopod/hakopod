package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hakopod/hakopod/internal/store"
)

// Called only after the GitLab integration authenticates its Pipeline Hook.
// The worker re-fetches pipeline identity, committed CI bytes and job artifact.
func (s *Server) enqueueGitLabBuildWebhook(ctx context.Context, body []byte, delivery string) error {
	var payload struct {
		Kind    string `json:"object_kind"`
		Project struct {
			Path string `json:"path_with_namespace"`
		} `json:"project"`
		Attributes struct {
			ID     int64  `json:"id"`
			SHA    string `json:"sha"`
			Ref    string `json:"ref"`
			Source string `json:"source"`
			Status string `json:"status"`
			Tag    bool   `json:"tag"`
		} `json:"object_attributes"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Kind != "pipeline" || !validSourceRepository("gitlab", payload.Project.Path) || payload.Attributes.ID <= 0 || !commitPattern.MatchString(payload.Attributes.SHA) {
		return store.ErrInput
	}
	if payload.Attributes.Source != "push" || payload.Attributes.Tag || (payload.Attributes.Status != "success" && payload.Attributes.Status != "failed" && payload.Attributes.Status != "canceled" && payload.Attributes.Status != "skipped") {
		return nil
	}
	rows, err := s.Store.Pool.Query(ctx, `SELECT id FROM build_configs WHERE config->>'provider'='gitlab' AND config->>'repository'=$1 AND config->>'branch'=$2 AND config->>'auto_build'='true' AND installed_revision=revision ORDER BY id LIMIT 101`, strings.ToLower(payload.Project.Path), payload.Attributes.Ref)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 100 {
		return store.ErrInput
	}
	for _, id := range ids {
		c, err := s.readBuild(ctx, id)
		if err != nil {
			return err
		}
		if c.Provider != "gitlab" || !c.AutoBuild || c.InstalledRevision != c.Revision || c.Repository != strings.ToLower(payload.Project.Path) || c.Branch != payload.Attributes.Ref {
			continue
		}
		if err = s.enqueueAutomaticBuild(ctx, c, gitlabBuildRequestID(c.ID, payload.Attributes.ID), payload.Attributes.SHA, payload.Attributes.ID, "gitlab-pipeline-"+delivery, body); err != nil {
			return err
		}
	}
	return nil
}
