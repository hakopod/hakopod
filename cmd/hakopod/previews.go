package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func previewCommand(ctx context.Context, c *client, cfg config, command, arg, name, branch, file, idem string, ttl time.Duration, discard bool) error {
	if command == "preview-delete" {
		if !agentID.MatchString(arg) || name == "" {
			return errors.New("preview-delete requires preview ID and --name for confirmation")
		}
		var out any
		if err := c.request(ctx, "DELETE", "/previews/"+url.PathEscape(arg), map[string]string{"confirmation": name}, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	parent, err := findApp(ctx, c, cfg, arg, file)
	if err != nil {
		return err
	}
	if command == "previews" {
		var out any
		if err = c.request(ctx, "GET", "/applications/"+parent.ID+"/previews", nil, "", &out); err != nil {
			return err
		}
		return printJSON(out)
	}
	if !discard {
		return errors.New("preview creation requires --acknowledge-data-expiry; containers, volumes and native secrets will be deleted at expiry")
	}
	if ttl < time.Hour || ttl > 72*time.Hour || ttl%time.Hour != 0 {
		return errors.New("preview --ttl must be a whole number of hours between 1h and 72h")
	}
	body, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if _, err = spec.Parse(body); err != nil {
		return err
	}
	if idem == "" {
		idem = store.NewID()
	}
	input := map[string]any{"name": name, "branch": branch, "ttl_hours": int(ttl / time.Hour), "expected_parent_revision": parent.Revision, "toml": string(body), "discard_on_expiry": true}
	var out json.RawMessage
	if err = c.request(ctx, "POST", "/applications/"+parent.ID+"/previews", input, idem, &out); err != nil {
		return err
	}
	return printJSON(out)
}
