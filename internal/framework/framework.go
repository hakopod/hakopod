// Package framework derives reviewable build plans from repository metadata.
// It never executes package scripts or framework configuration.
package framework

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const NodeImage = "node:24-alpine@sha256:50c8e8ca1d27439048670df5883f32d57cf81cff6233222c893fd0d9884cbd81"
const StaticImage = "nginx:stable-alpine-slim@sha256:77da26c31397bf6694b4bf93275f5b40b0b120ba1b8f114264b603e592c561d6"

type Plan struct {
	Framework       string `json:"framework"`
	Runtime         string `json:"runtime"`
	PackageManager  string `json:"package_manager"`
	InstallCommand  string `json:"install_command"`
	BuildCommand    string `json:"build_command"`
	StartCommand    string `json:"start_command,omitempty"`
	OutputDirectory string `json:"output_directory,omitempty"`
	Port            int    `json:"port"`
}
type Detection struct {
	Plan     Plan     `json:"plan"`
	Warnings []string `json:"warnings"`
}

func Detect(files map[string][]byte) (Detection, error) {
	var manifest struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		PackageManager  string            `json:"packageManager"`
	}
	if len(files["package.json"]) > 256<<10 {
		return Detection{}, fmt.Errorf("package.json exceeds 256 KiB")
	}
	if len(files["package.json"]) == 0 {
		if _, ok := files["index.html"]; ok {
			return Detection{Plan: Plan{Framework: "static", Runtime: "static", PackageManager: "none", InstallCommand: "true", BuildCommand: "true", OutputDirectory: ".", Port: 8080}, Warnings: []string{}}, nil
		}
		return Detection{}, fmt.Errorf("No supported package.json or index.html found in the selected build directory")
	}
	if json.Unmarshal(files["package.json"], &manifest) != nil {
		return Detection{}, fmt.Errorf("Invalid package.json")
	}
	has := func(name string) bool {
		return manifest.Dependencies[name] != "" || manifest.DevDependencies[name] != ""
	}
	manager := "npm"
	for _, item := range []struct{ file, manager string }{{"yarn.lock", "yarn"}, {"pnpm-lock.yaml", "pnpm"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"}} {
		if _, ok := files[item.file]; ok {
			manager = item.manager
		}
	}
	if manifest.PackageManager != "" {
		declared := strings.Split(manifest.PackageManager, "@")[0]
		if declared != "npm" && declared != "pnpm" && declared != "yarn" && declared != "bun" {
			return Detection{}, fmt.Errorf("framework.package_manager: Unsupported package manager")
		}
		manager = declared
	}
	install := map[string]string{"npm": "npm ci", "pnpm": "pnpm install --frozen-lockfile", "yarn": "yarn install --frozen-lockfile", "bun": "bun install --frozen-lockfile"}[manager]
	warnings := []string{}
	if manager == "npm" {
		if _, ok := files["package-lock.json"]; !ok {
			install = "npm install"
			warnings = append(warnings, "Commit a package-lock.json to make dependency installation reproducible.")
		}
	}
	p := Plan{Framework: "node", Runtime: "node", PackageManager: manager, InstallCommand: install, BuildCommand: manager + " run build", StartCommand: manager + " run start", Port: 3000}
	switch {
	case has("astro"):
		p.Framework = "astro"
		p.Runtime = "static"
		p.OutputDirectory = "dist"
		p.StartCommand = ""
		p.Port = 8080
		if has("@astrojs/node") {
			p.Runtime = "node"
			p.StartCommand = "node dist/server/entry.mjs"
			p.OutputDirectory = ""
			p.Port = 3000
		}
	case has("next"):
		p.Framework = "nextjs"
		p.StartCommand = "./node_modules/.bin/next start --hostname 0.0.0.0"
		export := regexp.MustCompile(`output\s*:\s*["']export["']`)
		for _, name := range []string{"next.config.js", "next.config.mjs", "next.config.ts"} {
			if export.Match(files[name]) {
				p.Runtime = "static"
				p.OutputDirectory = "out"
				p.StartCommand = ""
				p.Port = 8080
			}
		}
	case has("@sveltejs/kit"):
		p.Framework = "sveltekit"
		if has("@sveltejs/adapter-static") {
			p.Runtime = "static"
			p.OutputDirectory = "build"
			p.StartCommand = ""
			p.Port = 8080
		} else if has("@sveltejs/adapter-node") {
			p.StartCommand = "node build/index.js"
		} else {
			warnings = append(warnings, "Configure adapter-node or adapter-static before building SvelteKit for Hakopod.")
		}
	case has("@tanstack/react-start") || has("@tanstack/solid-start"):
		p.Framework = "tanstack-start"
		p.StartCommand = "node .output/server/index.mjs"
		warnings = append(warnings, "Confirm the Node server output path for your TanStack Start adapter. Choose static only when your app explicitly prerenders every route.")
	case has("vite"):
		p.Framework = "vite"
		p.Runtime = "static"
		p.OutputDirectory = "dist"
		p.StartCommand = ""
		p.Port = 8080
	default:
		warnings = append(warnings, "Review the build and start commands for this Node application.")
	}
	if manifest.Scripts["build"] == "" {
		p.BuildCommand = "true"
		warnings = append(warnings, "No build script is defined; confirm whether this application needs a build step.")
	}
	warnings = append(warnings, "Framework configuration was read as text, not executed. Review the suggested runtime and paths.")
	return Detection{Plan: p, Warnings: warnings}, nil
}

var safePath = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)

func Validate(p Plan) error {
	switch p.Framework {
	case "astro", "nextjs", "sveltekit", "tanstack-start", "vite", "node", "static":
	default:
		return fmt.Errorf("framework.framework: Unsupported framework")
	}
	if p.Runtime != "node" && p.Runtime != "static" {
		return fmt.Errorf("framework.runtime: Runtime must be node or static")
	}
	switch p.PackageManager {
	case "npm", "pnpm", "yarn", "bun":
	case "none":
		if p.Framework != "static" {
			return fmt.Errorf("framework.package_manager: Package manager required")
		}
	default:
		return fmt.Errorf("framework.package_manager: Unsupported package manager")
	}
	for _, field := range []struct{ name, command string }{
		{"install_command", p.InstallCommand}, {"build_command", p.BuildCommand}, {"start_command", p.StartCommand},
	} {
		command := field.command
		if len(command) > 1024 || strings.ContainsAny(command, "\r\n\x00") {
			return fmt.Errorf("framework.%s: Build commands must be single lines of at most 1024 characters", field.name)
		}
	}
	if p.InstallCommand == "" {
		return fmt.Errorf("framework.install_command: Install command is required")
	}
	if p.BuildCommand == "" {
		return fmt.Errorf("framework.build_command: Build command is required")
	}
	if p.Runtime == "static" {
		if p.OutputDirectory == "" || len(p.OutputDirectory) > 200 || !safePath.MatchString(p.OutputDirectory) || path.Clean(p.OutputDirectory) != p.OutputDirectory || strings.HasPrefix(p.OutputDirectory, "/") || p.OutputDirectory == ".." || strings.HasPrefix(p.OutputDirectory, "../") {
			return fmt.Errorf("framework.output_directory: Static output must be a relative directory")
		}
		if p.Port != 8080 {
			return fmt.Errorf("port: Static hosting uses port 8080")
		}
		if p.StartCommand != "" {
			return fmt.Errorf("framework.start_command: Static hosting does not use a start command")
		}
	} else {
		if p.StartCommand == "" {
			return fmt.Errorf("framework.start_command: Node runtime needs a start command")
		}
		if p.Port < 1 || p.Port > 65535 {
			return fmt.Errorf("port: Use a port between 1 and 65535")
		}
	}
	return nil
}

var secretID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var secretRef = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,99}$`)

func ValidateSecrets(secrets map[string]string) error {
	if len(secrets) > 16 {
		return fmt.Errorf("At most 16 build secrets are allowed")
	}
	for id, ref := range secrets {
		if !secretID.MatchString(id) || !secretRef.MatchString(ref) {
			return fmt.Errorf("Build secrets use lowercase mount IDs and uppercase CI secret names")
		}
		for _, prefix := range []string{"GITHUB_", "GITLAB_", "CI_", "RUNNER_", "ACTIONS_", "HAKOPOD_", "DOCKER_", "BUILDKIT_"} {
			if strings.HasPrefix(ref, prefix) {
				return fmt.Errorf("Build secrets cannot refer to reserved CI variables")
			}
		}
		switch ref {
		case "PATH", "HOME", "SHELL", "ENV", "BASH_ENV", "LD_PRELOAD", "LD_LIBRARY_PATH", "NODE_OPTIONS", "NODE_PATH", "NPM_CONFIG_PREFIX", "IFS":
			return fmt.Errorf("Build secrets cannot replace process configuration")
		}
	}
	return nil
}
func SecretIDs(secrets map[string]string) []string {
	ids := make([]string, 0, len(secrets))
	for id := range secrets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func jsonCommand(command string) string {
	b, _ := json.Marshal([]string{"sh", "-ec", command})
	return string(b)
}
func Dockerfile(p Plan, secrets map[string]string) (string, error) {
	if err := Validate(p); err != nil {
		return "", err
	}
	if err := ValidateSecrets(secrets); err != nil {
		return "", err
	}
	var s strings.Builder
	s.WriteString("FROM " + NodeImage + " AS build\nWORKDIR /app\n")
	switch p.PackageManager {
	case "pnpm":
		s.WriteString("RUN npm install --global pnpm@10.7.1\n")
	case "yarn":
		s.WriteString("RUN npm install --global --force yarn@1.22.22\n")
	case "bun":
		s.WriteString("RUN npm install --global bun@1.3.8\n")
	}
	s.WriteString("COPY . .\n")
	mounts, exports := "", ""
	for _, id := range SecretIDs(secrets) {
		mounts += "--mount=type=secret,id=" + id + ",required=true "
		exports += "export " + secrets[id] + "=\"$(cat /run/secrets/" + id + ")\"; "
	}
	s.WriteString("RUN " + mounts + jsonCommand(exports+p.InstallCommand+" && "+p.BuildCommand) + "\n")
	if p.Runtime == "static" {
		// Copy only the reviewed output to the final image. Do not ship the source,
		// dependency tree or package-manager credentials in the web-server layer.
		s.WriteString("RUN " + jsonCommand("mkdir -p /hakopod-output && cp -a "+p.OutputDirectory+"/. /hakopod-output/ && test -f /hakopod-output/index.html") + "\n")
		s.WriteString("FROM " + StaticImage + "\n")
		s.WriteString("COPY --from=build --chown=10001:10001 /hakopod-output /usr/share/nginx/html\n")
		conf := "pid /tmp/nginx.pid; events {} http { include /etc/nginx/mime.types; default_type application/octet-stream; access_log /dev/stdout; error_log /dev/stderr; client_body_temp_path /tmp/client; proxy_temp_path /tmp/proxy; fastcgi_temp_path /tmp/fastcgi; uwsgi_temp_path /tmp/uwsgi; scgi_temp_path /tmp/scgi; server { listen 8080; root /usr/share/nginx/html; index index.html; location / { try_files $uri $uri/ =404; } } }"
		s.WriteString("RUN " + jsonCommand("printf '%s' '"+conf+"' > /etc/nginx/nginx.conf") + "\nUSER 10001:10001\nEXPOSE 8080\nENTRYPOINT [\"nginx\",\"-g\",\"daemon off;\"]\n")
	} else {
		s.WriteString("FROM " + NodeImage + "\nWORKDIR /app\nENV NODE_ENV=production HOST=0.0.0.0 HOSTNAME=0.0.0.0 PORT=" + fmt.Sprint(p.Port) + "\nCOPY --from=build --chown=10001:10001 /app /app\n")
		// Runtime package-manager commands need their executable in the final stage.
		switch p.PackageManager {
		case "pnpm":
			s.WriteString("RUN npm install --global pnpm@10.7.1\n")
		case "yarn":
			s.WriteString("RUN npm install --global --force yarn@1.22.22\n")
		case "bun":
			s.WriteString("RUN npm install --global bun@1.3.8\n")
		}
		s.WriteString("USER 10001:10001\nEXPOSE " + fmt.Sprint(p.Port) + "\nCMD " + jsonCommand("exec "+p.StartCommand) + "\n")
	}
	return s.String(), nil
}
