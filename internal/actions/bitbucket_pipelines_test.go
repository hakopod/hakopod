package actions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const bitbucketFixturePipeline = "{dddddddd-dddd-4ddd-8ddd-dddddddddddd}"
const bitbucketFixtureStep = "{eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee}"

func bitbucketPipelineFixture(c *BitbucketClient) map[string]any {
	return map[string]any{"uuid": bitbucketFixturePipeline, "build_number": 41, "created_on": "2026-09-30T00:00:00Z", "state": map[string]any{"name": "IN_PROGRESS"}, "repository": map[string]any{"uuid": c.target.Bitbucket.Repository}, "variables": []any{map[string]any{"value": "private-variable-must-not-escape"}}}
}

func bitbucketStepFixture() map[string]any {
	return map[string]any{"uuid": bitbucketFixtureStep, "name": "Build", "state": map[string]any{"name": "IN_PROGRESS"}, "script_commands": []any{map[string]any{"command": "private-command-must-not-escape"}}}
}

func TestBitbucketRepositoryActivityHasNoInventedRunnerAssignment(t *testing.T) {
	var client *BitbucketClient
	client = bitbucketTestClient(t, true, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/pipelines") {
			if request.URL.Query().Get("pagelen") != "20" || request.URL.Query().Get("page") != "1" || request.URL.Query().Get("sort") != "-created_on" {
				t.Fatal("repository activity escaped its page bound")
			}
			return bitbucketResponse(200, map[string]any{"values": []any{bitbucketPipelineFixture(client)}}), nil
		}
		if strings.HasSuffix(request.URL.Path, "/steps") {
			return bitbucketResponse(200, map[string]any{"values": []any{bitbucketStepFixture()}}), nil
		}
		return bitbucketResponse(200, bitbucketPipelineFixture(client)), nil
	})
	pipelines, truncated, err := client.RepositoryPipelines(context.Background(), client.target, 1)
	if err != nil || truncated || len(pipelines) != 1 || pipelines[0].ID != bitbucketFixturePipeline {
		t.Fatal("repository activity failed", err)
	}
	steps, truncated, err := client.RepositoryPipelineSteps(context.Background(), client.target, bitbucketFixturePipeline)
	if err != nil || truncated || len(steps) != 1 || steps[0].ID != bitbucketFixtureStep {
		t.Fatal("pipeline steps failed", err)
	}
	encoded, _ := json.Marshal(struct{ Pipelines, Steps any }{pipelines, steps})
	for _, forbidden := range []string{"runner_id", "runner_name", "private-variable", "private-command", "script_commands", "variables"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("activity exposed unrelated private fields or invented a runner binding")
		}
	}
}

func TestBitbucketRepositoryActivityRejectsScopeAndPageBeforeRequests(t *testing.T) {
	client := bitbucketTestClient(t, true, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid read reached the provider")
		return nil, nil
	})
	for _, page := range []int{0, -1, 6} {
		if _, _, err := client.RepositoryPipelines(context.Background(), client.target, page); err == nil {
			t.Fatal("activity accepted unbounded page")
		}
	}
	for _, id := range []string{"", "../other", strings.Trim(bitbucketFixturePipeline, "{}"), "{not-a-uuid}"} {
		if _, _, err := client.RepositoryPipelineSteps(context.Background(), client.target, id); err == nil {
			t.Fatal("activity accepted invalid pipeline identity")
		}
		if _, _, err := client.RepositoryStepLogs(context.Background(), client.target, bitbucketFixturePipeline, id); err == nil {
			t.Fatal("activity accepted invalid step identity")
		}
	}
	other := client.target
	other.Bitbucket = &BitbucketTarget{Workspace: client.target.Bitbucket.Workspace, Repository: bitbucketFixtureRunner}
	if _, _, err := client.RepositoryPipelines(context.Background(), other, 1); err == nil {
		t.Fatal("activity accepted another repository")
	}
}

func TestBitbucketRepositoryLogsVerifyMetadataAndRange(t *testing.T) {
	for _, variant := range []string{"complete", "partial", "partial record", "all within range", "invalid range", "other pipeline", "other repository", "other step", "redirect"} {
		t.Run(variant, func(t *testing.T) {
			logCalls := 0
			var client *BitbucketClient
			client = bitbucketTestClient(t, true, func(request *http.Request) (*http.Response, error) {
				if strings.HasSuffix(request.URL.Path, "/log") {
					logCalls++
					if request.Header.Get("Range") != "bytes=0-1048575" {
						t.Fatal("log read lost its byte range")
					}
					response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("hello\n"))}
					switch variant {
					case "partial":
						response.StatusCode, response.Header["Content-Range"] = 206, []string{"bytes 0-5/100"}
					case "partial record":
						response.StatusCode, response.Header["Content-Range"] = 206, []string{"bytes 0-20/100"}
						response.Body = io.NopCloser(strings.NewReader("hello\nprivate-partial"))
					case "all within range":
						response.StatusCode, response.Header["Content-Range"] = 206, []string{"bytes 0-5/6"}
					case "invalid range":
						response.StatusCode, response.Header["Content-Range"] = 206, []string{"bytes 1-6/100"}
					case "redirect":
						response.StatusCode, response.Header["Location"] = 302, []string{"https://attacker.invalid/token"}
					}
					return response, nil
				}
				if strings.Contains(request.URL.Path, "/steps/") {
					step := bitbucketStepFixture()
					if variant == "other step" {
						step["uuid"] = bitbucketFixtureRunner
					}
					return bitbucketResponse(200, step), nil
				}
				pipeline := bitbucketPipelineFixture(client)
				if variant == "other pipeline" {
					pipeline["uuid"] = bitbucketFixtureRunner
				}
				if variant == "other repository" {
					pipeline["repository"] = map[string]any{"uuid": bitbucketFixtureRunner}
				}
				return bitbucketResponse(200, pipeline), nil
			})
			lines, truncated, err := client.RepositoryStepLogs(context.Background(), client.target, bitbucketFixturePipeline, bitbucketFixtureStep)
			if strings.HasPrefix(variant, "other") {
				if err == nil || logCalls != 0 {
					t.Fatal("logs were read without verified repository/pipeline/step identity")
				}
			} else if variant == "redirect" || variant == "invalid range" {
				if err == nil || lines != nil {
					t.Fatal("unsafe log response was accepted")
				}
			} else if err != nil || len(lines) != 1 || lines[0].Text != "hello" || truncated != strings.HasPrefix(variant, "partial") {
				t.Fatal("safe bounded log response was not preserved", err)
			}
		})
	}
}

func TestBitbucketManagementCredentialFormatsAreStrictAndPrivate(t *testing.T) {
	for _, value := range []string{bitbucketFixtureCredential, `{"email":"owner@example.com","api_token":"fixture-private-api-token"}`} {
		if _, err := ParseBitbucketCredential(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"", "short", `{"email":"owner@example.com","api_token":"fixture-private-api-token","unknown":true}`, `{"oauth_client_id":"fixture-client","oauth_client_secret":"fixture-private-secret"}`, `{"email":"owner@example.com","api_token":"fixture-private-api-token"} {}`, strings.Repeat("a", 8193)} {
		if _, err := ParseBitbucketCredential(value); err == nil || strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("invalid management secret accepted or private material entered diagnostics")
		}
	}
}
