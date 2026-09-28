package actions

import (
	"context"
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
