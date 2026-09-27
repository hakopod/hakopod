package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

type databaseConnectionFlags struct {
	ApplicationID, Service, Variable, Endpoint, JobID string
	ClusterAware, Inspected                           bool
	Import                                            databaseImportFlags
}

func databaseCommand(ctx context.Context, c *client, project, environment string, args []string, file, idem, review, artifact, confirmation string, revision int64, connection databaseConnectionFlags) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("database requires list, show, create, resize-plan, resize, delete, credentials, restore-plan restore, connection-plan, connect or inspect, followed by a database ID where needed")
	}
	action := args[0]
	if action == "import-plan" || action == "import" {
		return databaseImportCommand(ctx, c, args, file, idem, confirmation, connection.Import)
	}
	id := ""
	if len(args) == 2 {
		id = args[1]
	}
	if action != "list" && action != "create" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return fmt.Errorf("provide the managed database ID")
	}
	method, path := "GET", "/databases"
	var body any
	switch action {
	case "list":
		if project == "" || environment == "" {
			return fmt.Errorf("database list requires project and environment")
		}
		path += "?" + url.Values{"project": {project}, "environment": {environment}}.Encode()
	case "show":
		path += "/" + id
	case "create", "resize-plan", "resize":
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		if err != nil {
			return err
		}
		spec, err := database.Parse(data)
		if err != nil {
			return err
		}
		method = "POST"
		body = map[string]any{"spec": spec}
		if action == "create" {
			if project == "" || environment == "" {
				return fmt.Errorf("database create requires project and environment")
			}
			body = map[string]any{"project": project, "environment": environment, "spec": spec}
		} else {
			path += "/" + id + "/" + action
			if action == "resize" {
				if review == "" || revision < 1 {
					return fmt.Errorf("resize requires --review-id and --revision from the reviewed plan")
				}
				body = map[string]any{"spec": spec, "review_id": review, "expected_revision": revision}
			}
		}
	case "connection-plan":
		if connection.ApplicationID == "" || connection.Service == "" || connection.Variable == "" {
			return fmt.Errorf("connection-plan requires --application-id, --service and --variable")
		}
		method, path, body = "POST", path+"/"+id+"/connection-plan", map[string]any{"application_id": connection.ApplicationID, "service": connection.Service, "variable": connection.Variable, "endpoint": connection.Endpoint, "cluster_aware": connection.ClusterAware}
	case "connect":
		if review == "" || confirmation == "" {
			return fmt.Errorf("connect requires --review-id and --name matching the reviewed application")
		}
		method, path, body = "POST", path+"/"+id+"/connect", map[string]any{"review_id": review, "confirm_application": confirmation}
	case "inspect":
		if connection.JobID == "" || confirmation == "" || revision < 1 || !connection.Inspected {
			return fmt.Errorf("inspect requires --job-id, --name, --revision and --inspected after checking the recovered data")
		}
		method, path, body = "POST", path+"/"+id+"/inspect", map[string]any{"job_id": connection.JobID, "confirm_name": confirmation, "expected_revision": revision, "inspected": connection.Inspected}
	case "delete":
		if confirmation == "" || revision < 1 {
			return fmt.Errorf("delete requires --name matching the database and --revision")
		}
		method, path, body = "DELETE", path+"/"+id, map[string]any{"confirm_name": confirmation, "expected_revision": revision}
	case "credentials":
		method, path, body = "POST", path+"/"+id+"/credentials", map[string]any{}
	case "restore-plan":
		if artifact == "" {
			return fmt.Errorf("restore-plan requires --artifact-id")
		}
		method, path, body = "POST", path+"/"+id+"/restore-plan", map[string]any{"artifact_id": artifact}
	case "restore":
		if artifact == "" || review == "" || confirmation == "" {
			return fmt.Errorf("restore requires --artifact-id, --review-id and --name from the recovery review")
		}
		method, path, body = "POST", "/backup-artifacts/"+url.PathEscape(artifact)+"/restore", map[string]any{"plan_id": review, "confirmation": confirmation}
	default:
		return fmt.Errorf("unknown database action")
	}
	if idem == "" {
		idem = store.NewID()
	}
	var out any
	if err := c.request(ctx, method, path, body, idem, &out); err != nil {
		return err
	}
	return printJSON(out)
}
