package api

import (
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func TestPublicEndpointIdentityIntentSurvivesHealthAndClosureRetries(t *testing.T) {
	for _, phase := range []string{"identity", "tls", "provider", "publishing", "observing", "identity_closing"} {
		if !databasePublicEndpointIdentityStarted(phase) || databasePublicEndpointHealthRetryPhase(phase) != "identity" {
			t.Fatal("durable identity intent was lost", phase)
		}
	}
	for _, phase := range []string{"", "health", "closing", "unrecognized"} {
		if databasePublicEndpointIdentityStarted(phase) || databasePublicEndpointHealthRetryPhase(phase) != "health" {
			t.Fatal("identity change allowed before durable intent", phase)
		}
	}
}

func TestPublicEndpointIdentityConvergenceRetainsReviewGuards(t *testing.T) {
	stamp := time.Now().UTC()
	d := database.Resource{Revision: 3, Status: "ready"}
	review := &database.PublicEndpointReview{DatabaseRevision: 3, TopologyFingerprint: "reviewed-topology"}
	pending := database.Observation{Status: "pending", TopologyFingerprint: "reviewed-topology", TLS: &database.TLSObservation{Fingerprint: "new-leaf"}}
	for _, phase := range []string{"identity", "tls", "provider", "publishing", "identity_closing"} {
		if !databasePublicEndpointIdentityConverging(d, pending, review, phase) {
			t.Fatal("expected leaf reload became terminal failure", phase)
		}
	}
	if databasePublicEndpointIdentityConverging(d, pending, review, "health") {
		t.Fatal("unreviewed pre-identity failure was tolerated")
	}
	for _, kind := range []string{"revision", "restoring", "uninspected", "topology", "missing-review"} {
		t.Run(kind, func(t *testing.T) {
			changed, observed, accepted := d, pending, review
			switch kind {
			case "revision":
				changed.Revision++
			case "restoring":
				changed.Status = "restoring"
			case "uninspected":
				changed.Recovery = &database.Recovery{RestoredAt: &stamp}
			case "topology":
				observed.TopologyFingerprint = "another-topology"
			case "missing-review":
				accepted = nil
			}
			if databasePublicEndpointIdentityConverging(changed, observed, accepted, "identity") {
				t.Fatal("identity retry exempted a review guard")
			}
		})
	}
	ready := pending
	ready.Status, ready.TLS = "ready", &database.TLSObservation{Fingerprint: "new-leaf", Verified: true, PlaintextRejected: true}
	if databasePublicEndpointIdentityConverging(d, ready, review, "tls") {
		t.Fatal("healthy observation bypassed full review and route validation")
	}
}
