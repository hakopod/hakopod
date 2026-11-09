package vm

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"
)

// RunnerVersion matches the runner in the pod image (images/actions-runner).
// The checksums are the ones GitHub publishes in the v2.337.0 release notes,
// which also match the release assets' digests.
const RunnerVersion = "2.337.0"

var runnerSHA256 = map[string]string{
	"amd64": "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
	"arm64": "9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393",
}

var runnerArch = map[string]string{"amd64": "x64", "arm64": "arm64"}

// A JIT configuration is base64. Anything else must not reach the script.
var jitConfig = regexp.MustCompile(`^[A-Za-z0-9+/=]+$`)

// EC2 refuses user data above 16 KiB before base64 encoding.
const maxUserData = 16 << 10

// The script runs once, as root, from cloud-init. Any failure powers the VM
// off, and the instance is created to terminate on shutdown.
var userData = template.Must(template.New("user-data").Parse(`#!/bin/bash
# Hakopod single-use GitHub Actions runner: one job, then power off.
set -euo pipefail
trap 'shutdown -h now' EXIT
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q docker.io curl
useradd --create-home --shell /bin/bash --groups docker runner
install -d -o runner -g runner /home/runner/actions-runner
cd /home/runner/actions-runner
curl -fsSL --retry 5 -o runner.tar.gz https://github.com/actions/runner/releases/download/v{{.Version}}/actions-runner-linux-{{.Arch}}-{{.Version}}.tar.gz
echo '{{.SHA256}}  runner.tar.gz' | sha256sum --check --strict
tar xzf runner.tar.gz
rm runner.tar.gz
./bin/installdependencies.sh
chown -R runner:runner /home/runner/actions-runner
install -m 0600 -o runner -g runner /dev/null .jitconfig
cat > .jitconfig <<'HAKOPOD_JIT'
{{.Config}}
HAKOPOD_JIT
timeout --kill-after=1m {{.Minutes}}m sudo -u runner -H ./run.sh --jitconfig "$(cat .jitconfig)"
`))

// RunnerUserData renders the boot script for one job. minutes bounds the
// runner on the VM itself, so a VM cannot outlive an unreachable control plane.
func RunnerUserData(architecture, config string, minutes int64) (string, error) {
	sum, ok := runnerSHA256[architecture]
	if !ok || len(sum) != 64 {
		return "", fmt.Errorf("no verified runner checksum for %q", architecture)
	}
	if len(config) < 16 || len(config) > 12000 || !jitConfig.MatchString(config) {
		return "", fmt.Errorf("the runner configuration is not a JIT configuration")
	}
	if minutes < 5 || minutes > 600 {
		return "", fmt.Errorf("runner time limit out of range")
	}
	var out bytes.Buffer
	err := userData.Execute(&out, map[string]any{"Version": RunnerVersion, "Arch": runnerArch[architecture], "SHA256": sum, "Config": config, "Minutes": minutes})
	if err != nil {
		return "", err
	}
	if out.Len() > maxUserData {
		return "", fmt.Errorf("the runner boot script exceeds the 16 KiB user data limit")
	}
	return out.String(), nil
}
