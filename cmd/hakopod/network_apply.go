package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"

	"github.com/hakopod/hakopod/internal/spec"
)

type networkPlan struct {
	Spec             spec.VirtualNetwork  `json:"spec"`
	Previous         *spec.VirtualNetwork `json:"previous"`
	TOML             string               `json:"toml"`
	ExpectedID       string               `json:"expected_id"`
	ExpectedRevision int64                `json:"expected_revision"`
}

// Unchanged reports whether the plan would change nothing (Previous != nil and semantically equal to Spec).
func (p networkPlan) Unchanged() bool {
	if p.Previous == nil {
		return false
	}
	a, errA := json.Marshal(p.Spec)
	b, errB := json.Marshal(*p.Previous)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

type networkResult struct {
	Name     string `json:"name"`
	Action   string `json:"action"` // "created" | "updated" | "unchanged"
	Revision int64  `json:"revision"`
}

func planNetwork(ctx context.Context, c *client, project, environment string, data []byte) (networkPlan, error) {
	var p networkPlan
	in := map[string]any{"project": project, "environment": environment, "toml": string(data)}
	err := c.request(ctx, "POST", "/virtual-networks/plan", in, "", &p)
	return p, err
}

// applyNetwork creates (Previous == nil), updates (changed) or skips (Unchanged) using the
// reviewed expected_id/expected_revision from the plan, so a concurrent change is a server conflict.
func applyNetwork(ctx context.Context, c *client, project, environment string, p networkPlan) (networkResult, error) {
	res := networkResult{Name: p.Spec.Name, Action: "unchanged", Revision: p.ExpectedRevision}
	if p.Unchanged() {
		return res, nil
	}
	// Submit the canonical reviewed spec, not the raw TOML.
	in := map[string]any{"project": project, "environment": environment, "spec": p.Spec, "expected_revision": p.ExpectedRevision}
	method, path := "POST", "/virtual-networks"
	res.Action = "created"
	if p.Previous != nil {
		in["expected_id"] = p.ExpectedID
		method, path = "PUT", "/virtual-networks/"+url.PathEscape(p.Spec.Name)
		res.Action = "updated"
	}
	var out struct {
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
	if err := c.request(ctx, method, path, in, "", &out); err != nil {
		return networkResult{}, err
	}
	res.Name, res.Revision = out.Name, out.Revision
	return res, nil
}
