package cluster

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"testing"
)

func TestPendingDomainsNeverReachIngressOrCertificateHosts(t *testing.T) {
	c := &Client{options: Options{AppDomain: "apps.example.test", ApprovedDomains: func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{"ready.example.test": true}, nil
	}}}
	target := Target{ApplicationID: "pending-test", Spec: spec.Application{Domains: map[string]string{"pending.example.test": "web", "ready.example.test": "web"}}}
	hosts, err := c.serviceHostnames(context.Background(), target, "web")
	if err != nil || len(hosts) != 2 || hosts[1] != "ready.example.test" {
		t.Fatal(hosts, err)
	}
	c.options.ApprovedDomains = func(context.Context, string) (map[string]bool, error) { return nil, errors.New("database unavailable") }
	if _, err = c.serviceHostnames(context.Background(), target, "web"); err == nil {
		t.Fatal("domain authority failure was ignored")
	}
}

func TestMissingDomainAuthorityFailsClosed(t *testing.T) {
	c := &Client{options: Options{AppDomain: "apps.example.test"}}
	hosts, err := c.serviceHostnames(context.Background(), Target{Spec: spec.Application{Domains: map[string]string{"unverified.example.test": "web"}}}, "web")
	if err != nil || len(hosts) != 1 {
		t.Fatal(hosts, err)
	}
}
