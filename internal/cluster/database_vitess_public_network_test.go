package cluster

import (
	"slices"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func TestVitessPublicIdentityPreservesPrivateNames(t *testing.T) {
	d := vitessTestDatabase()
	private := vitessIdentityNames(d)
	d.PublicEndpointNames = []string{"database-15432.public.example.test"}
	public := vitessIdentityNames(d)
	for _, name := range private {
		if !slices.Contains(public, name) {
			t.Fatal("public identity dropped a private topology name")
		}
	}
	if !slices.Contains(public, d.PublicEndpointNames[0]) || !slices.Contains(databaseIdentityNames(d), d.PublicEndpointNames[0]) {
		t.Fatal("identity issuance omitted the public gateway hostname")
	}
}

func TestVitessPublicIngressSelectsOnlyOwnedProxyGateways(t *testing.T) {
	d := vitessTestDatabase()
	d.PublicEndpointAccess = true
	options := Options{ProxyNamespace: "haproxy-controller", ProxyRelease: "hakopod-ingress"}
	policies := vitessNetworkPolicies(d, "namespace-uid", nil, options)
	gateway := policies[2]
	if gateway.Spec.PodSelector.MatchLabels[vitessComponentLabel] != "gateway" || len(gateway.Spec.Ingress) != 2 {
		t.Fatal("public ingress did not select only gateways")
	}
	rule := gateway.Spec.Ingress[1]
	if len(rule.From) != 1 || len(rule.Ports) != 1 || rule.Ports[0].Port.IntVal != 3306 {
		t.Fatal("public ingress exposed internal ports or additional peers")
	}
	peer := rule.From[0]
	if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != options.ProxyNamespace || peer.PodSelector == nil || len(peer.PodSelector.MatchLabels) != 2 || peer.PodSelector.MatchLabels["app.kubernetes.io/name"] != "kubernetes-ingress" || peer.PodSelector.MatchLabels["app.kubernetes.io/instance"] != options.ProxyRelease {
		t.Fatal("public ingress did not bind both proxy namespace and release")
	}
	if len(policies[0].Spec.Ingress) != 1 {
		t.Fatal("public ingress broadened the database base policy")
	}
	for name, mutate := range map[string]func(*database.Resource, *Options){
		"private":                 func(d *database.Resource, _ *Options) { d.PublicEndpointAccess = false },
		"restoring":               func(d *database.Resource, _ *Options) { d.Status = "restoring" },
		"uninspected recovery":    func(d *database.Resource, _ *Options) { d.Recovery = &database.Recovery{} },
		"missing proxy namespace": func(_ *database.Resource, o *Options) { o.ProxyNamespace = "" },
		"missing proxy release":   func(_ *database.Resource, o *Options) { o.ProxyRelease = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, configured := d, options
			mutate(&candidate, &configured)
			got := vitessNetworkPolicies(candidate, "namespace-uid", nil, configured)[2].Spec.Ingress
			for _, rule := range got {
				for _, peer := range rule.From {
					if peer.PodSelector != nil {
						t.Fatal("closed or unconfigured endpoint admitted the public proxy")
					}
				}
			}
		})
	}
	now := time.Now()
	d.Recovery = &database.Recovery{RestoredAt: &now, InspectedAt: &now}
	if len(vitessNetworkPolicies(d, "namespace-uid", nil, options)[2].Spec.Ingress) != 2 {
		t.Fatal("inspected restored database did not restore reviewed public ingress")
	}
}
