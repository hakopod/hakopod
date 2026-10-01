package database

import (
	"fmt"
	"testing"
	"time"
)

func publicMemberFixture() (Resource, PublicEndpoint) {
	d := publicEndpointDatabase()
	d.Spec.Engine, d.Spec.Version, d.Spec.Replicas = "mongodb", "8.0", 2
	d.Observation.Endpoints = []Endpoint{{Purpose: "read_write", Port: 27017}}
	e := PublicEndpoint{ID: "endpoint", DatabaseID: d.ID}
	for i := 0; i < 3; i++ {
		member := Member{Name: fmt.Sprintf("database-%d", i), UID: fmt.Sprintf("uid-%d", i), Ready: true}
		d.Observation.Members = append(d.Observation.Members, member)
		e.MemberAllocations = append(e.MemberAllocations, PublicEndpointMemberAllocation{MemberName: member.Name, MemberUID: member.UID, Allocation: PublicEndpointAllocation{ID: fmt.Sprintf("allocation-%d", i), Host: fmt.Sprintf("member-%d.db.example.test", i), Address: "192.0.2.10", Port: int32(15432 + i)}})
	}
	e.Allocation = e.MemberAllocations[0].Allocation
	return d, e
}

func TestPublicEndpointMembersBindEveryDiscoveredIdentity(t *testing.T) {
	d, e := publicMemberFixture()
	input := PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}
	plan, err := planPublicEndpointMembers(d, input, e, time.Now())
	if err != nil || len(plan.MemberAllocations) != 3 {
		t.Fatal("member review failed", err)
	}
	plan.MemberAllocations[0].MemberUID = "modified"
	if e.MemberAllocations[0].MemberUID == "modified" {
		t.Fatal("review aliases durable member inventory")
	}
	for _, change := range []func(*Resource, *PublicEndpoint){
		func(d *Resource, e *PublicEndpoint) { d.Observation.Members[1].UID = "replaced" },
		func(d *Resource, e *PublicEndpoint) { d.Observation.Members[1].Ready = false },
		func(d *Resource, e *PublicEndpoint) { e.MemberAllocations = e.MemberAllocations[:2] },
		func(d *Resource, e *PublicEndpoint) {
			e.MemberAllocations[1].MemberName = e.MemberAllocations[0].MemberName
		},
		func(d *Resource, e *PublicEndpoint) { d.Observation.Members[1].Name = d.Observation.Members[0].Name },
	} {
		d, e := publicMemberFixture()
		change(&d, &e)
		if _, err := planPublicEndpointMembers(d, input, e, time.Now()); err == nil {
			t.Fatal("stale or incomplete member review accepted")
		}
	}
	if _, err := PlanPublicEndpointMembers(d, input, e, time.Now()); err == nil {
		t.Fatal("unqualified MongoDB gate opened")
	}
}

func TestPublicEndpointMembersRejectConflictingAllocations(t *testing.T) {
	for _, change := range []func(*PublicEndpoint){
		func(e *PublicEndpoint) { e.Allocation.Host = "other.db.example.test" },
		func(e *PublicEndpoint) { e.MemberAllocations[1].Allocation.ID = e.Allocation.ID },
		func(e *PublicEndpoint) { e.MemberAllocations[1].Allocation.Host = e.Allocation.Host },
		func(e *PublicEndpoint) { e.MemberAllocations[1].Allocation.Port = e.Allocation.Port },
		func(e *PublicEndpoint) { e.MemberAllocations[1].Allocation.Address = "::1" },
		func(e *PublicEndpoint) { e.MemberAllocations[1].MemberUID = "uid\nheader" },
		func(e *PublicEndpoint) { e.MemberAllocations[1].MemberUID = "uid bad" },
		func(e *PublicEndpoint) {
			e.MemberAllocations[1], e.MemberAllocations[2] = e.MemberAllocations[2], e.MemberAllocations[1]
		},
		func(e *PublicEndpoint) { e.MemberAllocations = make([]PublicEndpointMemberAllocation, MaxMembers+1) },
	} {
		_, e := publicMemberFixture()
		change(&e)
		if _, err := PublicEndpointAllocations(e); err == nil {
			t.Fatal("invalid public inventory accepted")
		}
	}
	_, e := publicMemberFixture()
	e.MemberAllocations = nil
	allocations, err := PublicEndpointAllocations(e)
	if err != nil || len(allocations) != 1 || allocations[0].MemberName != "" || allocations[0].Allocation != e.Allocation {
		t.Fatal("legacy singleton allocation changed", err)
	}
}

func TestPublicEndpointCertificateNamesHaveBoundedMemberBudget(t *testing.T) {
	names := make([]string, MaxPublicEndpointNames)
	for i := range names {
		names[i] = fmt.Sprintf("member-%03d.db.example.test", i)
	}
	if _, err := NormalizePublicEndpointNames(names); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizePublicEndpointNames(append(names, "overflow.db.example.test")); err == nil {
		t.Fatal("certificate name budget exceeded")
	}
}
