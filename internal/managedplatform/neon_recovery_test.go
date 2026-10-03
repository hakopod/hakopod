package managedplatform

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNeonRecoveryLSNRoundTripAndBounds(t *testing.T) {
	for _, value := range []string{"0/16B6C50", "A/FFFFFFFF", "FFFFFFFF/FFFFFFFF"} {
		parsed, err := parseNeonLSN(value)
		if err != nil {
			t.Fatalf("parse %s: %v", value, err)
		}
		roundTrip, err := parseNeonLSN(formatNeonLSN(parsed))
		if err != nil || roundTrip != parsed {
			t.Fatalf("LSN %s did not round trip", value)
		}
	}
	for _, value := range []string{"", "1", "1/", "/1", "100000000/0", "0/100000000", "x/1"} {
		if _, err := parseNeonLSN(value); err == nil {
			t.Fatalf("invalid LSN %q accepted", value)
		}
	}
}

func TestNeonRecoveryExternalKeyRebindsOnlyArtifactIdentity(t *testing.T) {
	tenant := strings.Repeat("a", 32)
	timeline := strings.Repeat("b", 32)
	token := strings.Repeat("c", 32)
	for _, test := range []struct{ component, resource, want string }{
		{"tenant", strings.Repeat("d", 32) + ":" + token, tenant},
		{"timeline", strings.Repeat("e", 64) + ":" + token, tenant + "/" + timeline},
		{"pageserver-registration-0", strings.Repeat("f", 64) + ":" + token, strings.Repeat("f", 64)},
	} {
		got, err := NeonRecoveryExternalKey(test.component, test.resource, tenant, timeline)
		if err != nil || got != test.want {
			t.Fatalf("%s recovery key = %q, %v", test.component, got, err)
		}
	}
	if _, err := NeonRecoveryExternalKey("compute-compute-0", "not-owned", tenant, timeline); err == nil {
		t.Fatal("unowned provider resource accepted for recovery")
	}
}

func TestNeonRecoveryCheckpointFencesEveryOwnedMember(t *testing.T) {
	for _, test := range []struct {
		name, changedHost, changedCommit, changedOwner string
		moveAttachment, foreignAttachment              bool
		wantError                                      bool
	}{{name: "complete"}, {name: "divergent commit", changedHost: "sk-2.test", changedCommit: "0/16B6C40", wantError: true}, {name: "foreign owner", changedHost: "sk-1.test", changedOwner: strings.Repeat("f", 32), wantError: true}, {name: "placement moved", moveAttachment: true, wantError: true}, {name: "foreign pageserver", foreignAttachment: true, wantError: true}} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			owner := strings.Repeat("a", 32)
			timelineOwner := strings.Repeat("b", 32)
			members := `[{"id":1,"host":"sk-0.test","pg_port":5454},{"id":2,"host":"sk-1.test","pg_port":5454},{"id":3,"host":"sk-2.test","pg_port":5454}]`
			calls := map[string]int{}
			lifecycle := &durableNeonFixture{op: DurableOperation{ID: testOperation, PlatformID: strings.Repeat("4", 32), Revision: 1, Kind: "update"}, claims: map[string]DurableResourceClaim{}, intents: map[string]DurableResourceIntent{}, events: &events}
			runtime := &DurableNeonRuntime{lifecycle: lifecycle, control: &NeonRuntime{config: NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "controller", Origin: "https://controller.test", Token: "controller-secret-token"}, SafekeeperToken: "safekeeper-secret-token", Safekeepers: []NeonSafekeeperRegistration{{Name: "0", NodeID: 1, Host: "sk-0.test"}, {Name: "1", NodeID: 2, Host: "sk-1.test"}, {Name: "2", NodeID: 3, Host: "sk-2.test"}}, Pageservers: []NeonPageserverRegistration{{Name: "0", NodeID: 1, Host: "ps-0.test"}, {Name: "1", NodeID: 2, Host: "ps-1.test"}}, RequestTimeout: time.Second}, client: &http.Client{Transport: neonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls[request.Method+" "+request.URL.Host+request.URL.Path]++
				body := `{}`
				host := strings.Split(request.URL.Host, ":")[0]
				wantToken := "controller-secret-token"
				if strings.HasPrefix(host, "ps-") {
					wantToken = "pageserver-secret-token"
				}
				if strings.HasPrefix(host, "sk-") {
					wantToken = "safekeeper-secret-token"
				}
				if request.Header.Get("Authorization") != "Bearer "+wantToken {
					t.Fatal("control request used another component's credential")
				}
				switch {
				case request.Method == http.MethodGet && request.URL.Path == "/control/v1/tenant/"+testTenant:
					body = `{"ownership_token":"` + owner + `","ownership_state":"completed"}`
				case request.Method == http.MethodPost && request.URL.Path == "/debug/v1/inspect":
					body = `{"attachment":[7,1]}`
					if test.foreignAttachment {
						body = `{"attachment":[7,9]}`
					}
					if test.moveAttachment && calls[request.Method+" "+request.URL.Host+request.URL.Path] > 1 {
						body = `{"attachment":[8,2]}`
					}
				case request.URL.Path == "/v1/status":
					ids := map[string]string{"sk-0.test": "1", "sk-1.test": "2", "sk-2.test": "3"}
					body = `{"id":` + ids[host] + `}`
				case strings.HasSuffix(request.URL.Path, "/timeline/"+testTimeline) && strings.HasPrefix(host, "sk-"):
					claim := timelineOwner
					if host == test.changedHost && test.changedOwner != "" {
						claim = test.changedOwner
					}
					commit := "0/16B6C50"
					if host == test.changedHost && test.changedCommit != "" {
						commit = test.changedCommit
					}
					body = `{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","ownership_token":"` + claim + `","mconf":{"generation":13,"members":` + members + `},"flush_lsn":"0/16B6C50","commit_lsn":"` + commit + `"}`
				case strings.HasPrefix(host, "ps-") && request.Method == http.MethodGet:
					body = `{"remote_consistent_lsn_visible":"0/16B6C50"}`
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}}
			runtime.control.config.PageserverToken = "pageserver-secret-token"
			fence, err := runtime.CheckpointRecovery(context.Background(), testTenant, testTimeline, owner, timelineOwner)
			if test.wantError {
				if err == nil {
					t.Fatal("unsafe checkpoint succeeded")
				}
				return
			}
			if err != nil || fence.TimelineGeneration != 13 || fence.CommitLSN != "0/16B6C50" || len(fence.Pageservers) != 1 || fence.Pageservers["0"] != "0/16B6C50" {
				t.Fatalf("checkpoint fence failed: %#v %v", fence, err)
			}
			for _, host := range []string{"sk-0.test:7676", "sk-1.test:7676", "sk-2.test:7676", "ps-0.test:9898"} {
				if calls[http.MethodPost+" "+host+"/v1/tenant/"+testTenant+"/timeline/"+testTimeline+"/checkpoint"] == 0 {
					t.Fatalf("missing checkpoint for %s", host)
				}
			}
			for call := range calls {
				if strings.Contains(call, "ps-1.test") {
					t.Fatal("secondary pageserver was treated as an active tenant")
				}
			}
		})
	}
}
