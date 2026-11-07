package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

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

// virtualNetworksCommand lists the environment's virtual networks, or shows
// one as canonical TOML. hakopod networks APP lists one application's networks.
func virtualNetworksCommand(ctx context.Context, c *client, cfg config, args []string, jsonOut bool, out io.Writer) error {
	if len(args) > 1 {
		return &exitError{2, "usage: hakopod virtual-networks [NAME] [--json]"}
	}
	scope := "?" + url.Values{"project": {cfg.Project}, "environment": {cfg.Environment}}.Encode()
	if len(args) == 1 {
		var network struct {
			TOML string `json:"toml"`
		}
		var raw json.RawMessage
		if err := c.request(ctx, "GET", "/virtual-networks/"+url.PathEscape(args[0])+scope, nil, "", &raw); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(raw)
		}
		if err := json.Unmarshal(raw, &network); err != nil {
			return err
		}
		_, err := io.WriteString(out, network.TOML)
		return err
	}
	var list struct {
		Items []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Segments    []string `json:"segments"`
			Revision    int64    `json:"revision"`
		} `json:"items"`
	}
	var raw json.RawMessage
	if err := c.request(ctx, "GET", "/virtual-networks"+scope, nil, "", &raw); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(raw)
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	if len(list.Items) == 0 {
		_, err := fmt.Fprintf(out, "No virtual networks in %s/%s\n", cfg.Project, cfg.Environment)
		return err
	}
	for _, n := range list.Items {
		line := fmt.Sprintf("%s\trevision %d\tsegments: %s", n.Name, n.Revision, strings.Join(n.Segments, ", "))
		if n.Description != "" {
			line += "\t" + n.Description
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}
