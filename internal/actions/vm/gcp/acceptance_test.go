package gcp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

// TestAcceptance runs against a real project when HAKOPOD_GCP_ACCEPTANCE=1:
// it impersonates the capacity's service account, checks the subnetwork,
// launches one Spot VM that powers itself off, finds it by its tags, makes
// sure another installation cannot delete it, and deletes it.
//
//	HAKOPOD_GCP_PROJECT, HAKOPOD_GCP_REGION, HAKOPOD_GCP_ZONES (comma
//	separated), HAKOPOD_GCP_SUBNETWORK, HAKOPOD_GCP_SERVICE_ACCOUNT,
//	HAKOPOD_GCP_MACHINE_TYPE (default t2a-standard-1) and
//	HAKOPOD_GCP_ARCH (default arm64).
func TestAcceptance(t *testing.T) {
	if os.Getenv("HAKOPOD_GCP_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_GCP_ACCEPTANCE=1 to run against a real project")
	}
	env := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	config := Config{Installation: "acceptance-gcp-" + time.Now().UTC().Format("150405"), Project: os.Getenv("HAKOPOD_GCP_PROJECT"),
		Region: os.Getenv("HAKOPOD_GCP_REGION"), Zones: strings.Split(os.Getenv("HAKOPOD_GCP_ZONES"), ","),
		Subnetwork: os.Getenv("HAKOPOD_GCP_SUBNETWORK"), ServiceAccount: os.Getenv("HAKOPOD_GCP_SERVICE_ACCOUNT"),
		Architecture: env("HAKOPOD_GCP_ARCH", "arm64"), DiskGiB: 20}
	c, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := c.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("check warnings:", warnings)
	start := time.Now()
	i, err := c.Launch(ctx, vm.LaunchInput{Name: "acceptance", InstanceTypes: []string{env("HAKOPOD_GCP_MACHINE_TYPE", "t2a-standard-1")}, Spot: true,
		UserData: "#!/bin/bash\nsleep 60\nshutdown -h now\n", Tags: map[string]string{vm.TagCapacity: "acceptance", vm.TagSlot: "acceptance", vm.TagPool: "app/svc"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("launched %s (%s, %s) in %s", i.ID, i.Type, i.Lifecycle, time.Since(start).Round(time.Second))
	defer func() {
		if err := c.Terminate(context.WithoutCancel(ctx), i.ID); err != nil {
			t.Error("delete:", err)
		}
	}()
	listed, err := c.List(ctx, map[string]string{vm.TagSlot: "acceptance", vm.TagPool: "app/svc"})
	if err != nil || len(listed) != 1 || listed[0].ID != i.ID || listed[0].Lifecycle != "spot" {
		t.Fatal("listed", listed, err)
	}
	other := *c
	other.config.Installation = "acceptance-someone-else"
	if err = other.Terminate(ctx, i.ID); err == nil {
		t.Fatal("another installation deleted the instance")
	}
	for state := listed[0].State; state != "stopped"; {
		time.Sleep(10 * time.Second)
		s, err := c.State(ctx, i.ID)
		if err != nil {
			t.Fatal(err)
		}
		state = s.State
		if s.Interrupted() {
			t.Log("preempted before it powered off")
			break
		}
	}
	t.Logf("powered itself off after %s", time.Since(start).Round(time.Second))
	if err = c.Terminate(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err = c.State(ctx, i.ID); errors.Is(err, vm.ErrNotFound) {
			break
		}
		time.Sleep(5 * time.Second)
	}
}
