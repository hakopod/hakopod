package main

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hakopod/hakopod/internal/store"
)

type databaseWorkflowFlags struct{ ApplicationID, Service, Variable, Endpoint, Role, Database, SecretReference, Profile, ConfirmDatabase string }

// databaseWorkflowCommand is kept separate from databaseCommand so the main
// flag switch only needs a small dispatch hook during integration.
func databaseWorkflowCommand(ctx context.Context, c *client, action, id, idem, review, confirmation string, flags databaseWorkflowFlags) error {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return fmt.Errorf("provide the managed database ID")
	}
	var path string
	var body any
	switch action {
	case "application-provisioning-plan":
		if flags.ApplicationID == "" || flags.Service == "" || flags.Variable == "" {
			return fmt.Errorf("application-provisioning-plan requires --application-id, --service and --variable")
		}
		path = "/databases/" + id + "/application-provisioning-plan"
		body = map[string]any{"application_id": flags.ApplicationID, "service": flags.Service, "variable": flags.Variable, "endpoint": flags.Endpoint, "role": flags.Role, "database": flags.Database, "secret_reference": flags.SecretReference}
	case "application-provision":
		if review == "" || confirmation == "" {
			return fmt.Errorf("application-provision requires --review-id and --name matching the reviewed application")
		}
		path = "/databases/" + id + "/application-provision"
		body = map[string]any{"review_id": review, "confirm_application": confirmation}
	case "migration-lock-recovery-plan":
		if flags.ApplicationID == "" || flags.Service == "" || flags.Variable == "" || flags.Profile == "" {
			return fmt.Errorf("migration-lock-recovery-plan requires --application-id, --service, --variable and --profile")
		}
		path = "/databases/" + id + "/migration-lock-recovery-plan"
		body = map[string]any{"application_id": flags.ApplicationID, "service": flags.Service, "variable": flags.Variable, "profile": flags.Profile}
	case "migration-lock-recover":
		if review == "" || confirmation == "" || flags.ConfirmDatabase == "" {
			return fmt.Errorf("migration-lock-recover requires --review-id, --name and --confirm-database")
		}
		path = "/databases/" + id + "/migration-lock-recover"
		body = map[string]any{"review_id": review, "confirm_application": confirmation, "confirm_database": flags.ConfirmDatabase}
	default:
		return fmt.Errorf("unknown database workflow action")
	}
	if idem == "" {
		idem = store.NewID()
	}
	var out any
	if err := c.request(ctx, "POST", path, body, idem, &out); err != nil {
		return err
	}
	return printJSON(out)
}
