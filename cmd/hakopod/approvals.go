package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hakopod/hakopod/internal/operations"
	"golang.org/x/term"
)

// approvalRequiredError is an approval_required refusal; it exits like any other 403.
type approvalRequiredError struct {
	ID        string
	ExpiresAt time.Time
	Message   string
}

func (e *approvalRequiredError) Error() string { return e.Message }
func (e *approvalRequiredError) Unwrap() error { return &exitError{3, e.Message} }

// Swapped by tests.
var (
	approvalInteractive = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	}
	approvalPoll = 5 * time.Second
	approvalNow  = time.Now
)

func (c *client) waitApproval(ctx context.Context, a *approvalRequiredError) error {
	deadline := a.ExpiresAt.Add(30 * time.Second)
	if a.ExpiresAt.IsZero() {
		deadline = approvalNow().Add(15 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	until := "unknown"
	if !a.ExpiresAt.IsZero() {
		until = a.ExpiresAt.Local().Format("15:04")
	}
	fmt.Fprintf(os.Stderr, "Waiting for approval %s (expires %s). Approve it in Hakopod → Approvals.\n", a.ID, until)
	tick := time.NewTicker(approvalPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return &exitError{3, "Approval " + a.ID + " expired."}
			}
			return errors.New("stopped waiting for approval " + a.ID)
		case <-tick.C:
		}
		var s struct{ State string }
		if err := c.send(ctx, "GET", "/agent-approvals/"+a.ID, nil, false, "", "", &s); err != nil {
			return err
		}
		switch s.State {
		case "approved":
			return nil
		case "denied", "expired":
			return &exitError{3, "Approval " + a.ID + " was " + s.State + "."}
		case "consumed":
			return &exitError{3, "Approval " + a.ID + " was already used."}
		}
	}
}

func approvalCommand(ctx context.Context, c *client, id string, jsonOut bool) error {
	if !operations.ValidApprovalID(id) {
		return &exitError{2, "usage: hakopod approval ID [--json]"}
	}
	var raw map[string]any
	if err := c.request(ctx, "GET", "/agent-approvals/"+id, nil, "", &raw); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(raw)
	}
	field := func(k string) string { v, _ := raw[k].(string); return v }
	scope := field("project") + "/" + field("environment")
	if scope == "/" {
		scope = "installation"
	}
	fmt.Printf("Approval:  %s\nState:     %s\nPolicy:    %s\nOperation: %s\nScope:     %s\nExpires:   %s\n", id, field("state"), field("policy_name"), field("operation"), scope, field("expires_at"))
	return nil
}
