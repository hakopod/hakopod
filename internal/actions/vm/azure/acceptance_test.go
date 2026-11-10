package azure

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

// TestAcceptance runs against a real subscription when
// HAKOPOD_AZURE_ACCEPTANCE=1, with Hakopod's usual Azure identity
// (AZURE_TENANT_ID, AZURE_CLIENT_ID and AZURE_CLIENT_SECRET, workload
// identity, or a managed identity): it checks the resource group and subnet,
// launches one Spot VM that powers itself off, finds it by its tags, makes
// sure another installation cannot delete it, and deletes it.
//
//	HAKOPOD_AZURE_SUBSCRIPTION, HAKOPOD_AZURE_RESOURCE_GROUP,
//	HAKOPOD_AZURE_REGION, HAKOPOD_AZURE_ZONES (comma separated, optional),
//	HAKOPOD_AZURE_SUBNET (resource ID), HAKOPOD_AZURE_VM_SIZE (default
//	Standard_B2pts_v2) and HAKOPOD_AZURE_ARCH (default arm64).
func TestAcceptance(t *testing.T) {
	if os.Getenv("HAKOPOD_AZURE_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AZURE_ACCEPTANCE=1 to run against a real subscription")
	}
	env := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var zones []string
	if z := os.Getenv("HAKOPOD_AZURE_ZONES"); z != "" {
		zones = strings.Split(z, ",")
	}
	config := Config{Installation: "acceptance-azure-" + time.Now().UTC().Format("150405"), SubscriptionID: os.Getenv("HAKOPOD_AZURE_SUBSCRIPTION"),
		ResourceGroup: os.Getenv("HAKOPOD_AZURE_RESOURCE_GROUP"), Region: os.Getenv("HAKOPOD_AZURE_REGION"), Zones: zones,
		Subnet: os.Getenv("HAKOPOD_AZURE_SUBNET"), Architecture: env("HAKOPOD_AZURE_ARCH", "arm64"), DiskGiB: 30}
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
	i, err := c.Launch(ctx, vm.LaunchInput{Name: "acceptance", InstanceTypes: []string{env("HAKOPOD_AZURE_VM_SIZE", "Standard_B2pts_v2")}, Spot: true,
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
		t.Fatal("another installation deleted the VM")
	}
	for state := listed[0].State; state != "stopped"; {
		time.Sleep(10 * time.Second)
		s, err := c.State(ctx, i.ID)
		if err != nil {
			t.Fatal(err)
		}
		state = s.State
		if s.Interrupted() {
			t.Log("evicted before it powered off")
			break
		}
	}
	t.Logf("powered itself off after %s", time.Since(start).Round(time.Second))
	if out, err := c.ConsoleOutput(ctx, i.ID); err != nil {
		t.Error("serial log:", err)
	} else {
		t.Logf("serial log: %d bytes", len(out))
	}
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
