package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const bitbucketFixtureName = "hakopod-1234567890abcdef1234567890abcdef"
const bitbucketFixtureCredential = "fixture-bitbucket-control-plane-token"
const bitbucketFixtureRunner = "{cccccccc-cccc-4ccc-8ccc-cccccccccccc}"

type bitbucketRoundTrip func(*http.Request) (*http.Response, error)

func (f bitbucketRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bitbucketResponse(status int, value any) *http.Response {
	var data []byte
	if status != http.StatusNoContent {
		data, _ = json.Marshal(value)
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}

func bitbucketTestClient(t *testing.T, repository bool, handler bitbucketRoundTrip) *BitbucketClient {
	t.Helper()
	target := &BitbucketTarget{Workspace: "{aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}"}
	if repository {
		target.Repository = "{bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb}"
	}
	c, err := NewBitbucketClient(ProviderTarget{Provider: ProviderBitbucket, Bitbucket: target}, BitbucketCredential{AccessToken: bitbucketFixtureCredential}, BitbucketClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = bitbucketRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.bitbucket.org" || !strings.HasPrefix(r.URL.Path, "/2.0/") || r.Header.Get("Authorization") != "Bearer "+bitbucketFixtureCredential {
			t.Fatal("request escaped the fixed origin or lost its authentication")
		}
		return handler(r)
	})
	return c
}

func bitbucketRunnerFixture(c *BitbucketClient, status string) map[string]any {
	return map[string]any{
		"uuid": bitbucketFixtureRunner, "name": bitbucketFixtureName,
		"labels": []string{"self.hosted", "linux.arm64", c.ownership(bitbucketFixtureName)},
		"state":  map[string]any{"status": status, "cordoned": true},
	}
}

func TestBitbucketRegistrationPreservesScopedNativePrivateMaterial(t *testing.T) {
	for _, repository := range []bool{false, true} {
		t.Run(fmt.Sprint(repository), func(t *testing.T) {
			calls := 0
			var c *BitbucketClient
			c = bitbucketTestClient(t, repository, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != c.runnerScope() || r.URL.RawQuery != "" {
					t.Fatal("registration used the wrong scope or method")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["name"] != bitbucketFixtureName || body["uuid"] != nil || len(body) != 2 {
					t.Fatal("registration changed the documented request shape")
				}
				runner := bitbucketRunnerFixture(c, "UNREGISTERED")
				if actual, _ := json.Marshal(body["labels"]); string(actual) != fmt.Sprintf(`["self.hosted","linux.arm64","%s"]`, c.ownership(bitbucketFixtureName)) {
					t.Fatal("registration omitted its platform or ownership labels")
				}
				runner["oauth_client"] = map[string]string{"id": "fixture-runner-client", "secret": "fixture-private-runner-secret", "token_endpoint": "https://bitbucket.org/site/oauth2/access_token", "audience": "fixture"}
				return bitbucketResponse(200, runner), nil
			})
			result, err := c.Register(context.Background(), c.target, bitbucketFixtureName, []string{"self.hosted", "linux.arm64"})
			if err != nil || calls != 1 || result.Runner.ID != bitbucketFixtureRunner || !result.Runner.Busy || len(result.CleanupCredential) != 0 {
				t.Fatal("registration failed or implied independent cleanup/idle authority", err)
			}
			var private BitbucketManagerConfig
			if json.Unmarshal(result.ManagerConfig, &private) != nil || private.SchemaVersion != 1 || private.Target != *c.target.Bitbucket || private.OAuthSecret != "fixture-private-runner-secret" || private.Name != bitbucketFixtureName {
				t.Fatal("private runner envelope is incomplete")
			}
			public, _ := json.Marshal(result)
			if strings.Contains(string(public), "secret") || strings.Contains(string(public), "oauth") || strings.Contains(string(result.ManagerConfig), bitbucketFixtureCredential) {
				t.Fatal("registration exposed manager or control-plane credentials")
			}
		})
	}
}

func TestBitbucketRegistrationRejectsUnsupportedLabelsBeforeRequest(t *testing.T) {
	for _, labels := range [][]string{{}, {"self.hosted"}, {"linux.arm64"}, {"self.hosted", "linux", "linux.arm64"}, {"self.hosted", "linux", "needs-hyphen"}, {"self.hosted", "linux", "UPPER"}, {"self.hosted", "linux", "linux"}, {"self.hosted", "linux", "hakopod.owner.fake"}, {"self.hosted", "linux", "windows"}, {"self.hosted", "linux", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}} {
		c := bitbucketTestClient(t, false, func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid labels reached the provider")
			return nil, nil
		})
		if _, err := c.Register(context.Background(), c.target, bitbucketFixtureName, labels); err == nil {
			t.Fatal("unsupported Bitbucket labels were accepted")
		}
	}
}

func TestBitbucketLostRegistrationUsesScopedInventoryWithoutSecondPost(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	posts := 0
	var c *BitbucketClient
	c = bitbucketTestClient(t, true, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
			return nil, errors.New("untrusted error with " + bitbucketFixtureCredential)
		}
		if r.Method != http.MethodGet || r.URL.Path != c.runnerScope() {
			t.Fatal("recovery changed its original runner scope")
		}
		return bitbucketResponse(200, map[string]any{"values": []any{bitbucketRunnerFixture(c, "UNREGISTERED")}}), nil
	})
	budget := NewRequestBudget(func() time.Time { return now })
	c.budget = &budget
	_, err := c.Register(context.Background(), c.target, bitbucketFixtureName, []string{"self.hosted", "linux.arm64"})
	var failure *BitbucketError
	if !errors.As(err, &failure) || !failure.Ambiguous || strings.Contains(err.Error(), bitbucketFixtureCredential) {
		t.Fatal("lost registration was not recorded as a sanitized ambiguous outcome")
	}
	now = now.Add(time.Minute)
	runners, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName)
	if err != nil || len(runners) != 1 || runners[0].ID != bitbucketFixtureRunner || posts != 1 {
		t.Fatal("recovery failed or registered a second runner", err)
	}
}

func TestBitbucketUncertainInventoryNeverConfirmsAbsence(t *testing.T) {
	for name, body := range map[string]any{
		"null": nil, "missing values": map[string]any{}, "null values": map[string]any{"values": nil},
		"hidden remainder": map[string]any{"values": []any{}, "size": 1},
		"oversized scope":  map[string]any{"values": []any{}, "size": 501},
		"wrong page":       map[string]any{"values": []any{}, "page": 2},
		"oversized page":   map[string]any{"values": []any{}, "pagelen": 101},
		"empty next page":  map[string]any{"values": []any{}, "next": "https://api.bitbucket.org/2.0/other?page=2"},
	} {
		t.Run(name, func(t *testing.T) {
			c := bitbucketTestClient(t, true, func(*http.Request) (*http.Response, error) { return bitbucketResponse(200, body), nil })
			if result, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName); err == nil || result != nil {
				t.Fatal("incomplete inventory confirmed absence")
			}
		})
	}
	for _, status := range []int{204, 403, 404, 429, 503} {
		c := bitbucketTestClient(t, false, func(*http.Request) (*http.Response, error) {
			return bitbucketResponse(status, map[string]any{"credential": bitbucketFixtureCredential}), nil
		})
		if result, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName); err == nil || result != nil || strings.Contains(err.Error(), bitbucketFixtureCredential) {
			t.Fatal("failed inventory confirmed absence or exposed its response")
		}
	}
	c := bitbucketTestClient(t, false, func(*http.Request) (*http.Response, error) {
		return bitbucketResponse(200, map[string]any{"values": []any{}}), nil
	})
	if result, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName); err != nil || result == nil || len(result) != 0 {
		t.Fatal("a complete empty scoped collection did not confirm absence", err)
	}
}

func TestBitbucketFollowsOnlyScopedBoundedPagination(t *testing.T) {
	for _, variant := range []string{"complete", "loop", "other origin", "other scope", "filtered", "extra pages", "duplicate runner"} {
		t.Run(variant, func(t *testing.T) {
			calls := 0
			var c *BitbucketClient
			c = bitbucketTestClient(t, true, func(r *http.Request) (*http.Response, error) {
				calls++
				runner := bitbucketRunnerFixture(c, "ONLINE")
				runner["uuid"] = fmt.Sprintf("{%08d-cccc-4ccc-8ccc-cccccccccccc}", calls)
				if variant == "duplicate runner" {
					runner["uuid"] = bitbucketFixtureRunner
				}
				body := map[string]any{"values": []any{runner}}
				next := &url.URL{Scheme: "https", Host: "api.bitbucket.org", Path: c.runnerScope(), RawQuery: fmt.Sprintf("page=%d&pagelen=100", calls+1)}
				if variant == "complete" && calls == 2 {
					body["size"] = 2
					return bitbucketResponse(200, body), nil
				}
				switch variant {
				case "loop":
					next.RawQuery = "page=2&pagelen=100"
				case "other origin":
					next.Host = "attacker.invalid"
				case "other scope":
					next.Path = "/2.0/workspaces/other/pipelines-config/runners"
				case "filtered":
					next.RawQuery = "page=2&q=name%3Dother"
				}
				body["next"] = next.String()
				return bitbucketResponse(200, body), nil
			})
			result, err := c.FindOwned(context.Background(), c.target, bitbucketFixtureName)
			if variant == "complete" {
				if err != nil || len(result) != 2 || calls != 2 {
					t.Fatal("complete pagination lost an exact name match", err)
				}
			} else if err == nil || result != nil || calls > bitbucketMaxRunnerPages {
				t.Fatal("pagination escaped its origin/scope or did not fail closed")
			}
		})
	}
}

func TestBitbucketOwnershipAndOriginalTargetFenceReadsAndDeletes(t *testing.T) {
	for _, variant := range []string{"name", "marker", "state", "id", "missing detail", "different target"} {
		t.Run(variant, func(t *testing.T) {
			calls := 0
			var c *BitbucketClient
			c = bitbucketTestClient(t, true, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet {
					t.Fatal("uncertain ownership reached mutation")
				}
				runner := bitbucketRunnerFixture(c, "UNREGISTERED")
				if calls == 1 {
					return bitbucketResponse(200, map[string]any{"values": []any{runner}}), nil
				}
				switch variant {
				case "name":
					runner["name"] = "other"
				case "marker":
					runner["labels"] = []string{"self.hosted", "linux.arm64"}
				case "state":
					delete(runner, "state")
				case "id":
					runner["uuid"] = "{dddddddd-dddd-4ddd-8ddd-dddddddddddd}"
				case "missing detail":
					return bitbucketResponse(404, nil), nil
				}
				return bitbucketResponse(200, runner), nil
			})
			target := c.target
			if variant == "different target" {
				target.Bitbucket = &BitbucketTarget{Workspace: c.target.Bitbucket.Workspace}
			}
			if err := c.DeleteOwned(context.Background(), target, bitbucketFixtureRunner, bitbucketFixtureName); err == nil {
				t.Fatal("cleanup accepted changed scope or identity")
			}
			if variant == "different target" && calls != 0 {
				t.Fatal("changed target was queried")
			}
		})
	}
}

func TestBitbucketCleanupDoesNotInventDrainFromOfflineOrCordoned(t *testing.T) {
	for _, status := range []string{"UNREGISTERED", "ONLINE", "OFFLINE", "DISABLED", "ENABLED", "UNHEALTHY"} {
		t.Run(status, func(t *testing.T) {
			deletes := 0
			var c *BitbucketClient
			c = bitbucketTestClient(t, true, func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodDelete {
					deletes++
					return bitbucketResponse(204, nil), nil
				}
				if r.Method != http.MethodGet {
					t.Fatal("cleanup called an undocumented state/drain endpoint")
				}
				runner := bitbucketRunnerFixture(c, status)
				if r.URL.Path == c.runnerScope() {
					return bitbucketResponse(200, map[string]any{"values": []any{runner}}), nil
				}
				return bitbucketResponse(200, runner), nil
			})
			err := c.DeleteOwned(context.Background(), c.target, bitbucketFixtureRunner, bitbucketFixtureName)
			if status == "UNREGISTERED" {
				if err != nil || deletes != 1 {
					t.Fatal("unused registration cleanup failed", err)
				}
			} else if err == nil || deletes != 0 {
				t.Fatal("cleanup treated offline or cordoned as an atomic idle guarantee")
			}
		})
	}
}

func TestBitbucketUnavailableCapabilitiesDoNotAuthorizeJobAccess(t *testing.T) {
	c := bitbucketTestClient(t, false, func(*http.Request) (*http.Response, error) {
		t.Fatal("unqualified capability accessed network")
		return nil, nil
	})
	capability := c.Capabilities()
	if capability.Available || capability.Cache.Persistent || capability.Build.CrossArchitecture || len(capability.Build.NativeArchitectures) != 0 || capability.Isolation.SingleJob || capability.Isolation.ManagerCredentials || capability.CancellationScope != CancellationNone {
		t.Fatal("unqualified capability was advertised")
	}
	if _, ok := any(c).(ProviderJobClient); ok {
		// Public pipeline_step has no runner binding. Caller-supplied step IDs
		// therefore cannot authorize arbitrary repository log access.
		t.Fatal("Bitbucket falsely implements authoritative runner/job binding")
	}
	if _, ok := any(c).(ProviderCancellationClient); ok {
		t.Fatal("Bitbucket falsely authorizes whole-pipeline cancellation")
	}
	if _, ok := any(c).(ProviderDrainClient); ok {
		t.Fatal("Bitbucket falsely implements an atomic drain")
	}
}
