package vm

import (
	"strings"
	"testing"
)

func TestRunnerUserData(t *testing.T) {
	config := strings.Repeat("QUJD", 600) + "=="
	script, err := RunnerUserData("arm64", config, 75)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#!/bin/bash\n",
		"trap 'shutdown -h now' EXIT",
		"apt-get install -y -q docker.io",
		"--groups docker runner",
		"releases/download/v2.337.0/actions-runner-linux-arm64-2.337.0.tar.gz",
		"echo '9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393  runner.tar.gz' | sha256sum --check --strict",
		"<<'HAKOPOD_JIT'\n" + config + "\nHAKOPOD_JIT\n",
		"timeout --kill-after=1m 75m sudo -u runner -H ./run.sh --jitconfig",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
	amd, err := RunnerUserData("amd64", config, 75)
	if err != nil || !strings.Contains(amd, "actions-runner-linux-x64-2.337.0.tar.gz") || !strings.Contains(amd, "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613") {
		t.Fatal("amd64 runner", err)
	}
	for name, input := range map[string][3]any{
		"arch":      {"riscv64", config, int64(60)},
		"injection": {"amd64", "abc\nHAKOPOD_JIT\nrm -rf /", int64(60)},
		"size":      {"amd64", strings.Repeat("A", 12001), int64(60)},
		"minutes":   {"amd64", config, int64(1)},
	} {
		if _, err := RunnerUserData(input[0].(string), input[1].(string), input[2].(int64)); err == nil {
			t.Fatal(name, "accepted")
		}
	}
	for arch, sum := range runnerSHA256 {
		if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
			t.Fatal("runner checksum is not a SHA-256", arch)
		}
	}
}
