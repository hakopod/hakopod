package aws

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

// TestAcceptanceSpotInstance launches one t4g.nano spot instance in a real
// account, which powers itself off at boot. actions-vm-acceptance.yml runs it
// with GitHub OIDC credentials; see "Acceptance in a real account" in
// docs/managed-actions.md.
func TestAcceptanceSpotInstance(t *testing.T) {
	if os.Getenv("HAKOPOD_AWS_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AWS_ACCEPTANCE=1 to launch an instance in a real AWS account")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := make([]byte, 4)
	rand.Read(suffix)
	c, err := New(ctx, Config{
		Installation:     "acceptance-" + hex.EncodeToString(suffix),
		Region:           os.Getenv("AWS_REGION"),
		RoleARN:          os.Getenv("HAKOPOD_ACTIONS_VM_ROLE_ARN"),
		ExternalID:       os.Getenv("HAKOPOD_ACTIONS_VM_EXTERNAL_ID"),
		SubnetIDs:        []string{os.Getenv("HAKOPOD_ACTIONS_VM_SUBNET")},
		SecurityGroupIDs: []string{os.Getenv("HAKOPOD_ACTIONS_VM_SECURITY_GROUP")},
		Architecture:     "arm64",
		DiskGiB:          8,
	})
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := c.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("check warnings:", warnings)

	launched, err := c.Launch(ctx, vm.LaunchInput{Name: "acceptance", Tags: map[string]string{vm.TagSlot: "acceptance"},
		InstanceTypes: []string{"t4g.nano", "t4g.micro"}, Spot: true, UserData: "#!/bin/sh\npoweroff\n"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background(), launched.ID); err != nil {
			t.Errorf("cleanup: %v; terminate %s by hand", err, launched.ID)
		}
	})
	t.Logf("launched %s (%s, %s)", launched.ID, launched.Type, launched.Lifecycle)

	// DescribeInstances is eventually consistent right after a launch.
	var seen vm.Instance
	for seen.ID == "" {
		seen, err = c.State(ctx, launched.ID)
		if err != nil && !errors.Is(err, vm.ErrNotFound) {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal("the instance never appeared")
		}
		time.Sleep(3 * time.Second)
	}
	if seen.Tags[vm.TagInstallation] != c.config.Installation || seen.Lifecycle != "spot" || seen.LaunchedAt.IsZero() {
		t.Fatalf("observed %+v", seen)
	}
	listed, err := c.List(ctx, map[string]string{vm.TagSlot: "acceptance"})
	if err != nil || !slices.ContainsFunc(listed, func(i vm.Instance) bool { return i.ID == launched.ID }) {
		t.Fatal("not listed by its tags", listed, err)
	}

	other := *c
	other.config.Installation = "someone-else"
	if err := other.Terminate(ctx, launched.ID); err == nil {
		t.Fatal("terminated an instance tagged for another installation")
	}

	if err := c.Terminate(ctx, launched.ID); err != nil {
		t.Fatal(err)
	}
	for !seen.Gone() {
		if ctx.Err() != nil {
			t.Fatalf("still %s", seen.State)
		}
		time.Sleep(3 * time.Second)
		if seen, err = c.State(ctx, launched.ID); errors.Is(err, vm.ErrNotFound) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%s is %s (%s)", launched.ID, seen.State, seen.StateReason)
}

// acceptanceClient is a capacity client for a fresh acceptance-... installation
// on HAKOPOD_ACCEPTANCE_ARCH, arm64 unless set to amd64.
func acceptanceClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("HAKOPOD_AWS_ACCEPTANCE") != "1" {
		t.Skip("set HAKOPOD_AWS_ACCEPTANCE=1 to launch instances in a real AWS account")
	}
	suffix := make([]byte, 4)
	rand.Read(suffix)
	c, err := New(context.Background(), Config{
		Installation:     "acceptance-" + hex.EncodeToString(suffix),
		Region:           os.Getenv("AWS_REGION"),
		RoleARN:          os.Getenv("HAKOPOD_ACTIONS_VM_ROLE_ARN"),
		ExternalID:       os.Getenv("HAKOPOD_ACTIONS_VM_EXTERNAL_ID"),
		SubnetIDs:        []string{os.Getenv("HAKOPOD_ACTIONS_VM_SUBNET")},
		SecurityGroupIDs: []string{os.Getenv("HAKOPOD_ACTIONS_VM_SECURITY_GROUP")},
		Architecture:     cmp.Or(os.Getenv("HAKOPOD_ACCEPTANCE_ARCH"), "arm64"),
		DiskGiB:          30, // RunsOn's images need 30
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// probeScript is the runner script with run.sh replaced by a check that
// Docker answers and the runner starts, then a small first job: an image
// pull, a shallow clone and a read of the runner's own files, which shows the
// cost of a volume still loading from its snapshot. No GitHub runner is
// registered. The nonce proves which user data ran on this boot. The
// instance stays up two minutes before it powers off: the latest console
// lags and goes blank once the instance shuts down.
func probeScript(t *testing.T, arch, image, nonce string) string {
	t.Helper()
	script, err := vm.RunnerUserData(image, arch, base64.StdEncoding.EncodeToString([]byte("acceptance-not-a-jit-config")), 10)
	if err != nil {
		t.Fatal(err)
	}
	run := `timeout --kill-after=1m 10m sudo -u runner -H ./run.sh --jitconfig "$(cat .jitconfig)"`
	probe := `say probe ` + nonce + ` docker=$(docker info --format '{{.ServerVersion}}') runner=$(sudo -u runner -H ./config.sh --version) dir=$dir
exec 2>/dev/console
trap 'say work failed at line $LINENO; sleep 30; shutdown -h now' ERR
docker run --rm public.ecr.aws/docker/library/alpine:3.20 true
git clone -q --depth 1 https://github.com/actions/checkout /tmp/checkout
say work ` + nonce + ` read=$( (tar cf - --warning=none "$dir/bin" "$dir/externals" || true) | wc -c)
systemd-analyze blame --no-pager | head -6 | sed 's/^/hakopod-blame: /' >/dev/console; sleep 120`
	if !strings.Contains(script, run) {
		t.Fatal("the runner command changed; update the probe")
	}
	return strings.Replace(script, run, probe, 1)
}

// slots keeps at most eight acceptance instances in the account at once.
var slots = make(chan struct{}, 8)

// slot waits for room for one more instance, held until the test ends, after
// its instance is terminated. Take it before starting a clock.
func slot(t *testing.T) {
	slots <- struct{}{}
	t.Cleanup(func() { <-slots })
}

// launchTagged launches one small on-demand instance and terminates it when
// the test ends.
func launchTagged(t *testing.T, c *Client, in vm.LaunchInput) vm.Instance {
	t.Helper()
	in.InstanceTypes = []string{"t4g.small"}
	if c.config.Architecture == "amd64" {
		in.InstanceTypes = []string{"t3a.small"}
	}
	in.Tags = map[string]string{vm.TagSlot: "acceptance", vm.TagCapacity: "acceptance"}
	i, err := c.Launch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Terminate(context.Background(), i.ID); err != nil {
			t.Errorf("cleanup: %v; terminate %s by hand", err, i.ID)
		}
	})
	return i
}

// awaitConsole polls the console until it shows want, and returns the line.
func awaitConsole(t *testing.T, c *Client, id, want string, limit time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(limit)
	seen, failed := "", error(nil)
	for time.Now().Before(deadline) {
		out, err := c.ConsoleOutput(context.Background(), id)
		if err == nil && !strings.Contains(out, want) {
			// The latest console sometimes stops updating; the saved one
			// catches up once the instance powers off.
			out, err = savedConsole(c, id)
		}
		if err != nil {
			failed = err
		}
		if err == nil && out != "" {
			seen = out
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, want) {
					return strings.TrimSpace(line)
				}
			}
		}
		// GetConsoleOutput is throttled per account; the times come from
		// the instance clock, so polling slowly costs no precision.
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("%s never printed %q (last error: %v); console tail:\n%s", id, want, failed, tail(seen, 3000))
	return ""
}

func savedConsole(c *Client, id string) (string, error) {
	var out struct {
		Output string `xml:"output"`
	}
	if err := c.call(context.Background(), "GetConsoleOutput", url.Values{"InstanceId": {id}}, &out); err != nil {
		return "", err
	}
	text, err := base64.StdEncoding.DecodeString(out.Output)
	return string(text), err
}

// since reads the instance clock from a console line ("at <epoch>") and
// returns how long after start it was printed, free of console delay.
func since(t *testing.T, line string, start time.Time) time.Duration {
	t.Helper()
	at := line[strings.LastIndex(line, " at ")+4:]
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(at, ")"), 64)
	if !strings.Contains(line, " at ") || err != nil {
		t.Fatalf("no time in %q", line)
	}
	return time.Unix(0, int64(seconds*1e9)).Sub(start).Round(100 * time.Millisecond)
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func awaitState(t *testing.T, c *Client, id, want string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if i, err := c.State(context.Background(), id); err == nil && i.State == want {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%s never became %s", id, want)
}

// timing is one run: from the launch or claim call to the runner start, to
// a working Docker and runner, and to the end of the probe's first job.
type timing struct{ runner, docker, work time.Duration }

var results struct {
	sync.Mutex
	cells map[string][]timing
}

func record(t *testing.T, cell string, r timing) {
	results.Lock()
	if results.cells == nil {
		results.cells = map[string][]timing{}
	}
	results.cells[cell] = append(results.cells[cell], r)
	results.Unlock()
	t.Logf("RESULT %s: runner %s, docker %s, work %s", cell, r.runner, r.docker, r.work)
}

// summarize logs the median, min and max of every cell.
func summarize(t *testing.T) {
	results.Lock()
	defer results.Unlock()
	stat := func(runs []timing, pick func(timing) time.Duration) string {
		d := make([]time.Duration, len(runs))
		for i, r := range runs {
			d[i] = pick(r)
		}
		slices.Sort(d)
		median := (d[(len(d)-1)/2] + d[len(d)/2]) / 2
		return fmt.Sprintf("%5.1f [%5.1f-%5.1f]", median.Seconds(), d[0].Seconds(), d[len(d)-1].Seconds())
	}
	for _, cell := range slices.Sorted(maps.Keys(results.cells)) {
		runs := results.cells[cell]
		t.Logf("SUMMARY %-36s n=%d runner %s docker %s work %s (median [min-max] s)", cell, len(runs),
			stat(runs, func(r timing) time.Duration { return r.runner }),
			stat(runs, func(r timing) time.Duration { return r.docker }),
			stat(runs, func(r timing) time.Duration { return r.work }))
	}
}

// coldStart launches from image, initializing the root volume at rate MiB/s
// when rate is set, and times the run.
func coldStart(t *testing.T, c *Client, setting, image string, rate int64) timing {
	slot(t)
	start := time.Now()
	i := launchTagged(t, c, vm.LaunchInput{Name: "acceptance-cold", Image: image, VolumeInitRate: rate, UserData: probeScript(t, c.config.Architecture, setting, "cold")})
	ready := awaitConsole(t, c, i.ID, "hakopod: starting the runner", 10*time.Minute)
	probe := awaitConsole(t, c, i.ID, "hakopod: probe cold", 3*time.Minute)
	work := awaitConsole(t, c, i.ID, "hakopod: work cold", 5*time.Minute)
	t.Logf("COLD %s %s: %s; %s; %s", setting, i.ID, ready, probe, work)
	logBlame(t, c, i.ID)
	awaitState(t, c, i.ID, "terminated", 5*time.Minute)
	return timing{since(t, ready, start), since(t, probe, start), since(t, work, start)}
}

// logBlame logs the slowest units of the boot, which the probe printed.
func logBlame(t *testing.T, c *Client, id string) {
	awaitConsole(t, c, id, "hakopod-blame:", time.Minute)
	out, _ := c.ConsoleOutput(context.Background(), id)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "hakopod-blame:") {
			t.Log(strings.TrimSpace(line))
		}
	}
}

// warmStart prepares a standby from image, claims it with new user data and
// times the run from the claim.
func warmStart(t *testing.T, c *Client, setting, image string) timing {
	slot(t)
	start := time.Now()
	standby, err := vm.RunnerUserData(setting, c.config.Architecture, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	i := launchTagged(t, c, vm.LaunchInput{Name: "acceptance-standby", Image: image, UserData: standby, StopOnShutdown: true})
	awaitState(t, c, i.ID, "stopped", 15*time.Minute)
	t.Logf("WARM %s: standby %s prepared and stopped after %s", setting, i.ID, time.Since(start).Round(time.Second))
	nonce := hex.EncodeToString([]byte(time.Now().Format("150405")))
	start = time.Now()
	if err = c.Claim(context.Background(), i.ID, probeScript(t, c.config.Architecture, setting, nonce), map[string]string{vm.TagPoolState: "claimed", vm.TagSlot: "acceptance-claimed"}); err != nil {
		t.Fatal(err)
	}
	claimed := time.Since(start)
	ready := awaitConsole(t, c, i.ID, "hakopod: starting the runner", 5*time.Minute)
	probe := awaitConsole(t, c, i.ID, "hakopod: probe "+nonce, 3*time.Minute)
	work := awaitConsole(t, c, i.ID, "hakopod: work "+nonce, 5*time.Minute)
	t.Logf("WARM %s %s: claim calls took %s; %s; %s; %s", setting, i.ID, claimed.Round(time.Millisecond), ready, probe, work)
	// The claim switched the instance to terminate on shutdown.
	awaitState(t, c, i.ID, "terminated", 5*time.Minute)
	if got, _ := c.State(context.Background(), i.ID); got.Tags[vm.TagSlot] != "acceptance-claimed" {
		t.Fatal("claim tags missing", got.Tags)
	}
	return timing{since(t, ready, start), since(t, probe, start), since(t, work, start)}
}

// TestAcceptanceRunnerImages measures runner start and a first job on stock
// Ubuntu, RunsOn's full image cold and from a standby, and a hakopod-minimal
// image built here, cold, cold with an initialization rate on its volume, and
// from a standby. HAKOPOD_ACCEPTANCE_RUNS repeats every cell (default 1):
// cold runs one after another, standbys side by side. Pick cells with -run,
// for example -run 'TestAcceptanceRunnerImages/(runs-on|hakopod-minimal)'.
// On-demand t4g.small, or t3a.small with HAKOPOD_ACCEPTANCE_ARCH=amd64.
func TestAcceptanceRunnerImages(t *testing.T) {
	c := acceptanceClient(t)
	arch := c.config.Architecture
	runs, err := strconv.Atoi(cmp.Or(os.Getenv("HAKOPOD_ACCEPTANCE_RUNS"), "1"))
	if err != nil || runs < 1 {
		t.Fatal("HAKOPOD_ACCEPTANCE_RUNS is not a positive number")
	}
	t.Logf("installation %s, %s, %d runs per cell", c.config.Installation, arch, runs)
	t.Cleanup(func() { summarize(t) })
	cold := func(t *testing.T, cell, setting, image string, rate int64) {
		t.Parallel()
		for n := range runs {
			t.Run(strconv.Itoa(n+1), func(t *testing.T) { record(t, arch+" "+cell, coldStart(t, c, setting, image, rate)) })
		}
	}
	warm := func(t *testing.T, cell, setting, image string) {
		t.Parallel()
		for n := range runs {
			t.Run(strconv.Itoa(n+1), func(t *testing.T) {
				t.Parallel()
				record(t, arch+" "+cell, warmStart(t, c, setting, image))
			})
		}
	}
	runsOn, err := c.Image(context.Background(), vm.ImageRunsOnFull)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("runs-on-full resolves to %s", runsOn)
	t.Run("stock", func(t *testing.T) { cold(t, "stock cold", "", "", 0) })
	t.Run("runs-on-cold", func(t *testing.T) { cold(t, "runs-on-full cold", vm.ImageRunsOnFull, runsOn, 0) })
	t.Run("runs-on-warm", func(t *testing.T) { warm(t, "runs-on-full warm", vm.ImageRunsOnFull, runsOn) })
	t.Run("hakopod-minimal", func(t *testing.T) {
		t.Parallel()
		image := buildMinimal(t, c)
		t.Run("cold", func(t *testing.T) { cold(t, "hakopod-minimal cold", vm.ImageHakopodMinimal, image, 0) })
		t.Run("cold-init-300", func(t *testing.T) {
			cold(t, "hakopod-minimal cold, init 300 MiB/s", vm.ImageHakopodMinimal, image, 300)
		})
		t.Run("warm", func(t *testing.T) { warm(t, "hakopod-minimal warm", vm.ImageHakopodMinimal, image) })
	})
}

func buildMinimal(t *testing.T, c *Client) string {
	ctx := context.Background()
	slot(t)
	start := time.Now()
	script, err := vm.ImageUserData(c.config.Architecture)
	if err != nil {
		t.Fatal(err)
	}
	builder := launchTagged(t, c, vm.LaunchInput{Name: "acceptance-image", UserData: script, StopOnShutdown: true, DiskGiB: 8})
	awaitState(t, c, builder.ID, "stopped", 20*time.Minute)
	out, _ := c.ConsoleOutput(ctx, builder.ID)
	t.Logf("builder %s stopped after %s; ready marker on the console of the stopped builder: %v", builder.ID, time.Since(start).Round(time.Second), strings.Contains(out, vm.ImageReady))
	image, err := c.CreateImage(ctx, builder.ID, "hakopod-acceptance-"+c.config.Installation, map[string]string{vm.TagCapacity: "acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.DeleteImage(context.Background(), image); err != nil {
			t.Errorf("cleanup: %v; deregister %s by hand", err, image)
		}
		if left, err := c.Images(context.Background(), nil); err != nil || len(left) != 0 {
			t.Errorf("images left behind: %v %v", left, err)
		}
	})
	for {
		images, err := c.Images(ctx, map[string]string{vm.TagCapacity: "acceptance"})
		if err != nil {
			t.Fatal(err)
		}
		if len(images) == 1 && images[0].State == "available" {
			break
		}
		if len(images) == 1 && images[0].State == "failed" || time.Since(start) > 40*time.Minute {
			t.Fatal("image never became available", images)
		}
		time.Sleep(10 * time.Second)
	}
	t.Logf("image %s available %s after the build started", image, time.Since(start).Round(time.Second))
	return image
}
