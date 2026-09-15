package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/framework"
	"sigs.k8s.io/yaml"
)

func TestFrameworkSetupRejectsRepositorySymlinkEscapes(t *testing.T) {
	for _, escape := range []string{"context", "ignore"} {
		t.Run(escape, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			protected := filepath.Join(outside, ".dockerignore")
			if err := os.WriteFile(protected, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			c := buildConfig{ContextPath: ".", Framework: &framework.Plan{Framework: "static", Runtime: "static", PackageManager: "none", InstallCommand: "true", BuildCommand: "true", OutputDirectory: ".", Port: 8080}}
			if escape == "context" {
				if err := os.Symlink(outside, filepath.Join(root, "app")); err != nil {
					t.Fatal(err)
				}
				c.ContextPath = "app"
			} else if err := os.Symlink(protected, filepath.Join(root, ".dockerignore")); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "recipe")
			command := exec.Command("sh", "-eu", "-c", frameworkSetup(c, destination))
			command.Dir = root
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatal("symlink escape accepted", string(output))
			}
			if body, err := os.ReadFile(protected); err != nil || string(body) != "unchanged" {
				t.Fatal("outside file changed", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("recipe created before context was checked", err)
			}
		})
	}
}

func TestFrameworkWorkflowsAndBuildSecrets(t *testing.T) {
	p := framework.Plan{Framework: "vite", Runtime: "static", PackageManager: "npm", InstallCommand: "npm ci", BuildCommand: "npm run build", OutputDirectory: "dist", Port: 8080}
	for _, provider := range []string{"github", "gitlab"} {
		c, err := normalizeBuild(buildInput{Project: "demo", Environment: "development", Name: "web", Repository: "team/repo", Provider: provider, Mode: "framework", Framework: &p, BuildSecrets: map[string]string{"npm_token": "NPM_TOKEN"}})
		if err != nil {
			t.Fatal(err)
		}
		c.ID = strings.Repeat("a", 32)
		c.Architecture = "amd64"
		workflow := buildWorkflow(c)
		var doc map[string]any
		if yaml.UnmarshalStrict([]byte(workflow), &doc) != nil {
			t.Fatalf("invalid %s YAML: %s", provider, workflow)
		}
		if strings.Contains(workflow, "{{BUILD_") || strings.Contains(workflow, "{{SECRET_") {
			t.Fatal("unexpanded workflow")
		}
		if !strings.Contains(workflow, "hakopod.Dockerfile") || !strings.Contains(workflow, "required") {
			t.Fatal("framework workflow missing recipe or secret guard")
		}
		if provider == "github" {
			if !strings.Contains(workflow, "secret-envs:") || !strings.Contains(workflow, "secrets['NPM_TOKEN']") {
				t.Fatal("GitHub secret not mounted")
			}
		} else if !strings.Contains(workflow, "type=env,id=npm_token,env=NPM_TOKEN") {
			t.Fatal("GitLab secret not mounted")
		}
	}
}
func TestBuildpackSecretsRejected(t *testing.T) {
	_, err := normalizeBuild(buildInput{Project: "demo", Environment: "development", Name: "web", Repository: "team/repo", Mode: "buildpacks", BuildSecrets: map[string]string{"npm_token": "NPM_TOKEN"}})
	if err == nil {
		t.Fatal("buildpack secrets could leak through plain environment")
	}
}
