package aws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

// TestAcceptanceSpotInstance launches one t4g.nano spot instance in a real
// account, which powers itself off at boot. actions-vm-acceptance.yml runs it
// with GitHub OIDC credentials; see "Acceptance in a real account" in
// docs/managed-actions.md.
func TestAcceptanceSpotInstance(t *testing.T) {
	if os.Getenv("HAKOPOD_AWS_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AWS_ACCEPTANCE=1 to launch an instance in a real AWS account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	c, err := New(ctx, Config{
		Installation:     "acceptance-" + hex.EncodeToString(suffix),
		Region:           os.Getenv("AWS_REGION"),
		RoleARN:          os.Getenv("HAKOPOD_ACTIONS_VM_ROLE_ARN"),
		ExternalID:       os.Getenv("HAKOPOD_ACTIONS_VM_EXTERNAL_ID"),
		SubnetIDs:        []string{os.Getenv("HAKOPOD_ACTIONS_VM_SUBNET")},
		SecurityGroupIDs: []string{os.Getenv("HAKOPOD_ACTIONS_VM_SECURITY_GROUP")},
		Architecture:     "arm64",
		DiskGiB:          8,
	})
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := c.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("check warnings:", warnings)

	launched, err := c.Launch(ctx, vm.LaunchInput{Name: "acceptance", Tags: map[string]string{vm.TagSlot: "acceptance"},
		InstanceTypes: []string{"t4g.nano", "t4g.micro"}, Spot: true, UserData: "#!/bin/sh\npoweroff\n"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background(), launched.ID); err != nil {
			t.Errorf("cleanup: %v; terminate %s by hand", err, launched.ID)
		}
	})
	t.Logf("launched %s (%s, %s)", launched.ID, launched.Type, launched.Lifecycle)

	// DescribeInstances is eventually consistent right after a launch.
	var seen vm.Instance
	for seen.ID == "" {
		seen, err = c.State(ctx, launched.ID)
		if err != nil && !errors.Is(err, vm.ErrNotFound) {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal("the instance never appeared")
		}
		time.Sleep(3 * time.Second)
	}
	if seen.Tags[vm.TagInstallation] != c.config.Installation || seen.Lifecycle != "spot" || seen.LaunchedAt.IsZero() {
		t.Fatalf("observed %+v", seen)
	}
	listed, err := c.List(ctx, map[string]string{vm.TagSlot: "acceptance"})
	if err != nil || !slices.ContainsFunc(listed, func(i vm.Instance) bool { return i.ID == launched.ID }) {
		t.Fatal("not listed by its tags", listed, err)
	}

	other := *c
	other.config.Installation = "someone-else"
	if err := other.Terminate(ctx, launched.ID); err == nil {
		t.Fatal("terminated an instance tagged for another installation")
	}

	if err := c.Terminate(ctx, launched.ID); err != nil {
		t.Fatal(err)
	}
	for !seen.Gone() {
		if ctx.Err() != nil {
			t.Fatalf("still %s", seen.State)
		}
		time.Sleep(3 * time.Second)
		if seen, err = c.State(ctx, launched.ID); errors.Is(err, vm.ErrNotFound) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%s is %s (%s)", launched.ID, seen.State, seen.StateReason)
}
