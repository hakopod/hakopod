package aws

import (
	"cmp"
	"context"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/actions/vm"
)

// TestAcceptanceSpotNotice interrupts a spot runner in the middle of a real
// GitHub job with AWS FIS. HAKOPOD_ACCEPTANCE_FIS names a script that takes
// the instance ID and starts a send-spot-instance-interruptions experiment;
// HAKOPOD_GITHUB_E2E_LONG_WORKFLOW (default long.yml) runs for ten minutes
// on [self-hosted, <label>]. The "watcher" cell boots the user data as
// shipped; "baseline" strips the spot notice watcher, so GitHub only learns
// of the interruption when it misses the runner. Each cell logs when the
// worker would see the notice, when GitHub concluded the job and how, and
// when the instance was gone.
func TestAcceptanceSpotNotice(t *testing.T) {
	if os.Getenv("HAKOPOD_AWS_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AWS_ACCEPTANCE=1 to launch instances in a real AWS account")
	}
	repo, token, fis := os.Getenv("HAKOPOD_GITHUB_E2E_REPO"), os.Getenv("HAKOPOD_GITHUB_E2E_TOKEN"), os.Getenv("HAKOPOD_ACCEPTANCE_FIS")
	if repo == "" || token == "" || fis == "" {
		t.Skip("set HAKOPOD_GITHUB_E2E_REPO, HAKOPOD_GITHUB_E2E_TOKEN and HAKOPOD_ACCEPTANCE_FIS")
	}
	gh, err := actions.New(token)
	if err != nil {
		t.Fatal(err)
	}
	e := &e2e{repo: repo, token: token, gh: gh, workflow: cmp.Or(os.Getenv("HAKOPOD_GITHUB_E2E_LONG_WORKFLOW"), "long.yml")}
	c := e2eClient(t, "arm64")
	t.Logf("installation %s", c.config.Installation)
	for _, watcher := range []bool{true, false} {
		name := map[bool]string{true: "watcher", false: "baseline"}[watcher]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e.spotNotice(t, c, fis, watcher)
		})
	}
}

func (e *e2e) spotNotice(t *testing.T, c *Client, fis string, watcher bool) {
	ctx := context.Background()
	label := "hakopod-spot-" + strings.ToLower(t.Name()[len(t.Name())-4:]) + time.Now().Format("150405")
	image, err := c.Image(ctx, vm.ImageRunsOnFull)
	if err != nil {
		t.Fatal(err)
	}
	run := e.dispatch(t, label)
	t.Cleanup(func() { e.cancel(run) })
	reg, err := e.gh.Register(ctx, actions.Target{Repository: e.repo}, label, []string{"self-hosted", "linux", "arm64", label})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.gh.Delete(context.Background(), actions.Target{Repository: e.repo}, reg.Runner.ID) })
	script, err := vm.RunnerUserData(vm.ImageRunsOnFull, "arm64", reg.EncodedConfig, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !watcher {
		start, end := strings.Index(script, "spot_watch() {"), strings.Index(script, "spot_watch &\n")
		if start < 0 || end < start {
			t.Fatal("the user data has no spot watcher to strip")
		}
		script = script[:start] + script[end+len("spot_watch &\n"):]
	}
	i, err := c.Launch(ctx, vm.LaunchInput{Name: "e2e-spot", InstanceTypes: []string{"t4g.small", "t4g.medium", "m7g.medium"}, Spot: true, UserData: script, Image: image,
		Tags: map[string]string{vm.TagCapacity: "acceptance-e2e", vm.TagPool: "e2e", vm.TagSlot: label}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background(), i.ID) })
	t.Logf("launched %s (%s, %s)", i.ID, i.Type, i.Lifecycle)
	job := e.awaitJob(t, run, func(j ghJob) bool { return j.Status == "in_progress" || j.Status == "completed" }, 15*time.Minute)
	time.Sleep(20 * time.Second) // well into the job
	out, err := exec.Command(fis, i.ID).CombinedOutput()
	if err != nil {
		t.Fatalf("FIS: %v: %s", err, out)
	}
	fired := time.Now()
	t.Logf("FIS experiment %s started for %s", strings.TrimSpace(string(out)), i.ID)
	since := func() string { return time.Since(fired).Round(time.Second).String() }
	var notice, done, gone string
	statuses := map[string]bool{}
	for deadline := time.Now().Add(20 * time.Minute); time.Now().Before(deadline) && (done == "" || gone == ""); time.Sleep(3 * time.Second) {
		if gone == "" {
			s, err := c.State(ctx, i.ID)
			if err == nil && notice == "" && s.Interrupted() && s.State == "running" {
				notice = since()
				t.Logf("NOTICE seen by the worker %s after FIS, instance still running", notice)
			}
			if err == nil && (s.State == "shutting-down" || s.State == "terminated") {
				gone = since()
				t.Logf("GONE %s after FIS (%s, interrupted %v)", gone, s.StateReason, s.Interrupted())
				if !s.Interrupted() {
					t.Error("the ended instance does not read as interrupted")
				}
			}
			if status := e.spotStatus(c, i.ID); status != "" && !statuses[status] {
				statuses[status] = true
				t.Logf("SPOT REQUEST status %s at %s", status, since())
			}
		}
		if done == "" {
			if jobs := e.jobs(t, run); len(jobs) == 1 && jobs[0].Status == "completed" {
				job, done = jobs[0], since()
				t.Logf("JOB completed %s after FIS, conclusion %q, job %d", done, job.Conclusion, job.ID)
			}
		}
	}
	console, _ := c.ConsoleOutput(ctx, i.ID)
	for _, l := range regexp.MustCompile(`(?m)^.*hakopod: .*$`).FindAllString(console, -1) {
		t.Logf("CONSOLE %s", l)
	}
	t.Logf("RESULT %s %s: notice seen %s, job done %s (%s), instance gone %s after FIS; job %d", t.Name(), i.ID, notice, done, job.Conclusion, gone, job.ID)
	if done == "" || gone == "" {
		t.Error("the job or the instance did not end within 20 minutes")
	}
}

// spotStatus reads the status code of the instance's spot request.
func (e *e2e) spotStatus(c *Client, id string) string {
	var in struct {
		Request string `xml:"reservationSet>item>instancesSet>item>spotInstanceRequestId"`
	}
	if c.call(context.Background(), "DescribeInstances", url.Values{"InstanceId.1": {id}}, &in) != nil || in.Request == "" {
		return ""
	}
	var out struct {
		Status string `xml:"spotInstanceRequestSet>item>status>code"`
	}
	if c.call(context.Background(), "DescribeSpotInstanceRequests", url.Values{"SpotInstanceRequestId.1": {in.Request}}, &out) != nil {
		return ""
	}
	return out.Status
}
