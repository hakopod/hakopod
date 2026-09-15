package framework

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFrameworkPlans(t *testing.T) {
	for _, tc := range []struct{ name, dependency, runtime, output string }{
		{"astro", "astro", "static", "dist"}, {"nextjs", "next", "node", ""}, {"sveltekit", "@sveltejs/kit", "node", ""}, {"tanstack-start", "@tanstack/react-start", "node", ""}, {"vite", "vite", "static", "dist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"dependencies": map[string]string{tc.dependency: "1"}, "scripts": map[string]string{"build": "build"}})
			d, err := Detect(map[string][]byte{"package.json": body, "package-lock.json": nil})
			if err != nil || d.Plan.Framework != tc.name || d.Plan.Runtime != tc.runtime || d.Plan.OutputDirectory != tc.output {
				t.Fatalf("%+v %v", d, err)
			}
			if err = Validate(d.Plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestStaticExportAndSecretMounts(t *testing.T) {
	d, err := Detect(map[string][]byte{"package.json": []byte(`{"dependencies":{"next":"1"},"scripts":{"build":"next build"},"packageManager":"pnpm@10.7.1"}`), "next.config.mjs": []byte(`export default {output: "export"}`)})
	if err != nil || d.Plan.Runtime != "static" || d.Plan.OutputDirectory != "out" || d.Plan.PackageManager != "pnpm" {
		t.Fatalf("%+v %v", d, err)
	}
	recipe, err := Dockerfile(d.Plan, map[string]string{"npm_token": "NPM_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"type=secret,id=npm_token,required=true", "COPY --from=build --chown=10001:10001 /hakopod-output", "USER 10001:10001", "try_files $uri $uri/ =404"} {
		if !strings.Contains(recipe, required) {
			t.Fatalf("missing %s", required)
		}
	}
	if strings.Contains(recipe, "ENV NPM_TOKEN") || strings.Contains(recipe, "ARG NPM_TOKEN") {
		t.Fatal("secret persisted as image configuration")
	}
	if strings.Contains(recipe, "COPY --from=build /app /app") {
		t.Fatal("static image contains source/dependencies")
	}
}
func TestInvalidPlansAndSecrets(t *testing.T) {
	p := Plan{Framework: "vite", Runtime: "static", PackageManager: "npm", InstallCommand: "npm ci", BuildCommand: "npm run build", OutputDirectory: "dist", Port: 8080}
	for _, dir := range []string{"../../etc", "/etc", "dist;cat", "dist\nRUN bad", "a/../dist"} {
		bad := p
		bad.OutputDirectory = dir
		if Validate(bad) == nil {
			t.Fatalf("accepted path %q", dir)
		}
	}
	bad := p
	bad.BuildCommand = "build\nFROM other"
	if Validate(bad) == nil {
		t.Fatal("accepted newline")
	}
	for _, secrets := range []map[string]string{{"npm": "GITHUB_TOKEN"}, {"npm": "PATH"}, {"npm": "TOKEN' ] }}"}, {"../npm": "TOKEN"}, {"npm": "BASH_ENV"}} {
		if ValidateSecrets(secrets) == nil {
			t.Fatal("accepted unsafe secret reference")
		}
	}
}
