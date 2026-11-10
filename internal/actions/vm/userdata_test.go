package vm

import (
	"os/exec"
	"strings"
	"testing"
)

// bashSyntax parses a script without running it.
func bashSyntax(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v %s", err, out)
	}
}

func TestRunnerUserData(t *testing.T) {
	config := strings.Repeat("QUJD", 600) + "=="
	script, err := RunnerUserData(ImageHakopodMinimal, "arm64", config, 75)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#!/bin/bash\n",
		"trap 'shutdown -h now' EXIT",
		"if ! command -v docker >/dev/null; then",
		"apt-get install -y -q docker.io",
		"usermod -aG docker runner",
		"if [ -x /home/runner/run.sh ]; then dir=/home/runner; fi",
		"releases/download/v2.337.0/actions-runner-linux-arm64-2.337.0.tar.gz",
		"echo '9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393  runner.tar.gz' | sha256sum --check --strict",
		"<<'HAKOPOD_JIT'\n" + config + "\nHAKOPOD_JIT\n",
		"timeout --kill-after=1m 75m sudo -u runner -H ./run.sh --jitconfig",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
	bashSyntax(t, script)
	// Azure is recognised before EC2's IMDS is asked; EC2 keeps its notice.
	azure := strings.Index(script, "Microsoft Corporation")
	if azure < 0 || azure > strings.Index(script, "instance-life-cycle") || !strings.Contains(script, "chassis_asset_tag") ||
		!strings.Contains(script, "scheduledevents?api-version=2020-07-01") || strings.Count(script, "pkill -INT -f Runner.Listener") != 2 {
		t.Fatal("spot watch")
	}
	preempted := script[strings.Index(script, "python3 -c '")+len("python3 -c '"):]
	preempted = preempted[:strings.Index(preempted, "' \"$name\"")]
	for events, want := range map[string]bool{
		`{"Events":[{"EventType":"Preempt","Resources":["hakopod-a"]}]}`:                                              true,
		`{"Events":[{"EventType":"Preempt","Resources":["other"]},{"EventType":"Reboot","Resources":["hakopod-a"]}]}`: false,
		`{"DocumentIncarnation":0,"Events":[]}`:                                                                       false,
	} {
		cmd := exec.Command("python3", "-c", preempted, "hakopod-a")
		cmd.Stdin = strings.NewReader(events)
		if err := cmd.Run(); (err == nil) != want {
			t.Fatal(events, err)
		}
	}
	if strings.Contains(script, "multipart") || strings.Contains(script, "standby ready") {
		t.Fatal("hakopod-minimal user data is the plain job script")
	}
	amd, err := RunnerUserData("ami-0123456789abcdef0", "amd64", config, 75)
	if err != nil || !strings.Contains(amd, "actions-runner-linux-x64-2.337.0.tar.gz") || !strings.Contains(amd, "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613") {
		t.Fatal("amd64 runner", err)
	}
	// Other images run it through cloud-init on every boot, so a claimed
	// standby runs its new user data.
	if !strings.HasPrefix(amd, "Content-Type: multipart/mixed") || !strings.Contains(amd, "- [scripts-user, always]") || !strings.HasSuffix(amd, "\n--HAKOPOD--\n") {
		t.Fatal("not wrapped for every boot:", amd[:200])
	}
	standby, err := RunnerUserData(ImageRunsOnFull, "arm64", "", 0)
	if err != nil || !strings.Contains(standby, "standby ready") || strings.Contains(standby, "run.sh --jitconfig") {
		t.Fatal("standby script", err, standby)
	}
	bashSyntax(t, standby[strings.Index(standby, "#!/bin/bash"):strings.LastIndex(standby, "--HAKOPOD--")])
	for name, input := range map[string][3]any{
		"arch":      {"riscv64", config, int64(60)},
		"injection": {"amd64", "abc\nHAKOPOD_JIT\nrm -rf /", int64(60)},
		"size":      {"amd64", strings.Repeat("A", 12001), int64(60)},
		"minutes":   {"amd64", config, int64(1)},
	} {
		if _, err := RunnerUserData(ImageRunsOnFull, input[0].(string), input[1].(string), input[2].(int64)); err == nil {
			t.Fatal(name, "accepted")
		}
	}
	for arch, sum := range runnerSHA256 {
		if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
			t.Fatal("runner checksum is not a SHA-256", arch)
		}
	}
}

func TestImageUserData(t *testing.T) {
	script, err := ImageUserData("amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"actions-runner-linux-x64-2.337.0.tar.gz", "./bin/installdependencies.sh",
		"systemctl enable hakopod-runner.service docker.service",
		"http://169.254.169.254/latest/api/token", "exec /run/hakopod-user-data",
		"cloud_final_modules: [final-message]", "systemctl mask snapd.service",
		"echo '" + ImageReady + "' >/dev/console\nshutdown -h now\n",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
	bashSyntax(t, script)
	if _, err = ImageUserData("riscv64"); err == nil {
		t.Fatal("unknown architecture accepted")
	}
}
