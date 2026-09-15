package api

import (
	"strconv"
	"strings"
)

// Pins were resolved from each official repository on 2026-09-12. Updating a
// pin changes the previewed workflow and requires an explicit install again.
const checkoutAction = "actions/checkout@11d5960a326750d5838078e36cf38b85af677262"
const loginAction = "docker/login-action@c94ce9fb468520275223c153574b00df6fe4bcc9"
const buildxAction = "docker/setup-buildx-action@8d2750c68a42422c14e847fe6c8ac0403b4cbd6f"
const buildPushAction = "docker/build-push-action@10e90e3645eae34f1e60eeb005ba3a3d33f178e8"
const uploadAction = "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02"
const packAction = "buildpacks/github-actions/setup-pack@af8c06ccd592e27001cbf78083dcc9b47aa6652a"
const paketoBuilder = "paketobuildpacks/builder-jammy-buildpackless-base@sha256:9e313856b55fcea8382fd4fc59f840361a99adc814982f4a5b75d5bda7fbf898"

func (c buildConfig) workflowPath() string {
	if c.Provider == "gitlab" {
		return ".gitlab-ci.yml"
	}
	return ".github/workflows/hakopod-build-" + c.ID + ".yml"
}
func (c buildConfig) imageName() string {
	if c.ManagedRegistry != "" {
		return c.ManagedRegistry
	}
	if c.Provider == "gitlab" {
		return "registry.gitlab.com/" + strings.ToLower(c.Repository) + "/hakopod-" + c.ID[:12]
	}
	return "ghcr.io/" + strings.ToLower(c.Repository) + "-hakopod-" + c.ID[:12]
}
func buildWorkflow(c buildConfig) string {
	if c.Provider == "gitlab" {
		return gitlabBuildWorkflow(c)
	}
	build := `      - name: Build and publish Dockerfile
        uses: {{BUILD_PUSH}}
{{SECRET_ENV}}        with:
          context: {{CONTEXT}}
          file: {{DOCKERFILE}}
          push: true
          tags: ${{ env.IMAGE_NAME }}:${{ env.REQUEST_ID }}
          provenance: mode=min
{{BUILD_ARGS}}{{SECRET_INPUTS}}`
	if c.Mode == "buildpacks" {
		build = `      - name: Install verified Cloud Native Buildpacks pack
        uses: {{PACK}}
        with:
          pack-version: "0.40.9"
      - name: Build and publish with Cloud Native Buildpacks
        env:
          BUILD_CONTEXT: {{CONTEXT}}
        run: |
          set -euo pipefail
{{PACK_SETUP}}          pack build "$IMAGE_NAME:$REQUEST_ID" --path "$BUILD_CONTEXT" --builder {{BUILDER}} --buildpack "$PACK_IMAGE" --env BP_WEB_SERVER=nginx{{PACK_ARGS}} --publish --pull-policy if-not-present
`
	}
	if c.Mode == "framework" {
		setup := frameworkSetup(c, `"$RUNNER_TEMP/hakopod.Dockerfile"`)
		var lines strings.Builder
		for _, line := range strings.Split(strings.TrimSuffix(setup, "\n"), "\n") {
			lines.WriteString("          " + line + "\n")
		}
		build = "      - name: Prepare reviewed framework recipe\n        run: |\n          set -eu\n" + lines.String() + strings.Replace(build, "{{DOCKERFILE}}", "${{ runner.temp }}/hakopod.Dockerfile", 1)
	}
	build = buildSecretCheck(c.BuildSecrets) + build
	text := `# Managed by Hakopod build {{BUILD_ID}}. Review through Hakopod before reinstalling.
name: Hakopod build {{BUILD_ID}}
run-name: ${{ github.event_name == 'push' && format('Hakopod push {0}', github.sha) || format('Hakopod {0}', inputs.request_id) }}
on:
{{PUSH_TRIGGER}}  workflow_dispatch:
    inputs:
      commit:
        description: Exact repository commit to build
        required: true
        type: string
      request_id:
        description: Hakopod build request identifier
        required: true
        type: string
permissions:
  contents: read
  packages: write
concurrency:
  group: hakopod-build-{{BUILD_ID}}
  cancel-in-progress: false
jobs:
  build:
    runs-on: {{RUNNER}}
    timeout-minutes: 30
    env:
      IMAGE_NAME: {{IMAGE}}
      SOURCE_SHA: ${{ inputs.commit || github.sha }}
      REQUEST_ID: ${{ inputs.request_id }}
    steps:
      - name: Validate request
        run: |
          set -euo pipefail
          if [ "$GITHUB_EVENT_NAME" = "push" ]; then
            REQUEST_ID="$(printf '%032x' "$GITHUB_RUN_ID")"
            echo "REQUEST_ID=$REQUEST_ID" >> "$GITHUB_ENV"
          fi
          [[ "$SOURCE_SHA" =~ ^[0-9a-f]{40,64}$ ]]
          [[ "$REQUEST_ID" =~ ^[0-9a-f]{32}$ ]]
      - name: Check out the exact source commit
        uses: {{CHECKOUT}}
        with:
          ref: ${{ env.SOURCE_SHA }}
          persist-credentials: false
      - name: Sign in to GitHub Container Registry
        uses: {{LOGIN}}
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Set up Buildx
        uses: {{BUILDX}}
{{BUILD_STEP}}      - name: Record immutable build result
        run: |
          set -euo pipefail
          docker buildx imagetools inspect "$IMAGE_NAME:$REQUEST_ID" > image-inspection.txt
          DIGEST="$(awk '$1 == "Digest:" { print $2; exit }' image-inspection.txt)"
          [[ "$DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]
          jq -n --arg build_id {{BUILD_ID}} --arg request_id "$REQUEST_ID" --arg commit_sha "$SOURCE_SHA" --arg image "$IMAGE_NAME@$DIGEST" '{build_id:$build_id,request_id:$request_id,commit_sha:$commit_sha,image:$image}' > result.json
      - name: Save build result for Hakopod
        uses: {{UPLOAD}}
        with:
          name: hakopod-result-${{ env.REQUEST_ID }}
          path: result.json
          retention-days: 7
          if-no-files-found: error
`
	push := ""
	if c.AutoBuild {
		push = "  push:\n    branches:\n      - " + strconv.Quote(c.Branch) + "\n    paths-ignore:\n      - " + strconv.Quote(c.workflowPath()) + "\n"
	}
	runner := "ubuntu-24.04"
	if c.Architecture == "arm64" {
		runner = "ubuntu-24.04-arm"
	}
	if c.ManagedRegistry != "" {
		text = strings.Replace(text, "  packages: write", "  id-token: write", 1)
		start := strings.Index(text, "      - name: Sign in to GitHub Container Registry")
		end := strings.Index(text, "      - name: Set up Buildx")
		text = text[:start] + managedRegistryLogin(c) + text[end:]
	}
	text = strings.ReplaceAll(text, "{{RUNNER}}", runner)
	text = strings.ReplaceAll(text, "{{PUSH_TRIGGER}}", push)
	text = strings.ReplaceAll(text, "{{BUILD_STEP}}", build)
	text = strings.ReplaceAll(text, "{{BUILD_ARGS}}", workflowBuildArguments(c.BuildArgs))
	text = strings.ReplaceAll(text, "{{SECRET_ENV}}", buildSecretEnvironment(c.BuildSecrets))
	text = strings.ReplaceAll(text, "{{SECRET_INPUTS}}", buildSecretInputs(c.BuildSecrets))
	text = strings.ReplaceAll(text, "{{PACK_ARGS}}", shellBuildArguments(c.BuildArgs, "--env"))
	return strings.NewReplacer("{{BUILD_ID}}", c.ID, "{{IMAGE}}", strconv.Quote(c.imageName()), "{{CHECKOUT}}", checkoutAction, "{{LOGIN}}", loginAction, "{{BUILDX}}", buildxAction, "{{UPLOAD}}", uploadAction, "{{BUILD_PUSH}}", buildPushAction, "{{PACK}}", packAction, "{{CONTEXT}}", strconv.Quote(c.ContextPath), "{{DOCKERFILE}}", strconv.Quote(c.Dockerfile), "{{BUILDER}}", paketoBuilder, "{{PACK_SETUP}}", buildpackSetup(c.Preset)).Replace(text)
}

var buildpackImages = map[string]string{
	"nodejs": "paketobuildpacks/nodejs@sha256:1d3d50ae1eef0013805436049cf54ad32ab8706bc09b1631390bfd95b9f66f8f",
	"python": "paketobuildpacks/python@sha256:f545f985bd0d4587f26e1cb4c1714c3d61fdb339cec100c1937ddae95ad878b9",
	"go":     "paketobuildpacks/go@sha256:bb88f96b8ab813e03c3be437b6ce6e8d95e5396021f9f97992d7976607d80242",
	"java":   "paketobuildpacks/java@sha256:c2f7f75bfd11011b000ee51e3bae81f76bf6489c31e66489575a7c4189490227",
	"dotnet": "paketobuildpacks/dotnet-core@sha256:4207aa272264367be62cdd73273b19b7d4ae65ca4da6def71baab2aee2afe359",
	"ruby":   "paketobuildpacks/ruby@sha256:9abc1efcc35da1492ef272766ebb569dd7fb71ce77f8da1105682b7eacaf555e",
	"static": "paketobuildpacks/web-servers@sha256:37acb0ce96f4c257c2ee95ca27eb33ccbe56e08469ec8d987a38812791fbe0dd",
}

func buildpackSetup(preset string) string {
	if preset != "auto" && preset != "" {
		return "          PACK_IMAGE=" + strconv.Quote(buildpackImages[preset]) + "\n"
	}
	checks := []struct{ name, test string }{
		{"nodejs", `[ -f "$BUILD_CONTEXT/package.json" ]`},
		{"python", `[ -f "$BUILD_CONTEXT/pyproject.toml" ] || [ -f "$BUILD_CONTEXT/requirements.txt" ] || [ -f "$BUILD_CONTEXT/Pipfile" ]`},
		{"go", `[ -f "$BUILD_CONTEXT/go.mod" ]`},
		{"java", `[ -f "$BUILD_CONTEXT/pom.xml" ] || [ -f "$BUILD_CONTEXT/build.gradle" ] || [ -f "$BUILD_CONTEXT/build.gradle.kts" ]`},
		{"dotnet", `compgen -G "$BUILD_CONTEXT/*.csproj" > /dev/null || compgen -G "$BUILD_CONTEXT/*.fsproj" > /dev/null`},
		{"ruby", `[ -f "$BUILD_CONTEXT/Gemfile" ]`},
		{"static", `[ -f "$BUILD_CONTEXT/index.html" ]`},
	}
	var result strings.Builder
	for i, check := range checks {
		keyword := "elif"
		if i == 0 {
			keyword = "if"
		}
		result.WriteString("          " + keyword + " " + check.test + "; then\n            PACK_IMAGE=" + strconv.Quote(buildpackImages[check.name]) + "\n")
	}
	result.WriteString("          else\n            echo 'No supported language manifest detected. Choose a buildpack preset or provide a Dockerfile.' >&2\n            exit 1\n          fi\n")
	return result.String()
}
