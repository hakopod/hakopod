package database

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicEndpointReviewSurvivesPrivateTargetSerialization(t *testing.T) {
	for _, spec := range []Spec{
		{Engine: "vitess", Mode: "cluster", Replicas: 2},
		{Engine: "oracle", Mode: "standalone", Shards: 1, Oracle: &OracleConfig{Edition: "free"}},
	} {
		t.Run(spec.Engine, func(t *testing.T) {
			route, err := PublicEndpointRouteFor(spec, "read_write")
			if err != nil {
				t.Fatal(err)
			}
			current := PublicEndpointReview{Spec: PublicEndpointSpec{Purpose: route.Purpose}, Route: &route, RouteFingerprint: route.Fingerprint()}
			raw, err := json.Marshal(current)
			if err != nil || strings.Contains(string(raw), "backend") || strings.Contains(string(raw), route.BackendDatabase) {
				t.Fatal("serialized public review exposed its private target")
			}
			var persisted PublicEndpointReview
			if err = json.Unmarshal(raw, &persisted); err != nil || !persisted.MatchesRoute(current) {
				t.Fatal("durable public review rejected its unchanged route", err)
			}
			for _, mutate := range []func(*PublicEndpointRoute){
				func(r *PublicEndpointRoute) { r.BackendDatabase = "another-database" },
				func(r *PublicEndpointRoute) { r.BackendUser = "another-user" },
				func(r *PublicEndpointRoute) { r.BackendService = "another-service" },
				func(r *PublicEndpointRoute) { r.BackendPort++ },
			} {
				changed := route
				mutate(&changed)
				next := current
				next.Route, next.RouteFingerprint = &changed, changed.Fingerprint()
				if persisted.MatchesRoute(next) {
					t.Fatal("persisted review accepted a changed private target")
				}
			}
			persisted.Route.ReadOnly = true
			if persisted.MatchesRoute(current) {
				t.Fatal("persisted review accepted a changed public descriptor")
			}
		})
	}
}
