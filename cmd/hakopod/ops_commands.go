package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
)

// Everyday operations that used to need `api call` or a hand-edited spec.

type appSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Revision    int64  `json:"revision"`
	Status      string `json:"status"`
}

func listApplications(ctx context.Context, c *client, cfg config) ([]appSummary, error) {
	var out []appSummary
	cursor := ""
	for {
		var page struct {
			Items      []store.Application `json:"items"`
			NextCursor string              `json:"next_cursor"`
		}
		path := "/applications?" + url.Values{"project": {cfg.Project}, "environment": {cfg.Environment}, "cursor": {cursor}}.Encode()
		if err := c.request(ctx, "GET", path, nil, "", &page); err != nil {
			return nil, err
		}
		for _, a := range page.Items {
			out = append(out, appSummary{a.ID, a.Name, a.Project, a.Environment, a.Revision, a.Status})
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

func printApplications(apps []appSummary, jsonOut bool, w io.Writer) error {
	if jsonOut {
		return printJSON(map[string]any{"items": apps})
	}
	for _, a := range apps {
		fmt.Fprintf(w, "%s  %s  revision %d  %s\n", a.ID, a.Name, a.Revision, a.Status)
	}
	return nil
}

// bindings lists every service's native bindings, keyed by service then variable.
func bindings(a store.Application) map[string]map[string]spec.Binding {
	out := map[string]map[string]spec.Binding{}
	for name, svc := range a.Spec.Services {
		if len(svc.Bindings) > 0 {
			out[name] = svc.Bindings
		}
	}
	return out
}

// exportTOML renders the live desired specification as a hakopod.toml that
// plans with no changes. Secrets appear only as references.
func exportTOML(a store.Application) ([]byte, error) {
	encoded, err := toml.Marshal(a.Spec)
	if err != nil {
		return nil, err
	}
	if _, err := spec.Parse(encoded); err != nil {
		return nil, fmt.Errorf("exported specification does not parse back: %w", err)
	}
	header := fmt.Sprintf("# Exported from %s/%s application %s at revision %d.\n", a.Project, a.Environment, a.Name, a.Revision)
	return append([]byte(header), encoded...), nil
}

// soleService picks the service when the flag is empty and there is only one.
func soleService(a store.Application, service string) (string, error) {
	if service != "" {
		if _, ok := a.Spec.Services[service]; !ok {
			return "", &exitError{2, "unknown service " + service}
		}
		return service, nil
	}
	if len(a.Spec.Services) == 1 {
		for name := range a.Spec.Services {
			return name, nil
		}
	}
	return "", &exitError{2, "--service is required for a multi-service application"}
}

// renderManifests fetches the YAML Hakopod generates for the current revision.
// It applies nothing; an empty service renders every long-running service.
func renderManifests(ctx context.Context, c *client, a store.Application, service string) (string, error) {
	path := "/applications/" + a.ID + "/manifests"
	if service != "" {
		if _, ok := a.Spec.Services[service]; !ok {
			return "", &exitError{2, "unknown service " + service}
		}
		path += "?service=" + url.QueryEscape(service)
	}
	var out struct {
		Manifests string `json:"manifests"`
	}
	err := c.request(ctx, "GET", path, nil, "", &out)
	return out.Manifests, err
}

func restartService(ctx context.Context, c *client, a store.Application, service, idem string) (store.Deployment, error) {
	var d store.Deployment
	err := c.request(ctx, "POST", "/applications/"+a.ID+"/services/"+url.PathEscape(service)+"/restart", map[string]any{"expected_revision": a.Revision}, idem, &d)
	return d, err
}

// setImage plans the live specification with one image replaced, shows the
// reviewed changes, then deploys exactly that plan.
func setImage(ctx context.Context, c *client, cfg config, a store.Application, service, image, idem string, jsonOut bool) (store.Deployment, error) {
	if image == "" {
		return store.Deployment{}, &exitError{2, "--image is required"}
	}
	next := a.Spec
	next.Services = make(map[string]spec.Service, len(a.Spec.Services))
	for name, svc := range a.Spec.Services {
		next.Services[name] = svc
	}
	svc := next.Services[service]
	if svc.Image == image {
		return store.Deployment{}, &exitError{2, service + " already runs " + image}
	}
	svc.Image = image
	next.Services[service] = svc
	plan, err := planMergedApplication(ctx, c, cfg, next)
	if err != nil {
		return store.Deployment{}, err
	}
	if len(plan.MissingSecrets) > 0 {
		return store.Deployment{}, &exitError{2, "missing application secrets: " + strings.Join(plan.MissingSecrets, ", ")}
	}
	if !jsonOut {
		for _, change := range plan.Changes {
			fmt.Fprintf(os.Stderr, "  %s %s: %v -> %v\n", change.Service, change.Field, change.Before, change.After)
		}
	}
	return submitDeployment(ctx, c, cfg, plan, idem, jsonOut)
}

type secretItem struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func secretQuery(cfg config, application string) string {
	return url.Values{"project": {cfg.Project}, "environment": {cfg.Environment}, "application": {application}}.Encode()
}

// readSecretValue takes the value from a file or from redirected stdin, never
// from a flag or an argument, so it stays out of shell history and ps.
func readSecretValue(fromFile string, fromStdin bool, stdin io.Reader) (string, error) {
	if (fromFile == "") == !fromStdin {
		return "", &exitError{2, "give the value with exactly one of --from-file FILE or --stdin"}
	}
	var raw []byte
	var err error
	if fromStdin {
		raw, err = io.ReadAll(io.LimitReader(stdin, 64<<10+1))
	} else {
		raw, err = boundedFile(fromFile, 64<<10)
	}
	if err != nil {
		return "", err
	}
	// A trailing newline from echo or an editor is almost never part of a secret.
	raw = bytes.TrimSuffix(bytes.TrimSuffix(raw, []byte("\n")), []byte("\r"))
	if len(raw) == 0 || len(raw) > 64<<10 || bytes.IndexByte(raw, 0) >= 0 {
		return "", &exitError{2, "secret value must be 1 byte to 64 KiB without NUL"}
	}
	return string(raw), nil
}

func secretCommand(ctx context.Context, c *client, cfg config, args []string, fromFile string, fromStdin, jsonOut bool) error {
	usage := &exitError{2, "usage: hakopod secret list APP | secret set APP NAME (--from-file FILE | --stdin) | secret delete APP NAME"}
	if len(args) < 2 {
		return usage
	}
	app := args[1]
	switch {
	case args[0] == "list" && len(args) == 2:
		var out struct {
			Items []secretItem `json:"items"`
		}
		if err := c.request(ctx, "GET", "/secrets?"+secretQuery(cfg, app), nil, "", &out); err != nil {
			return err
		}
		sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
		if jsonOut {
			return printJSON(out)
		}
		for _, item := range out.Items {
			fmt.Println(item.Name)
		}
		return nil
	case args[0] == "set" && len(args) == 3:
		value, err := readSecretValue(fromFile, fromStdin, os.Stdin)
		if err != nil {
			return err
		}
		var out map[string]any
		err = c.request(ctx, "PUT", "/secrets/"+url.PathEscape(args[2])+"?"+secretQuery(cfg, app), map[string]string{"value": value}, "", &out)
		if err != nil {
			return err
		}
		if jsonOut {
			return printJSON(out)
		}
		fmt.Printf("saved %s for %s\n", args[2], app)
		if restart, _ := out["restart_required"].(bool); restart {
			fmt.Printf("running services keep the old value until restarted: hakopod restart %s --service SERVICE\n", app)
		}
		return nil
	case args[0] == "delete" && len(args) == 3:
		return c.request(ctx, "DELETE", "/secrets/"+url.PathEscape(args[2])+"?"+secretQuery(cfg, app), nil, "", nil)
	}
	return usage
}

// readyPod picks the only ready pod of a service so exec can omit --pod.
func readyPod(ctx context.Context, c *client, a store.Application, service string) (string, error) {
	var runtime struct {
		Pods []struct {
			Name  string `json:"name"`
			Ready bool   `json:"ready"`
		} `json:"pods"`
	}
	if err := c.request(ctx, "GET", "/applications/"+a.ID+"/services/"+url.PathEscape(service)+"/runtime", nil, "", &runtime); err != nil {
		return "", err
	}
	var ready []string
	for _, p := range runtime.Pods {
		if p.Ready {
			ready = append(ready, p.Name)
		}
	}
	switch len(ready) {
	case 1:
		return ready[0], nil
	case 0:
		return "", errors.New(service + " has no ready pod to run the command in")
	}
	sort.Strings(ready)
	return "", &exitError{2, service + " has " + fmt.Sprint(len(ready)) + " ready pods; choose one with --pod: " + strings.Join(ready, ", ")}
}
