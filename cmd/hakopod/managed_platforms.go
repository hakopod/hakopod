package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
)

type managedPlatformReviewResponse struct {
	Platform store.ManagedPlatform        `json:"platform"`
	Plan     managedplatform.Plan         `json:"plan"`
	Review   *store.ManagedPlatformReview `json:"review"`
	Blocked  bool                         `json:"blocked"`
}

func readManagedPlatformSpec(path string) (managedplatform.Spec, error) {
	var spec managedplatform.Spec
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<10 {
		return spec, &exitError{2, "managed platform TOML must be a regular file of at most 64 KiB"}
	}
	file, err := os.Open(path)
	if err != nil {
		return spec, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return spec, &exitError{2, "managed platform TOML changed while opening"}
	}
	body, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return spec, &exitError{2, "managed platform TOML could not be read"}
	}
	decoder := toml.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&spec); err != nil {
		return spec, &exitError{2, "managed platform TOML is invalid"}
	}
	if err = spec.Validate(); err != nil {
		return spec, &exitError{2, err.Error()}
	}
	return spec, nil
}

func managedPlatformCommand(ctx context.Context, c *client, project, environment string, args []string, file, idem, confirm string) error {
	if len(args) < 1 || len(args) > 2 {
		return &exitError{2, "platform requires list, show, review, apply, update, delete, operations or operation"}
	}
	action, id := args[0], ""
	if len(args) == 2 {
		id = args[1]
	}
	if action != "list" && action != "review" && action != "apply" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return &exitError{2, "provide the 32-character platform or operation ID"}
	}
	switch action {
	case "list":
		var out struct {
			Items []store.ManagedPlatform `json:"items"`
		}
		if (project == "") != (environment == "") {
			return &exitError{2, "provide both a project and environment, or omit both"}
		}
		query := url.Values{}
		if project != "" {
			query.Set("project", project)
			query.Set("environment", environment)
		}
		if err := c.request(ctx, "GET", "/managed-platforms?"+query.Encode(), nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "show":
		var out store.ManagedPlatform
		if err := c.request(ctx, "GET", "/managed-platforms/"+url.PathEscape(id), nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "operations":
		var out struct {
			Items []store.ManagedPlatformOperation `json:"items"`
		}
		if err := c.request(ctx, "GET", "/managed-platforms/"+url.PathEscape(id)+"/operations", nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "operation":
		var out store.ManagedPlatformOperation
		if err := c.request(ctx, "GET", "/managed-platform-operations/"+url.PathEscape(id), nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "review", "apply":
		spec, err := readManagedPlatformSpec(file)
		if err != nil {
			return err
		}
		intent := map[string]any{"project": project, "environment": environment, "expected_revision": int64(0), "kind": "create", "spec": spec}
		var reviewed managedPlatformReviewResponse
		if err = c.request(ctx, "POST", "/managed-platforms/reviews", intent, "", &reviewed); err != nil {
			return err
		}
		if action == "review" {
			return printJSON(reviewed)
		}
		if reviewed.Blocked || reviewed.Review == nil {
			return &exitError{1, "managed platform creation is unavailable: " + reviewed.Plan.Capability.Reason}
		}
		intent["id"] = reviewed.Platform.ID
		intent["review"] = reviewed.Review
		if idem == "" {
			idem = "platform-create-" + reviewed.Platform.ID
		}
		var out store.ManagedPlatformOperation
		if err := c.request(ctx, "POST", "/managed-platforms/operations", intent, idem, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "update":
		spec, err := readManagedPlatformSpec(file)
		if err != nil {
			return err
		}
		var current store.ManagedPlatform
		if err = c.request(ctx, "GET", "/managed-platforms/"+url.PathEscape(id), nil, "", &current); err != nil {
			return err
		}
		intent := map[string]any{"id": id, "project": current.Project, "environment": current.Environment, "expected_revision": current.Revision, "kind": "update", "confirm_name": current.Spec.Name, "spec": spec}
		var reviewed managedPlatformReviewResponse
		if err = c.request(ctx, "POST", "/managed-platforms/reviews", intent, "", &reviewed); err != nil {
			return err
		}
		if reviewed.Blocked || reviewed.Review == nil {
			return printJSON(reviewed)
		}
		intent["review"] = reviewed.Review
		if idem == "" {
			idem = fmt.Sprintf("platform-update-%s-r%d", id, current.Revision+1)
		}
		var out store.ManagedPlatformOperation
		if err := c.request(ctx, "POST", "/managed-platforms/operations", intent, idem, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "delete":
		if confirm == "" {
			return &exitError{2, "platform delete requires --name with the current platform name"}
		}
		var current store.ManagedPlatform
		if err := c.request(ctx, "GET", "/managed-platforms/"+url.PathEscape(id), nil, "", &current); err != nil {
			return err
		}
		if current.Spec.Name != confirm {
			return &exitError{2, "--name must exactly match the current platform name"}
		}
		intent := map[string]any{"id": current.ID, "project": current.Project, "environment": current.Environment, "expected_revision": current.Revision, "kind": "delete", "confirm_name": confirm, "spec": current.Spec}
		var reviewed managedPlatformReviewResponse
		if err := c.request(ctx, "POST", "/managed-platforms/reviews", intent, "", &reviewed); err != nil {
			return err
		}
		if reviewed.Blocked || reviewed.Review == nil {
			return &exitError{1, "delete review was blocked"}
		}
		intent["review"] = reviewed.Review
		if idem == "" {
			idem = "platform-delete-" + current.ID
		}
		var out store.ManagedPlatformOperation
		if err := c.request(ctx, "POST", "/managed-platforms/operations", intent, idem, &out); err != nil {
			return err
		}
		return printJSON(out)
	default:
		return &exitError{2, "platform requires list, show, review, apply, update, delete, operations or operation"}
	}
}
