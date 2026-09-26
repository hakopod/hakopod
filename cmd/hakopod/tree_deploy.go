package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type appPlan struct {
	ApplicationID    string           `json:"application_id"`
	ExpectedRevision int64            `json:"expected_revision"`
	Spec             spec.Application `json:"spec"`
	Changes          []spec.Change    `json:"changes"`
	Warnings         []string         `json:"warnings"`
	ResourceProfiles any              `json:"resource_profiles"`
	MissingSecrets   []string         `json:"missing_secrets"`
}

func planApplication(ctx context.Context, c *client, cfg config, data []byte, envFiles map[string]string, service string) (appPlan, error) {
	var plan appPlan
	in := map[string]any{"project": cfg.Project, "environment": cfg.Environment, "toml": string(data)}
	if len(envFiles) > 0 {
		in["env_files"] = envFiles
	}
	if service != "" {
		in["service"] = service
	}
	err := c.request(ctx, "POST", "/plan", in, "", &plan)
	return plan, err
}

// submitDeployment sends the canonical reviewed full revision so retries do not merge a
// target into a later application state under a reused idempotency key.
func submitDeployment(ctx context.Context, c *client, cfg config, plan appPlan, idem string, jsonOut bool) (store.Deployment, error) {
	in := map[string]any{"project": cfg.Project, "environment": cfg.Environment, "spec": plan.Spec, "expected_revision": plan.ExpectedRevision}
	if !jsonOut {
		fmt.Fprintf(os.Stderr, "Submitting %s revision %d (%d changes); retry key %s\n", plan.Spec.Name, plan.ExpectedRevision+1, len(plan.Changes), idem)
	}
	var d store.Deployment
	err := c.request(ctx, "POST", "/deployments", in, idem, &d)
	return d, err
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func exitCode(err error) int {
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

func validateTree(root string, only []string, jsonOut bool) error {
	tree, err := loadTree(root, only)
	if err != nil {
		return &exitError{2, err.Error()}
	}
	type appOut struct {
		Dir          string `json:"dir"`
		Name         string `json:"name"`
		ServiceCount int    `json:"service_count"`
	}
	apps := []appOut{}
	for _, a := range tree.Apps {
		apps = append(apps, appOut{a.Dir, a.Spec.Name, len(a.Spec.Services)})
	}
	if jsonOut {
		return printJSON(map[string]any{"valid": true, "network": tree.Network, "applications": apps})
	}
	if tree.Network != nil {
		fmt.Printf("Valid network: %s, %d segment(s)\n", tree.Network.Name, len(tree.Network.Segments))
	}
	for _, a := range tree.Apps {
		fmt.Printf("Valid: %s, %d service(s), schema v%d\n", a.Spec.Name, len(a.Spec.Services), a.Spec.SchemaVersion)
	}
	return nil
}

type treeAppResult struct {
	Dir          string   `json:"dir"`
	Name         string   `json:"name"`
	Plan         *appPlan `json:"plan,omitempty"`
	Status       string   `json:"status,omitempty"` // deploy: applied | unchanged | failed | not-run
	DeploymentID string   `json:"deployment_id,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// networkAccessError explains the common case: machine keys cannot manage virtual networks
// (403 from the server, 404 from the dashboard proxy that does not forward the route).
func networkAccessError(err error) error {
	msg := "network.toml: " + err.Error()
	var e *exitError
	if errors.As(err, &e) && (e.code == 3 || strings.HasPrefix(e.message, "not_found:")) || strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "HTTP 404") {
		msg += "; managing virtual networks requires a project administrator. Apply network.toml with an administrator login, or rerun with --no-network to deploy only the applications."
	}
	return &exitError{exitCode(err), msg}
}

func networkAction(p networkPlan) string {
	switch {
	case p.Previous == nil:
		return "create"
	case p.Unchanged():
		return "unchanged"
	}
	return "update"
}

// treeCommand plans (and for deploy, applies) a folder tree. Everything is validated
// locally and every application is planned before any application is deployed. The
// network goes first because application plans check its grants. Never deletes anything.
func treeCommand(ctx context.Context, c *client, cfg config, command, root string, only []string, noNetwork, wait, jsonOut bool) error {
	tree, err := loadTree(root, only)
	if err != nil {
		return &exitError{2, err.Error()}
	}
	var np *networkPlan
	var netOut any // JSON network field; stays nil (null) without network.toml
	if tree.Network != nil && noNetwork {
		netOut = map[string]string{"name": tree.Network.Name, "action": "skipped"}
		if !jsonOut {
			fmt.Fprintf(os.Stderr, "Network %s: skipped (--no-network)\n", tree.Network.Name)
		}
	} else if tree.Network != nil {
		p, err := planNetwork(ctx, c, cfg.Project, cfg.Environment, tree.NetworkData)
		if err != nil {
			return networkAccessError(err)
		}
		np, netOut = &p, &p
	}
	if command == "deploy" && np != nil {
		r, err := applyNetwork(ctx, c, cfg.Project, cfg.Environment, *np)
		if err != nil {
			return networkAccessError(err)
		}
		netOut = &r
		if !jsonOut {
			fmt.Fprintf(os.Stderr, "Network %s: %s (revision %d)\n", r.Name, r.Action, r.Revision)
		}
	}

	results := make([]treeAppResult, len(tree.Apps))
	plans := make([]appPlan, len(tree.Apps))
	var firstErr error
	for i, a := range tree.Apps {
		results[i] = treeAppResult{Dir: a.Dir, Name: a.Spec.Name, Status: "not-run"}
		if plans[i], err = planApplication(ctx, c, cfg, a.Data, a.EnvFiles, ""); err != nil {
			results[i].Error, results[i].Status = err.Error(), "failed"
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if command == "plan" {
		for i := range results {
			results[i].Status = ""
			if results[i].Error == "" {
				results[i].Plan = &plans[i]
			}
		}
		if jsonOut {
			if err := printJSON(map[string]any{"network": netOut, "applications": results}); err != nil {
				return err
			}
		} else {
			if np != nil {
				fmt.Fprintf(os.Stderr, "Network %s: %s\n", np.Spec.Name, networkAction(*np))
			}
			for i, r := range results {
				if r.Error != "" {
					fmt.Fprintf(os.Stderr, "%s (%s/): plan failed: %s\n", r.Name, r.Dir, r.Error)
					continue
				}
				p := plans[i]
				fmt.Fprintf(os.Stderr, "%s (%s/): revision %d, %d change(s)", r.Name, r.Dir, p.ExpectedRevision+1, len(p.Changes))
				if len(p.MissingSecrets) > 0 {
					fmt.Fprintf(os.Stderr, "; missing secrets: %s", strings.Join(p.MissingSecrets, ", "))
				}
				fmt.Fprintln(os.Stderr)
				for _, w := range p.Warnings {
					fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
				}
			}
		}
		if firstErr != nil {
			msg := "one or more application plans failed"
			if np != nil && networkAction(*np) != "unchanged" {
				msg += "; network.toml grants take effect once the network is applied (deploy applies it first)"
			}
			return &exitError{exitCode(firstErr), msg}
		}
		return nil
	}

	// Deploy: secrets are prompted for every application before any deployment is submitted.
	if firstErr == nil {
		for i := range tree.Apps {
			if err := setupDeploymentSecrets(ctx, c, cfg.Project, cfg.Environment, plans[i].Spec, plans[i].MissingSecrets, jsonOut); err != nil {
				results[i].Error, results[i].Status = err.Error(), "failed"
				firstErr = err
				break
			}
		}
	}
	applied := 0
	for i := range tree.Apps {
		if firstErr != nil {
			break
		}
		// A zero-change revision still rolls every pod, so a rerun after a partial failure
		// would restart applications that already succeeded. Skip only healthy ones: the
		// diff is against the current spec, which a failed release has already replaced.
		if len(plans[i].Changes) == 0 && plans[i].ApplicationID != "" {
			var current store.Application
			if err := c.request(ctx, "GET", "/applications/"+url.PathEscape(plans[i].ApplicationID), nil, "", &current); err == nil && current.Status == "healthy" {
				results[i].Status = "unchanged"
				continue
			}
		}
		d, err := submitDeployment(ctx, c, cfg, plans[i], store.NewID(), jsonOut)
		if err == nil && wait {
			d, err = waitDeployment(ctx, c, d)
		}
		results[i].DeploymentID = d.ID
		if err == nil && (d.Status == "failed" || d.Status == "cancelled" || d.Status == "superseded") {
			err = &exitError{5, "deployment did not succeed: " + d.Status}
			if d.Error != "" {
				err = &exitError{5, "deployment did not succeed: " + d.Status + ": " + d.Error}
			}
		}
		if err != nil {
			results[i].Error, results[i].Status = err.Error(), "failed"
			firstErr = err
			break
		}
		results[i].Status = "applied"
		applied++
	}

	note := ""
	if firstErr != nil && applied > 0 {
		note = "Not atomic: earlier applications stay deployed; rerun after fixing — healthy applications with no changes are skipped."
	}
	if jsonOut {
		out := map[string]any{"network": netOut, "applications": results}
		if note != "" {
			out["note"] = note
		}
		if err := printJSON(out); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			line := fmt.Sprintf("%-8s %s (%s/)", r.Status, r.Name, r.Dir)
			if r.DeploymentID != "" {
				line += " deployment " + r.DeploymentID
			}
			if r.Error != "" {
				line += ": " + r.Error
			}
			fmt.Fprintln(os.Stderr, line)
		}
		if note != "" {
			fmt.Fprintln(os.Stderr, note)
		}
	}
	if firstErr != nil {
		return &exitError{exitCode(firstErr), fmt.Sprintf("%d of %d application(s) applied; stopped at first failure", applied, len(tree.Apps))}
	}
	return nil
}
