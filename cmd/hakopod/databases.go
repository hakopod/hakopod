package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

type databaseConnectionFlags struct {
	ApplicationID, Service, Variable, Endpoint, JobID, HistoryRange string
	TargetMember, OperationID                                       string
	PublicEndpointID, PublicEndpointPurpose, PublicEndpointCIDRs    string
	PublicEndpointRevision, PublicEndpointMaxConnections            int64
	ClusterAware, Inspected                                         bool
	Import                                                          databaseImportFlags
}

func databaseCommand(ctx context.Context, c *client, project, environment string, args []string, file, idem, review, artifact, confirmation string, revision int64, connection databaseConnectionFlags) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("database requires list, nodes, show, create, resize-plan, resize, resize-retry-plan, resize-retry, switchover-plan, switchover, switchover-retry, operation, public-endpoint-capabilities, public-endpoint-list, public-endpoint-plan, public-endpoint-publish, public-endpoint-revoke, public-endpoint-operation, delete, credentials, trust, metrics, connections, restore-plan, restore, connection-plan, connect or inspect, followed by an ID where needed")
	}
	action := args[0]
	if connection.TargetMember != "" && action != "switchover-plan" {
		return fmt.Errorf("--target-member applies only to switchover-plan; switchover and retries use the approved target")
	}
	if connection.OperationID != "" && action != "switchover-retry" && action != "resize-retry-plan" && action != "resize-retry" {
		return fmt.Errorf("--operation-id applies only to database retry commands")
	}
	if action == "import-plan" || action == "import" {
		return databaseImportCommand(ctx, c, args, file, idem, confirmation, connection.Import)
	}
	id := ""
	if len(args) == 2 {
		id = args[1]
	}
	if action != "list" && action != "nodes" && action != "create" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return fmt.Errorf("provide the managed database ID")
	}
	method, path := "GET", "/databases"
	var body any
	switch action {
	case "nodes":
		if id != "" || project == "" || environment == "" {
			return fmt.Errorf("database nodes requires project and environment, without a database ID")
		}
		path = "/database-placement/nodes?" + url.Values{"project": {project}, "environment": {environment}}.Encode()
	case "list":
		if project == "" || environment == "" {
			return fmt.Errorf("database list requires project and environment")
		}
		path += "?" + url.Values{"project": {project}, "environment": {environment}}.Encode()
	case "show":
		path += "/" + id
	case "operation":
		path = "/database-operations/" + id
	case "public-endpoint-operation":
		path = "/database-public-endpoint-operations/" + id
	case "public-endpoint-capabilities":
		path += "/" + id + "/public-endpoint-capabilities"
	case "public-endpoint-list":
		path += "/" + id + "/public-endpoints"
	case "public-endpoint-plan":
		if connection.PublicEndpointPurpose == "" || connection.PublicEndpointCIDRs == "" || connection.PublicEndpointMaxConnections < 1 {
			return fmt.Errorf("public-endpoint-plan requires --purpose, --source-cidrs and --max-connections")
		}
		cidrs := strings.Split(connection.PublicEndpointCIDRs, ",")
		for _, cidr := range cidrs {
			if cidr == "" || strings.TrimSpace(cidr) != cidr {
				return fmt.Errorf("--source-cidrs must be a comma-separated list without empty values or spaces")
			}
		}
		method, path, body = "POST", path+"/"+id+"/public-endpoint-plan", map[string]any{"purpose": connection.PublicEndpointPurpose, "source_cidrs": cidrs, "max_connections": connection.PublicEndpointMaxConnections}
	case "public-endpoint-publish":
		if review == "" || revision < 1 || connection.PublicEndpointRevision < 0 {
			return fmt.Errorf("public-endpoint-publish requires --review-id, --revision and --endpoint-revision from the reviewed plan; existing connections will close")
		}
		method, path, body = "POST", path+"/"+id+"/public-endpoints", map[string]any{"review_id": review, "expected_database_revision": revision, "expected_endpoint_revision": connection.PublicEndpointRevision}
	case "public-endpoint-revoke":
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(connection.PublicEndpointID) || connection.PublicEndpointRevision < 1 {
			return fmt.Errorf("public-endpoint-revoke requires --public-endpoint-id and --endpoint-revision")
		}
		method, path, body = "DELETE", path+"/"+id+"/public-endpoints/"+connection.PublicEndpointID, map[string]any{"expected_endpoint_revision": connection.PublicEndpointRevision}
	case "resize-retry-plan", "resize-retry":
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(connection.OperationID) || revision < 1 || file != "" {
			return fmt.Errorf("replica retries require --operation-id and --revision; the requested layout cannot change")
		}
		method, path, body = "POST", path+"/"+id+"/"+action, map[string]any{"operation_id": connection.OperationID, "expected_revision": revision}
		if action == "resize-retry" {
			if review == "" || confirmation == "" || len(idem) < 8 || len(idem) > 128 {
				return fmt.Errorf("resize-retry requires --review-id, --name and an 8–128 character --idempotency-key")
			}
			body = map[string]any{"operation_id": connection.OperationID, "expected_revision": revision, "review_id": review, "confirm_name": confirmation}
		}
	case "switchover-plan":
		if connection.TargetMember == "" || len(connection.TargetMember) > 253 {
			return fmt.Errorf("switchover-plan requires --target-member naming a physical standby member")
		}
		method, path, body = "POST", path+"/"+id+"/switchover-plan", map[string]any{"target_member": connection.TargetMember}
	case "switchover":
		if review == "" || revision < 1 || confirmation == "" {
			return fmt.Errorf("switchover requires --review-id, --revision and --name matching the reviewed database; existing connections will close")
		}
		method, path, body = "POST", path+"/"+id+"/switchover", map[string]any{"review_id": review, "expected_revision": revision, "confirm_name": confirmation}
	case "switchover-retry":
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(connection.OperationID) || revision < 1 || confirmation == "" {
			return fmt.Errorf("switchover-retry requires --operation-id, --revision and --name matching the database; retry resumes the same approved target")
		}
		method, path, body = "POST", path+"/"+id+"/switchover-retry", map[string]any{"operation_id": connection.OperationID, "expected_revision": revision, "confirm_name": confirmation}
	case "trust", "connections":
		path += "/" + id + "/" + action
	case "metrics":
		window := connection.HistoryRange
		if window == "" {
			window = "1h"
		}
		if window != "1h" && window != "6h" && window != "24h" {
			return fmt.Errorf("database metrics range must be 1h, 6h or 24h")
		}
		path += "/" + id + "/metrics?" + url.Values{"range": {window}}.Encode()
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
