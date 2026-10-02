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
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/pelletier/go-toml/v2"
)

type managedPlatformReviewResponse struct {
	Platform store.ManagedPlatform        `json:"platform"`
	Plan     managedplatform.Plan         `json:"plan"`
	Review   *store.ManagedPlatformReview `json:"review"`
	Blocked  bool                         `json:"blocked"`
}

type managedPlatformRecoveryRequest struct {
	SchemaVersion          int                   `toml:"schema_version" json:"-"`
	Kind                   string                `toml:"kind" json:"kind"`
	Project                string                `toml:"project" json:"project"`
	Environment            string                `toml:"environment" json:"environment"`
	SourcePlatformID       string                `toml:"source_platform_id" json:"source_platform_id"`
	TargetPlatformID       string                `toml:"target_platform_id" json:"target_platform_id,omitempty"`
	ArtifactID             string                `toml:"artifact_id" json:"artifact_id,omitempty"`
	DestinationID          string                `toml:"destination_id" json:"destination_id,omitempty"`
	DestinationRevision    int64                 `toml:"destination_revision" json:"destination_revision,omitempty"`
	ExpectedSourceRevision int64                 `toml:"expected_source_revision" json:"expected_source_revision"`
	ExpectedTargetRevision int64                 `toml:"expected_target_revision" json:"expected_target_revision,omitempty"`
	Review                 platformbackup.Review `toml:"-" json:"review"`
	ConfirmTargetName      string                `toml:"confirm_target_name" json:"confirm_target_name"`
}

func (r managedPlatformRecoveryRequest) intent() platformbackup.Intent {
	return platformbackup.Intent{Kind: r.Kind, Project: r.Project, Environment: r.Environment, SourcePlatformID: r.SourcePlatformID, TargetPlatformID: r.TargetPlatformID, ArtifactID: r.ArtifactID, DestinationID: r.DestinationID, DestinationRevision: r.DestinationRevision, ExpectedSourceRevision: r.ExpectedSourceRevision, ExpectedTargetRevision: r.ExpectedTargetRevision}
}

func readManagedPlatformRecovery(path string) (managedPlatformRecoveryRequest, error) {
	var request managedPlatformRecoveryRequest
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<10 {
		return request, &exitError{2, "managed platform recovery TOML must be a regular file of at most 64 KiB"}
	}
	file, err := os.Open(path)
	if err != nil {
		return request, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return request, &exitError{2, "managed platform recovery TOML changed while opening"}
	}
	body, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return request, &exitError{2, "managed platform recovery TOML could not be read"}
	}
	decoder := toml.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil || request.SchemaVersion != 1 || request.intent().Validate() != nil {
		return request, &exitError{2, "managed platform recovery TOML is invalid"}
	}
	if request.Kind == "restore" && request.ConfirmTargetName == "" {
		return request, &exitError{2, "managed platform restore requires confirm_target_name"}
	}
	return request, nil
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
		return &exitError{2, "platform requires list, catalog, show, review, apply, update, delete, operations, operation, recovery-review, recovery-apply, recovery-operations, recovery-operation or recovery-cancel"}
	}
	action, id := args[0], ""
	if len(args) == 2 {
		id = args[1]
	}
	if action != "list" && action != "catalog" && action != "review" && action != "apply" && action != "recovery-review" && action != "recovery-apply" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return &exitError{2, "provide the 32-character platform or operation ID"}
	}
	switch action {
	case "catalog":
		if id != "" || project == "" || environment == "" {
			return &exitError{2, "platform catalog requires a project and environment, without a resource ID"}
		}
		var out struct {
			Project          string                            `json:"project"`
			Environment      string                            `json:"environment"`
			StorageClass     string                            `json:"storage_class"`
			Nodes            []managedplatform.CapacityNode    `json:"nodes"`
			SecretReferences []managedplatform.SecretReference `json:"secret_references"`
			Items            []managedplatform.CatalogEntry    `json:"items"`
		}
		query := url.Values{"project": {project}, "environment": {environment}}
		if err := c.request(ctx, "GET", "/managed-platforms/catalog?"+query.Encode(), nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
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
	case "recovery-operations":
		var out struct {
			Items []platformbackup.Operation `json:"items"`
		}
		if err := c.request(ctx, "GET", "/managed-platforms/"+url.PathEscape(id)+"/recovery-operations", nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "recovery-operation":
		var out platformbackup.Operation
		if err := c.request(ctx, "GET", "/managed-platform-recovery-operations/"+url.PathEscape(id), nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	case "recovery-cancel":
		var out map[string]bool
		if err := c.request(ctx, "POST", "/managed-platform-recovery-operations/"+url.PathEscape(id)+"/cancel", map[string]any{}, idem, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "recovery-review", "recovery-apply":
		if id != "" {
			return &exitError{2, "managed platform recovery review does not accept a resource ID"}
		}
		request, err := readManagedPlatformRecovery(file)
		if err != nil {
			return err
		}
		if project != "" && request.Project != project || environment != "" && request.Environment != environment {
			return &exitError{2, "managed platform recovery TOML scope differs from the selected project or environment"}
		}
		var reviewed platformbackup.Review
		if err = c.request(ctx, "POST", "/managed-platform-recovery/reviews", request, "", &reviewed); err != nil {
			return err
		}
		if action == "recovery-review" {
			return printJSON(reviewed)
		}
		request.Review = reviewed
		if idem == "" {
			idem = "platform-recovery-" + reviewed.ID
		}
		var out platformbackup.Operation
		if err = c.request(ctx, "POST", "/managed-platform-recovery/operations", request, idem, &out); err != nil {
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
		return &exitError{2, "platform requires list, catalog, show, review, apply, update, delete, operations, operation, recovery-review, recovery-apply, recovery-operations, recovery-operation or recovery-cancel"}
	}
}
