package api

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// Official Docker manifest-list digests and pack release asset checksums were
// resolved on 2026-09-12; Docker manifests contain both amd64 and arm64.
const gitlabDockerCLI = "docker:28-cli@sha256:625d9431a9f54c5a2bc90f24f0e1c3d55b1349fd857dd85035f98c2c9acbdd4d"
const gitlabDockerDaemon = "docker:28-dind@sha256:2a232a42256f70d78e3cc5d2b5d6b3276710a0de0596c145f627ecfae90282ac"

func gitlabBuildRequestID(build string, pipeline int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("gitlab:%s:%d", build, pipeline)))
	return fmt.Sprintf("%x", sum[:16])
}
func (c buildConfig) gitlabJobName() string { return "hakopod_build_" + c.ID }
func gitlabBuildWorkflow(c buildConfig) string {
	runner := "saas-linux-small-amd64"
	packAsset := "pack-v0.40.9-linux.tgz"
	packChecksum := "dc0ee1e931cf8a106d7555a01a214864f9acb60b77adf15d69b74df4404758e9"
	if c.Architecture == "arm64" {
		runner = "saas-linux-small-arm64"
		packAsset = "pack-v0.40.9-linux-arm64.tgz"
		packChecksum = "091ccb213823656c727731537ef8f1000eb4dc3ec61641506653e7f9d6da0c5e"
	}
	rules := "    - if: " + strconv.Quote(`$CI_PIPELINE_SOURCE == "api" && $HAKOPOD_BUILD_ID == "`+c.ID+`"`) + "\n"
	if c.AutoBuild {
		rules += "    - if: " + strconv.Quote(`$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == `+strconv.Quote(c.Branch)) + "\n"
	}
	rules += "    - when: never\n"
	script := `set -eu
BUILD_ID={{BUILD_ID}}
IMAGE_NAME={{IMAGE}}
BUILD_CONTEXT={{CONTEXT}}
if [ "$CI_PIPELINE_SOURCE" = push ]; then
  SOURCE_SHA="$CI_COMMIT_SHA"
  REQUEST_ID="$(printf 'gitlab:%s:%s' "$BUILD_ID" "$CI_PIPELINE_ID" | sha256sum | cut -c1-32)"
else
  SOURCE_SHA="$HAKOPOD_SOURCE_SHA"
  REQUEST_ID="$HAKOPOD_REQUEST_ID"
fi
case "$SOURCE_SHA" in ''|*[!0-9a-f]*) exit 1;; esac
[ "${#SOURCE_SHA}" -eq 40 ] || [ "${#SOURCE_SHA}" -eq 64 ]
case "$REQUEST_ID" in ''|*[!0-9a-f]*) exit 1;; esac
[ "${#REQUEST_ID}" -eq 32 ]
git fetch --no-tags origin "$SOURCE_SHA"
git checkout --detach "$SOURCE_SHA"
git remote set-url origin {{REPOSITORY_URL}}
attempt=0
until docker info >/dev/null 2>&1; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 30 ] || exit 1
  sleep 2
done
printf '%s' "$CI_JOB_TOKEN" | docker login registry.gitlab.com --username gitlab-ci-token --password-stdin
`
	if c.Mode == "buildpacks" {
		script += `wget -q -O /tmp/hakopod-pack.tgz {{PACK_URL}}
printf '%s  %s\n' {{PACK_CHECKSUM}} /tmp/hakopod-pack.tgz | sha256sum -c -
tar -xzf /tmp/hakopod-pack.tgz -C /tmp pack
`
		setup := buildpackSetup(c.Preset)
		for _, line := range strings.Split(strings.TrimSuffix(setup, "\n"), "\n") {
			script += strings.TrimPrefix(line, "          ") + "\n"
		}
		script = strings.ReplaceAll(script, `compgen -G "$BUILD_CONTEXT/*.csproj" > /dev/null || compgen -G "$BUILD_CONTEXT/*.fsproj" > /dev/null`, `[ -n "$(find "$BUILD_CONTEXT" -maxdepth 1 \( -name '*.csproj' -o -name '*.fsproj' \) -print -quit)" ]`)
		script += `/tmp/pack build "$IMAGE_NAME:$REQUEST_ID" --path "$BUILD_CONTEXT" --builder {{BUILDER}} --buildpack "$PACK_IMAGE" --env BP_WEB_SERVER=nginx --publish --pull-policy if-not-present
`
	} else {
		script += `docker buildx create --driver docker-container --use
docker buildx build --platform {{PLATFORM}} --file {{DOCKERFILE}} --tag "$IMAGE_NAME:$REQUEST_ID" --provenance=mode=min --push "$BUILD_CONTEXT"
`
	}
	script += `docker buildx imagetools inspect "$IMAGE_NAME:$REQUEST_ID" > /tmp/hakopod-image.txt
DIGEST="$(awk '$1 == "Digest:" { print $2; exit }' /tmp/hakopod-image.txt)"
printf '%s\n' "$DIGEST" | grep -Eq '^sha256:[0-9a-f]{64}$'
printf '{"build_id":"%s","request_id":"%s","commit_sha":"%s","image":"%s@%s"}\n' "$BUILD_ID" "$REQUEST_ID" "$SOURCE_SHA" "$IMAGE_NAME" "$DIGEST" > result.json
`
	script = strings.NewReplacer("{{BUILD_ID}}", strconv.Quote(c.ID), "{{IMAGE}}", strconv.Quote(c.imageName()), "{{CONTEXT}}", strconv.Quote(c.ContextPath), "{{REPOSITORY_URL}}", strconv.Quote("https://gitlab.com/"+c.Repository+".git"), "{{PACK_URL}}", strconv.Quote("https://github.com/buildpacks/pack/releases/download/v0.40.9/"+packAsset), "{{PACK_CHECKSUM}}", strconv.Quote(packChecksum), "{{BUILDER}}", strconv.Quote(paketoBuilder), "{{PLATFORM}}", strconv.Quote("linux/"+c.Architecture), "{{DOCKERFILE}}", strconv.Quote(c.Dockerfile)).Replace(script)
	var out strings.Builder
	out.WriteString("# Managed by Hakopod build " + c.ID + ". Review through Hakopod before reinstalling.\nworkflow:\n  rules:\n" + rules + "stages: [build]\n" + c.gitlabJobName() + ":\n  stage: build\n  image: " + strconv.Quote(gitlabDockerCLI) + "\n  tags: [" + strconv.Quote(runner) + "]\n  timeout: 30m\n  interruptible: false\n  retry: 0\n  resource_group: " + strconv.Quote("hakopod-"+c.ID) + "\n  services:\n    - name: " + strconv.Quote(gitlabDockerDaemon) + "\n      alias: docker\n      command: [\"--tls=false\", \"--mtu=1400\"]\n  variables:\n    DOCKER_HOST: \"tcp://docker:2375\"\n    DOCKER_TLS_CERTDIR: \"\"\n    GIT_DEPTH: \"1\"\n  script:\n    - |\n")
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		out.WriteString("      " + line + "\n")
	}
	out.WriteString("  artifacts:\n    paths: [result.json]\n    expire_in: 7 days\n    when: on_success\n    access: developer\n")
	return out.String()
}
