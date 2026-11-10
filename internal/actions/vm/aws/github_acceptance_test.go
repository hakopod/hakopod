package aws

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/actions/vm"
)

// TestAcceptanceGitHubJob runs real GitHub Actions jobs on runner VMs the way
// the control plane starts them: a JIT registration from actions.Client, the
// boot script from vm.RunnerUserData, then a cold launch or a standby claim.
// Each run dispatches HAKOPOD_GITHUB_E2E_WORKFLOW (default e2e.yml) in
// HAKOPOD_GITHUB_E2E_REPO with a fresh label; the workflow takes a "label"
// input and runs one job on [self-hosted, <label>]. The token
// (HAKOPOD_GITHUB_E2E_TOKEN) needs repo and workflow scopes; it never reaches
// the VM, which the test checks against the instance's user data.
//
// It times dispatch to pickup and to completion, and launch or claim to the
// instance's termination, and checks that the runner unregisters and that a
// second job for the same label stays queued. HAKOPOD_ACCEPTANCE_RUNS repeats
// the on-demand and warm cells (default 1). Pick cells with -run.
func TestAcceptanceGitHubJob(t *testing.T) {
	if os.Getenv("HAKOPOD_AWS_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AWS_ACCEPTANCE=1 to launch instances in a real AWS account")
	}
	repo, token := os.Getenv("HAKOPOD_GITHUB_E2E_REPO"), os.Getenv("HAKOPOD_GITHUB_E2E_TOKEN")
	if repo == "" || token == "" {
		t.Skip("set HAKOPOD_GITHUB_E2E_REPO and HAKOPOD_GITHUB_E2E_TOKEN to run GitHub jobs")
	}
	runs, err := strconv.Atoi(cmp.Or(os.Getenv("HAKOPOD_ACCEPTANCE_RUNS"), "1"))
	if err != nil || runs < 1 {
		t.Fatal("HAKOPOD_ACCEPTANCE_RUNS is not a positive number")
	}
	gh, err := actions.New(token)
	if err != nil {
		t.Fatal(err)
	}
	e := &e2e{repo: repo, token: token, gh: gh, workflow: cmp.Or(os.Getenv("HAKOPOD_GITHUB_E2E_WORKFLOW"), "e2e.yml"),
		clients: map[string]*Client{"arm64": e2eClient(t, "arm64"), "amd64": e2eClient(t, "amd64")}}
	t.Logf("installations %s, %s", e.clients["arm64"].config.Installation, e.clients["amd64"].config.Installation)
	t.Cleanup(func() { e.summarize(t) })
	cell := func(name, arch string, spot, warm bool, n int) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for i := range n {
				t.Run(strconv.Itoa(i+1), func(t *testing.T) {
					t.Parallel()
					e.run(t, name, arch, spot, warm, i == 0 && name == "cold-arm64")
				})
			}
		})
	}
	cell("cold-arm64", "arm64", false, false, runs)
	cell("warm-arm64", "arm64", false, true, runs)
	cell("cold-arm64-spot", "arm64", true, false, 1)
	cell("cold-amd64", "amd64", false, false, 1)
}

// e2eSlots keeps at most six of these instances in the account at once.
var e2eSlots = make(chan struct{}, 6)

func e2eClient(t *testing.T, arch string) *Client {
	t.Helper()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	c, err := New(context.Background(), Config{
		Installation:     cmp.Or(os.Getenv("HAKOPOD_ACCEPTANCE_PREFIX"), "acceptance-e2e-") + hex.EncodeToString(suffix),
		Region:           os.Getenv("AWS_REGION"),
		RoleARN:          os.Getenv("HAKOPOD_ACTIONS_VM_ROLE_ARN"),
		ExternalID:       os.Getenv("HAKOPOD_ACTIONS_VM_EXTERNAL_ID"),
		SubnetIDs:        []string{os.Getenv("HAKOPOD_ACTIONS_VM_SUBNET")},
		SecurityGroupIDs: []string{os.Getenv("HAKOPOD_ACTIONS_VM_SECURITY_GROUP")},
		Architecture:     arch,
		DiskGiB:          30, // RunsOn's images need 30
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type e2e struct {
	repo, token, workflow string
	gh                    *actions.Client
	clients               map[string]*Client

	sync.Mutex
	results map[string][]e2eTiming
}

// e2eTiming: dispatch to the job running, dispatch to the job done, and the
// launch or claim call to the instance terminated.
type e2eTiming struct{ pickup, done, gone time.Duration }

func (e *e2e) run(t *testing.T, cell, arch string, spot, warm, sameLabel bool) {
	ctx := context.Background()
	c := e.clients[arch]
	e2eSlots <- struct{}{}
	t.Cleanup(func() { <-e2eSlots })
	nonce := make([]byte, 5)
	rand.Read(nonce)
	label := "hakopod-e2e-" + hex.EncodeToString(nonce)
	types := map[string][]string{"arm64": {"t4g.small"}, "amd64": {"t3a.small"}}[arch]
	image, err := c.Image(ctx, vm.ImageRunsOnFull)
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]string{vm.TagCapacity: "acceptance-e2e", vm.TagPool: "e2e", vm.TagSlot: label}
	var instance string
	t.Cleanup(func() {
		if instance != "" {
			if err := c.Terminate(context.Background(), instance); err != nil {
				t.Errorf("cleanup: %v; terminate %s by hand", err, instance)
			}
		}
	})

	if warm {
		standby, err := vm.RunnerUserData(vm.ImageRunsOnFull, arch, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		i, err := c.Launch(ctx, vm.LaunchInput{Name: "e2e-standby", InstanceTypes: types, UserData: standby, Image: image, StopOnShutdown: true,
			Tags: map[string]string{vm.TagCapacity: "acceptance-e2e", vm.TagPoolState: "standby", vm.TagImage: image}})
		if err != nil {
			t.Fatal(err)
		}
		instance = i.ID
		awaitState(t, c, i.ID, "stopped", 15*time.Minute)
		t.Logf("standby %s stopped %s after its launch", i.ID, time.Since(start).Round(time.Second))
	}

	// The clock starts when the job is queued. Registration and the boot
	// script follow, as they do for a slot the control plane creates.
	dispatched := time.Now()
	run := e.dispatch(t, label)
	t.Cleanup(func() { e.cancel(run) })
	reg, err := e.gh.Register(ctx, actions.Target{Repository: e.repo}, label, []string{"self-hosted", "linux", arch, label})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.gh.Delete(context.Background(), actions.Target{Repository: e.repo}, reg.Runner.ID) })
	script, err := vm.RunnerUserData(vm.ImageRunsOnFull, arch, reg.EncodedConfig, 30)
	if err != nil {
		t.Fatal(err)
	}
	launched := time.Now()
	if warm {
		claimed := maps.Clone(tags)
		claimed[vm.TagPoolState] = "claimed"
		err = c.Claim(ctx, instance, script, claimed)
	} else {
		var i vm.Instance
		i, err = c.Launch(ctx, vm.LaunchInput{Name: "e2e", InstanceTypes: types, Spot: spot, UserData: script, Image: image, Tags: tags})
		instance = i.ID
		if err == nil {
			t.Logf("launched %s (%s, %s)", i.ID, i.Type, i.Lifecycle)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	e.checkUserData(t, c, instance, reg.EncodedConfig)

	job := e.awaitJob(t, run, func(j ghJob) bool { return j.Status == "in_progress" || j.Status == "completed" }, 15*time.Minute)
	pickup := time.Since(dispatched)
	if job.RunnerName != label {
		t.Errorf("job picked up by %q, not %q", job.RunnerName, label)
	}
	job = e.awaitJob(t, run, func(j ghJob) bool { return j.Status == "completed" }, 10*time.Minute)
	done := time.Since(dispatched)
	var second int64
	if sameLabel {
		second = e.dispatch(t, label)
		t.Cleanup(func() { e.cancel(second) })
	}
	gone := awaitGone(t, c, instance, 5*time.Minute).Sub(launched)
	e.awaitUnregistered(t, reg.Runner.ID, 3*time.Minute)
	r := e2eTiming{pickup, done, gone}
	t.Logf("RESULT %s %s: pickup %s, done %s, launch to terminated %s, conclusion %s, GitHub job %s to %s", cell, instance,
		r.pickup.Round(100*time.Millisecond), r.done.Round(100*time.Millisecond), r.gone.Round(100*time.Millisecond), job.Conclusion, job.StartedAt, job.CompletedAt)
	if job.Conclusion != "success" {
		t.Errorf("the job concluded %q", job.Conclusion)
	}
	e.Lock()
	if e.results == nil {
		e.results = map[string][]e2eTiming{}
	}
	e.results[cell] = append(e.results[cell], r)
	e.Unlock()

	if sameLabel {
		// The VM ran its one job and is gone; nothing may take the second.
		time.Sleep(60 * time.Second)
		jobs := e.jobs(t, second)
		if len(jobs) != 1 || jobs[0].Status != "queued" || jobs[0].RunnerName != "" {
			t.Errorf("a second job for %s was picked up: %+v", label, jobs)
		} else {
			t.Logf("SAME-LABEL second run %d still queued %s after the first job completed", second, time.Since(dispatched.Add(done)).Round(time.Second))
		}
	}
}

// checkUserData reads the instance's user data back from EC2: it must carry
// the JIT configuration and nothing that could register another runner.
func (e *e2e) checkUserData(t *testing.T, c *Client, id, config string) {
	var out struct {
		Value string `xml:"userData>value"`
	}
	if err := c.call(context.Background(), "DescribeInstanceAttribute", url.Values{"InstanceId": {id}, "Attribute": {"userData"}}, &out); err != nil {
		t.Errorf("read the user data: %v", err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(out.Value)
	if err != nil {
		t.Errorf("user data is not base64: %v", err)
		return
	}
	text := string(data)
	if !strings.Contains(text, config) {
		t.Error("the user data lacks the JIT configuration")
	}
	if strings.Contains(text, e.token) || regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_`).MatchString(text) {
		t.Error("the user data carries a GitHub token")
	}
	var jit map[string]json.RawMessage
	raw, _ := base64.StdEncoding.DecodeString(config)
	_ = json.Unmarshal(raw, &jit)
	t.Logf("USERDATA %s: %d bytes, carries the JIT configuration (files %v), no GitHub token", id, len(data), slices.Sorted(maps.Keys(jit)))
}

// awaitGone waits for the instance to terminate and returns when it saw that.
func awaitGone(t *testing.T, c *Client, id string, limit time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		i, err := c.State(context.Background(), id)
		if errors.Is(err, vm.ErrNotFound) || err == nil && (i.State == "terminated" || i.State == "shutting-down") {
			return time.Now()
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s did not terminate after its job", id)
	return time.Time{}
}

type ghJob struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	RunnerName  string `json:"runner_name"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

func (e *e2e) api(method, path string, body, out any) (int, error) {
	var in io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "https://api.github.com"+path, in)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 && res.StatusCode != 404 {
		return res.StatusCode, fmt.Errorf("%s %s: %d", method, path, res.StatusCode)
	}
	if out != nil && len(b) > 0 && res.StatusCode < 300 {
		err = json.Unmarshal(b, out)
	}
	return res.StatusCode, err
}

// dispatch queues the workflow for label and returns its run ID.
func (e *e2e) dispatch(t *testing.T, label string) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"workflow_run_id"`
	}
	path := fmt.Sprintf("/repos/%s/actions/workflows/%s/dispatches", e.repo, e.workflow)
	if _, err := e.api(http.MethodPost, path, map[string]any{"ref": "main", "inputs": map[string]string{"label": label}, "return_run_details": true}, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == 0 {
		t.Fatal("GitHub returned no run for the dispatch")
	}
	return out.ID
}

func (e *e2e) jobs(t *testing.T, run int64) []ghJob {
	var out struct {
		Jobs []ghJob `json:"jobs"`
	}
	if _, err := e.api(http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d/jobs", e.repo, run), nil, &out); err != nil {
		t.Log(err)
	}
	return out.Jobs
}

// awaitJob polls the run's only job every two seconds.
func (e *e2e) awaitJob(t *testing.T, run int64, ok func(ghJob) bool, limit time.Duration) ghJob {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if jobs := e.jobs(t, run); len(jobs) == 1 && ok(jobs[0]) {
			return jobs[0]
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("run %d: the job never reached the expected state: %+v", run, e.jobs(t, run))
	return ghJob{}
}

func (e *e2e) awaitUnregistered(t *testing.T, id int64, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		status, err := e.api(http.MethodGet, fmt.Sprintf("/repos/%s/actions/runners/%d", e.repo, id), nil, nil)
		if status == 404 {
			t.Logf("RUNNER %d unregistered", id)
			return
		}
		if err != nil {
			t.Log(err)
		}
		time.Sleep(3 * time.Second)
	}
	t.Errorf("runner %d is still registered", id)
}

// cancel stops a run that has not finished; finished runs refuse with 409.
func (e *e2e) cancel(run int64) {
	if run != 0 {
		_, _ = e.api(http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", e.repo, run), nil, nil)
	}
}

func (e *e2e) summarize(t *testing.T) {
	e.Lock()
	defer e.Unlock()
	stat := func(runs []e2eTiming, pick func(e2eTiming) time.Duration) string {
		d := make([]time.Duration, len(runs))
		for i, r := range runs {
			d[i] = pick(r)
		}
		slices.Sort(d)
		median := (d[(len(d)-1)/2] + d[len(d)/2]) / 2
		return fmt.Sprintf("%5.1f [%5.1f-%5.1f]", median.Seconds(), d[0].Seconds(), d[len(d)-1].Seconds())
	}
	for _, cell := range slices.Sorted(maps.Keys(e.results)) {
		runs := e.results[cell]
		t.Logf("SUMMARY %-16s n=%d pickup %s done %s launch-to-terminated %s (median [min-max] s)", cell, len(runs),
			stat(runs, func(r e2eTiming) time.Duration { return r.pickup }),
			stat(runs, func(r e2eTiming) time.Duration { return r.done }),
			stat(runs, func(r e2eTiming) time.Duration { return r.gone }))
	}
}
