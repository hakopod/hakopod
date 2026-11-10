package vm

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

// RunnerVersion matches the runner in the pod image (images/actions-runner).
// The checksums are the ones GitHub publishes in the v2.337.0 release notes,
// which also match the release assets' digests. Images that already ship a
// runner, such as RunsOn's, use theirs.
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

// installRunner makes any Ubuntu image a runner host: images that ship Docker
// and the runner skip the installs. RunsOn images keep the runner in
// /home/runner itself.
const installRunner = `export DEBIAN_FRONTEND=noninteractive
if ! command -v docker >/dev/null; then
  apt-get update -q
  apt-get install -y -q docker.io curl
fi
id runner >/dev/null 2>&1 || useradd --create-home --shell /bin/bash runner
usermod -aG docker runner
# The VM is single-use, as GitHub-hosted runners are, and jobs expect sudo.
echo 'runner ALL=(ALL) NOPASSWD:ALL' | install -m 0440 /dev/stdin /etc/sudoers.d/hakopod-runner
dir=/home/runner/actions-runner
if [ -x /home/runner/run.sh ]; then dir=/home/runner; fi
if [ ! -x "$dir/run.sh" ]; then
  install -d -o runner -g runner "$dir"
  cd "$dir"
  curl -fsSL --retry 5 -o runner.tar.gz https://github.com/actions/runner/releases/download/v{{.Version}}/actions-runner-linux-{{.Arch}}-{{.Version}}.tar.gz
  echo '{{.SHA256}}  runner.tar.gz' | sha256sum --check --strict
  tar xzf runner.tar.gz
  rm runner.tar.gz
  ./bin/installdependencies.sh
  chown -R runner:runner "$dir"
fi
cd "$dir"
`

// The script runs as root on every boot. Any failure powers the VM off; a
// runner terminates on shutdown, a standby stops until a job claims it.
var runnerScript = template.Must(template.New("user-data").Parse(`#!/bin/bash
# Hakopod single-use GitHub Actions runner: one job, then power off.
set -euo pipefail
trap 'shutdown -h now' EXIT
say() { echo "hakopod: $* (up $(cut -d' ' -f1 /proc/uptime)s at $(date +%s.%N))" >/dev/console 2>&1 || true; }
` + installRunner + `{{if .Config -}}
systemctl --no-block start docker
install -m 0600 -o runner -g runner /dev/null .jitconfig
cat > .jitconfig <<'HAKOPOD_JIT'
{{.Config}}
HAKOPOD_JIT
# A spot VM gets two minutes' notice. The runner is stopped half a minute
# before the end, so GitHub fails the job then instead of waiting to miss the
# runner, and the rerun starts sooner.
spot_watch() {
  trap - EXIT
  set +e
  local a at
  imds() {
    local t
    t=$(curl -fsS -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token) &&
      curl -fsS -H "X-aws-ec2-metadata-token: $t" "http://169.254.169.254/latest/meta-data/$1"
  }
  [ "$(imds instance-life-cycle)" = spot ] || return
  until a=$(imds spot/instance-action 2>/dev/null); do sleep 5; done
  at=$(date -d "$(sed -n 's/.*"time" *: *"\([^"]*\)".*/\1/p' <<<"$a")" +%s) || at=$(($(date +%s) + 120))
  say "spot interruption notice for $(date -u -d "@$at" +%TZ)"
  sleep $((at - 30 - $(date +%s) > 0 ? at - 30 - $(date +%s) : 0))
  say stopping the runner before the spot interruption
  pkill -INT -f Runner.Listener
}
spot_watch &
say starting the runner
timeout --kill-after=1m {{.Minutes}}m sudo -u runner -H ./run.sh --jitconfig "$(cat .jitconfig)"
{{- else -}}
# A standby reads what a job reads first, so its stopped volume already
# holds those blocks, then powers off until a job claims it.
find "$dir" -type f -exec cat {} + >/dev/null
docker info >/dev/null
say standby ready
{{- end}}
`))

// cloud-init runs user data scripts once per instance. A standby boots again
// with new user data, so the script is wrapped to run on every boot.
const everyBoot = "Content-Type: multipart/mixed; boundary=\"HAKOPOD\"\nMIME-Version: 1.0\n\n" +
	"--HAKOPOD\nContent-Type: text/cloud-config; charset=\"us-ascii\"\n\ncloud_final_modules:\n- [scripts-user, always]\n\n" +
	"--HAKOPOD\nContent-Type: text/x-shellscript; charset=\"us-ascii\"\n\n%s\n--HAKOPOD--\n"

// RunnerUserData renders the boot script for one job, or with an empty config
// for a standby that warms up and powers off. minutes bounds the runner on the
// VM itself, so a VM cannot outlive an unreachable control plane.
// hakopod-minimal images run the plain script from their own boot unit; on
// every other image cloud-init runs it.
func RunnerUserData(image, architecture, config string, minutes int64) (string, error) {
	data, err := runnerData(architecture)
	if err != nil {
		return "", err
	}
	if config != "" {
		if len(config) < 16 || len(config) > 12000 || !jitConfig.MatchString(config) {
			return "", fmt.Errorf("the runner configuration is not a JIT configuration")
		}
		if minutes < 5 || minutes > 600 {
			return "", fmt.Errorf("runner time limit out of range")
		}
	}
	data["Config"], data["Minutes"] = config, minutes
	var out bytes.Buffer
	if err = runnerScript.Execute(&out, data); err != nil {
		return "", err
	}
	script := out.String()
	if image != ImageHakopodMinimal {
		script = fmt.Sprintf(everyBoot, script)
	}
	if len(script) > maxUserData {
		return "", fmt.Errorf("the runner boot script exceeds the 16 KiB user data limit")
	}
	return script, nil
}

func runnerData(architecture string) (map[string]any, error) {
	sum, ok := runnerSHA256[architecture]
	if !ok || len(sum) != 64 {
		return nil, fmt.Errorf("no verified runner checksum for %q", architecture)
	}
	return map[string]any{"Version": RunnerVersion, "Arch": runnerArch[architecture], "SHA256": sum}, nil
}

// ImageReady is what an image builder prints to its console before it powers
// off for the capture.
const ImageReady = "hakopod: image ready"

// The builder runs once on stock Ubuntu. A failure leaves it running with the
// error on its console; success powers it off for the image to be taken.
var imageScript = template.Must(template.New("image").Parse(`#!/bin/bash
# Builds a hakopod-minimal Actions runner image.
set -euxo pipefail
` + installRunner + `cat > /usr/local/sbin/hakopod-boot <<'HAKOPOD'
#!/bin/bash
# Runs this boot's user data: one runner job, or a standby's warm-up.
if grep -q 'Google Compute Engine' /sys/class/dmi/id/product_name 2>/dev/null; then
  curl -fsS --retry 10 --retry-connrefused -H 'Metadata-Flavor: Google' -o /run/hakopod-user-data \
    http://169.254.169.254/computeMetadata/v1/instance/attributes/user-data || exec shutdown -h now
else
  t=$(curl -fsS --retry 10 --retry-connrefused -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 300' http://169.254.169.254/latest/api/token) &&
    curl -fsS --retry 5 -H "X-aws-ec2-metadata-token: $t" -o /run/hakopod-user-data http://169.254.169.254/latest/user-data ||
    exec shutdown -h now
fi
chmod 0700 /run/hakopod-user-data
exec /run/hakopod-user-data
HAKOPOD
chmod 0755 /usr/local/sbin/hakopod-boot
cat > /etc/systemd/system/hakopod-runner.service <<'HAKOPOD'
[Unit]
Description=Hakopod Actions runner from this boot's user data
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/sbin/hakopod-boot
StandardOutput=journal+console
StandardError=journal+console

[Install]
WantedBy=multi-user.target
HAKOPOD
systemctl enable hakopod-runner.service docker.service
# cloud-init still sets up the network and grows the disk; the unit above,
# not cloud-init, runs user data on every boot.
cat > /etc/cloud/cloud.cfg.d/90-hakopod.cfg <<'HAKOPOD'
cloud_config_modules: []
cloud_final_modules: [final-message]
HAKOPOD
systemctl mask {{.Masked}}
apt-get clean
rm -rf /var/lib/apt/lists/*
truncate -s 0 /etc/machine-id
echo '` + ImageReady + `' >/dev/console
shutdown -h now
`))

// Units a single-job runner never needs. Masking a unit an image lacks is
// harmless.
var maskedUnits = []string{"snapd.service", "snapd.socket", "snapd.seeded.service", "apt-daily.timer", "apt-daily-upgrade.timer",
	"unattended-upgrades.service", "motd-news.timer", "man-db.timer", "fwupd-refresh.timer", "ModemManager.service", "multipathd.service", "multipathd.socket"}

// ImageUserData renders the script that turns stock Ubuntu 24.04 into a
// hakopod-minimal image.
func ImageUserData(architecture string) (string, error) {
	data, err := runnerData(architecture)
	if err != nil {
		return "", err
	}
	data["Masked"] = strings.Join(maskedUnits, " ")
	var out bytes.Buffer
	err = imageScript.Execute(&out, data)
	return out.String(), err
}
