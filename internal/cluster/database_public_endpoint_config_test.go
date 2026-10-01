package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestDatabasePublicEndpointOptionsRequireSelfHostedProvisionedPool(t *testing.T) {
	valid := Options{DeploymentMode: DeploymentSelfHosted, PublicTCPPorts: []int32{15432, 15433}, DatabasePublicAddress: "192.0.2.10", DatabasePublicDomain: "database.example.test", DatabasePublicPorts: []int32{15432}}
	if err := ValidateDatabasePublicEndpointOptions(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Options){
		func(o *Options) { o.DatabasePublicDomain = "" },
		func(o *Options) { o.DeploymentMode = DeploymentManagedCloud },
		func(o *Options) { o.DatabasePublicPorts = []int32{15434} },
		func(o *Options) { o.DatabasePublicAddress = "not-an-address" },
	} {
		candidate := valid
		mutate(&candidate)
		if err := ValidateDatabasePublicEndpointOptions(candidate); err == nil {
			t.Fatal("invalid database public endpoint configuration accepted")
		}
	}
}

func TestDatabasePublicEndpointAllocationsAreOperatorOwnedAndStable(t *testing.T) {
	c := &Client{options: Options{DatabasePublicAddress: "192.0.2.10", DatabasePublicDomain: "database.example.test", DatabasePublicPorts: []int32{15432, 15433}}}
	first := c.DatabasePublicEndpointAllocations()
	second := c.DatabasePublicEndpointAllocations()
	if len(first) != 2 || first[0] != second[0] || first[0].Host != "database-15432.database.example.test" || first[0].Address != "192.0.2.10" || first[0].ID == "" {
		t.Fatalf("unexpected allocations: %#v", first)
	}
}

func TestDatabasePublicEndpointPublicationRejectsWithdrawnConfiguration(t *testing.T) {
	d := database.Resource{Spec: database.Spec{Engine: "postgresql", TLS: &database.TLSConfig{Mode: "required"}}}
	options := Options{DeploymentMode: DeploymentSelfHosted, PublicTCPPorts: []int32{15432}, DatabasePublicAddress: "192.0.2.10", DatabasePublicDomain: "database.example.test", DatabasePublicPorts: []int32{15432}}
	valid := &Client{options: options}
	endpoint := database.PublicEndpoint{Spec: database.PublicEndpointSpec{Purpose: "read_write"}, Allocation: valid.DatabasePublicEndpointAllocations()[0]}
	for name, mutate := range map[string]func(*Options){
		"removed allocation": func(o *Options) {
			o.DatabasePublicPorts = nil
			o.DatabasePublicAddress = ""
			o.DatabasePublicDomain = ""
		},
		"changed address":     func(o *Options) { o.DatabasePublicAddress = "192.0.2.11" },
		"changed domain":      func(o *Options) { o.DatabasePublicDomain = "replacement.example.test" },
		"unavailable edition": func(o *Options) { o.DeploymentMode = DeploymentManagedCloud },
	} {
		t.Run(name, func(t *testing.T) {
			changed := options
			mutate(&changed)
			c := &Client{options: changed}
			want := "configured operator inventory"
			if name == "unavailable edition" {
				want = "managed Cloud database public endpoints have not passed release qualification"
			}
			if err := c.ValidateDatabasePublicEndpoint(context.Background(), d, endpoint); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("withdrawn configuration reached DNS or runtime publication", err)
			}
		})
	}
}

func TestManagedDatabasePublicEndpointQualificationCannotBeEnabledByInventory(t *testing.T) {
	spec := database.Spec{Engine: "postgresql", TLS: &database.TLSConfig{Mode: "required"}}
	options := Options{DeploymentMode: DeploymentManagedCloud, ManagedDatabasePublicEndpoints: true, DatabasePublicAddress: "192.0.2.10", DatabasePublicDomain: "database.example.test", DatabasePublicPorts: []int32{15432}}
	if err := ValidateDatabasePublicEndpointOptions(options); err != nil {
		t.Fatal("trusted managed inventory was rejected before source qualification", err)
	}
	client := &Client{options: options}
	capabilities := client.DatabasePublicEndpointCapabilities(spec)
	if capabilities.Available || !strings.Contains(capabilities.UnavailableReason, "exact release candidate") {
		t.Fatal("operator inventory bypassed managed release qualification", capabilities)
	}
	endpoint := database.PublicEndpoint{Spec: database.PublicEndpointSpec{Purpose: "read_write"}, Allocation: client.DatabasePublicEndpointAllocations()[0]}
	if err := client.ValidateDatabasePublicEndpoint(context.Background(), database.Resource{Spec: spec}, endpoint); err == nil || !strings.Contains(err.Error(), "release qualification") {
		t.Fatal("unqualified managed publication reached runtime", err)
	}

	options.ManagedDatabasePublicEndpointsQualified = true
	client = &Client{options: options}
	if capabilities = client.DatabasePublicEndpointCapabilities(spec); !capabilities.Available {
		t.Fatal("qualified managed inventory remained unavailable", capabilities)
	}
}
