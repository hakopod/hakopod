package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hakopod/hakopod/internal/store"
)

func testBindingConnection(ctx context.Context, c *client, application store.Application, service, variable, pod string) error {
	svc, exists := application.Spec.Services[service]
	if _, bound := svc.Bindings[variable]; !exists || !bound {
		return fmt.Errorf("test-connection requires --service and --variable naming a saved database binding")
	}
	path := "/applications/" + url.PathEscape(application.ID) + "/services/" + url.PathEscape(service) + "/bindings/" + url.PathEscape(variable) + "/test"
	var result map[string]any
	if err := c.request(ctx, "POST", path, map[string]any{"expected_revision": application.Revision, "pod": pod}, "", &result); err != nil {
		return err
	}
	if err := printJSON(result); err != nil {
		return err
	}
	if result["outcome"] != "passed" {
		return &exitError{1, "The binding connection was not verified. Inspect the reported stages."}
	}
	return nil
}
