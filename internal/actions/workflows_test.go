package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkflowIdentityRequiresExactRunnerAndAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/team/repo/actions/runs/9/attempts/2/jobs" {
			t.Error(r.URL.Path)
		}
		fmt.Fprint(w, `{"jobs":[{"id":1,"run_id":9,"run_attempt":1,"runner_id":42,"runner_name":"hakopod-one"},{"id":2,"run_id":9,"run_attempt":2,"runner_id":42,"runner_name":"hakopod-other"},{"id":3,"run_id":9,"run_attempt":2,"runner_id":42,"runner_name":"hakopod-one","steps":[]}]}`)
	}))
	defer srv.Close()
	c, _ := New("private-workflow-test-token")
	c.base = srv.URL
	job, err := c.AssignedJob(context.Background(), Observation{Repository: "team/repo", RunID: 9, Attempt: 2}, 42, "hakopod-one")
	if err != nil || job == nil || job.ID != 3 {
		t.Fatal(job, err)
	}
	if (Observation{Repository: "other/repo", RunID: 9, Attempt: 1}).Valid(Target{Organization: "team"}) {
		t.Fatal("escaped organization")
	}
}

func TestKnownWorkflowIdentityRequiresEveryRecordedBinding(t *testing.T) {
	for _, scenario := range []string{"valid", "job", "run", "attempt", "runner-id", "runner-name"} {
		t.Run(scenario, func(t *testing.T) {
			job := Job{ID: 3, RunID: 9, RunAttempt: 2, RunnerID: 42, RunnerName: "hakopod-one"}
			switch scenario {
			case "job":
				job.ID++
			case "run":
				job.RunID++
			case "attempt":
				job.RunAttempt++
			case "runner-id":
				job.RunnerID++
			case "runner-name":
				job.RunnerName = "hakopod-other"
			}
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/repos/team/repo/actions/jobs/3" || r.Method != http.MethodGet {
					t.Error("known identity rescanned workflow", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(job)
			}))
			defer srv.Close()
			client, _ := New("private-known-workflow-test-token")
			client.base = srv.URL
			actual, err := client.AssignedJobID(context.Background(), Observation{Repository: "team/repo", RunID: 9, Attempt: 2}, 42, "hakopod-one", 3)
			if scenario == "valid" {
				if err != nil || actual == nil || actual.ID != 3 {
					t.Fatal(actual, err)
				}
			} else if err == nil || actual != nil {
				t.Fatal("mismatched known job identity accepted", actual, err)
			}
			if requests != 1 {
				t.Fatal("known identity did not use exactly one lookup", requests)
			}
		})
	}
}
func TestWorkflowParserRejectsDiagnosticsAndBoundsOutput(t *testing.T) {
	data := []byte("[WORKER] private diagnostic\nHAKOPOD_WORKFLOW_V1 {\"job\":{\"repository\":\"team/repo\"},\"line\":2,\"text\":\"masked ***\"}\nHAKOPOD_WORKFLOW_V1 {\"line\":2,\"text\":\"duplicate\"}\n")
	out := ParseOutput(data)
	if len(out.Lines) != 1 || out.Lines[0].Text != "masked ***" || !out.Truncated || out.Job.Repository != "team/repo" {
		t.Fatal(out)
	}
	data = []byte(strings.Repeat("HAKOPOD_WORKFLOW_V1 {bad}\n", 100))
	if len(ParseOutput(data).Lines) != 0 {
		t.Fatal("malformed envelope accepted")
	}
}

func TestWorkflowParserRetainsMaskCommandsOutsideTheOutputWindow(t *testing.T) {
	var data strings.Builder
	for i := 1; i <= 10002; i++ {
		text := "ordinary output"
		if i == 1 {
			text = "::add-mask::private-window-value"
		} else if i == 10002 {
			text = "private-window-value"
		}
		encoded, _ := json.Marshal(text)
		fmt.Fprintf(&data, "HAKOPOD_WORKFLOW_V1 {\"line\":%d,\"text\":%s}\n", i, encoded)
	}
	out := ParseOutput([]byte(data.String()))
	if !out.Truncated || len(out.Lines) != 10000 || out.Lines[0].Number != 3 || out.Lines[len(out.Lines)-1].Text != "***" {
		t.Fatal("output window lost a previously registered mask")
	}
}

func TestWorkflowParserWithholdsOutputAfterIncompleteRedactionState(t *testing.T) {
	for name, record := range map[string]string{
		"oversized envelope": "HAKOPOD_WORKFLOW_V1 " + strings.Repeat("x", 128<<10),
		"oversized text":     `HAKOPOD_WORKFLOW_V1 {"line":1,"text":"::add-mask::` + strings.Repeat("x", 64<<10) + `"}`,
		"observer omission":  `HAKOPOD_WORKFLOW_V1 {"line":1,"text":"withheld","redaction_incomplete":true}`,
		"malformed envelope": `HAKOPOD_WORKFLOW_V1 {"line":1,"text":"::add-mask::private`,
	} {
		t.Run(name, func(t *testing.T) {
			data := record + "\nHAKOPOD_WORKFLOW_V1 {\"line\":2,\"text\":\"unknown-private-value\"}\n"
			out := ParseOutput([]byte(data))
			if !out.Truncated || len(out.Lines) == 0 {
				t.Fatal("incomplete redaction state was not represented")
			}
			for _, line := range out.Lines {
				if strings.Contains(line.Text, "unknown-private-value") {
					t.Fatal("skipped record could bypass dynamic mask state")
				}
			}
		})
	}
}
func TestWorkflowLogsRefuseCredentialRedirect(t *testing.T) {
	for _, location := range []string{"https://evil.invalid/log", "http://test.blob.core.windows.net/log", "https://test.blob.core.windows.net.evil.invalid/log", "https://user@test.blob.core.windows.net/log", "https://test.blob.core.windows.net:8443/log"} {
		called := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called++
			w.Header().Set("Location", location)
			w.WriteHeader(302)
		}))
		c, _ := New("private-workflow-test-token")
		c.base = srv.URL
		_, _, err := c.JobLogs(context.Background(), "team/repo", 9)
		srv.Close()
		if err == nil || called != 1 || strings.Contains(err.Error(), location) {
			t.Fatal("unsafe redirect", err, called)
		}
	}
}

func TestEscapedWorkflowLogsFitCloudProxy(t *testing.T) {
	lines := []LogLine{}
	for i := 1; i <= 100; i++ {
		lines = append(lines, LogLine{Number: int64(i), Text: strings.Repeat("<>&", 4096)})
	}
	for _, newest := range []bool{false, true} {
		bounded, truncated := BoundLogLines(lines, newest)
		encoded, err := json.Marshal(map[string]any{"lines": bounded})
		if err != nil || !truncated || len(bounded) == 0 || len(encoded) >= 2<<20 {
			t.Fatal("escaped logs exceed Cloud proxy limit", len(encoded), err)
		}
		if newest && bounded[len(bounded)-1].Number != 100 {
			t.Fatal("live window lost latest output")
		}
		if !newest && bounded[0].Number != 1 {
			t.Fatal("completed window lost first output")
		}
	}
}
