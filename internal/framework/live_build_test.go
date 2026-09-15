package framework

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Generated recipes are exercised in disposable containers without mounted
// host directories, provider credentials or access to the operator cluster.
func TestLiveFrameworkRecipes(t *testing.T) {
	if os.Getenv("HAKOPOD_FRAMEWORK_BUILD_TEST") != "1" {
		t.Skip("opt-in Docker framework build acceptance")
	}
	fixtures := map[string]map[string]string{
		"astro": {
			"package.json":          `{"type":"module","scripts":{"build":"astro build"},"dependencies":{"astro":"7.3.2"}}`,
			"src/pages/index.astro": "---\n---\n<html><body>hakopod-framework-ready</body></html>",
		},
		"nextjs": {
			"package.json":  `{"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16.3.5","react":"19.3.0","react-dom":"19.3.0"}}`,
			"app/layout.js": `export default function Layout({children}) {return <html><body>{children}</body></html>}`,
			"app/page.js":   `export default function Page() {return <main>hakopod-framework-ready</main>}`,
		},
		"sveltekit": {
			"package.json":            `{"type":"module","scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"2.70.3","@sveltejs/adapter-node":"5.5.7","@sveltejs/vite-plugin-svelte":"7.3.0","svelte":"5.57.0","vite":"8.3.0"}}`,
			"vite.config.js":          `import { sveltekit } from '@sveltejs/kit/vite'; export default {plugins:[sveltekit()]}`,
			"svelte.config.js":        `import adapter from '@sveltejs/adapter-node'; export default {kit:{adapter:adapter()}}`,
			"src/app.html":            `<!doctype html><html><head>%sveltekit.head%</head><body><div>%sveltekit.body%</div></body></html>`,
			"src/routes/+page.svelte": `<main>hakopod-framework-ready</main>`,
		},
		"tanstack-start": {
			"package.json":          `{"type":"module","scripts":{"build":"vite build"},"dependencies":{"@tanstack/react-start":"1.168.54","@tanstack/react-router":"1.170.36","react":"19.3.0","react-dom":"19.3.0","nitro":"3.0.260903-beta","vite":"8.3.0","@vitejs/plugin-react":"6.1.1"}}`,
			"vite.config.ts":        `import { defineConfig } from 'vite'; import { tanstackStart } from '@tanstack/react-start/plugin/vite'; import react from '@vitejs/plugin-react'; import { nitro } from 'nitro/vite'; export default defineConfig({plugins:[tanstackStart(),nitro(),react()]})`,
			"src/router.tsx":        `import {createRouter} from '@tanstack/react-router';import {routeTree} from './routeTree.gen';export function getRouter(){return createRouter({routeTree})}`,
			"src/routes/__root.tsx": `import {createRootRoute,Outlet,HeadContent,Scripts} from '@tanstack/react-router';export const Route=createRootRoute({component:()=> <html><head><HeadContent/></head><body><Outlet/><Scripts/></body></html>})`,
			"src/routes/index.tsx":  `import {createFileRoute} from '@tanstack/react-router';export const Route=createFileRoute('/')({component:()=> <main>hakopod-framework-ready</main>})`,
		},
		"static-secret": {
			"index.html": "hakopod-framework-ready",
		},
	}
	for _, name := range []string{"astro", "nextjs", "sveltekit", "tanstack-start", "static-secret"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			dir := t.TempDir()
			files := map[string][]byte{}
			for path, body := range fixtures[name] {
				full := filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(path, "/") {
					files[path] = []byte(body)
				}
			}
			detection, err := Detect(files)
			if err != nil {
				t.Fatal(err)
			}
			secrets := map[string]string{}
			args := []string{"build", "--progress=plain"}
			if name == "static-secret" {
				secrets["probe"] = "BUILD_PROBE"
				detection.Plan.BuildCommand = `test "$BUILD_PROBE" = synthetic-build-secret && test -f /run/secrets/probe`
				secret := filepath.Join(t.TempDir(), "value")
				if err := os.WriteFile(secret, []byte("synthetic-build-secret"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--secret", "id=probe,src="+secret)
			}
			recipe, err := Dockerfile(detection.Plan, secrets)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(recipe), 0600); err != nil {
				t.Fatal(err)
			}
			// Production workflow adds these excludes; never copy the recipe into plain HTML.
			os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("Dockerfile\n.dockerignore\nnode_modules\n.git\n.env\n.env.*\n"), 0600)
			tag := fmt.Sprintf("hakopod-framework-fixture:%s-%d", name, time.Now().UnixNano())
			args = append(args, "-t", tag, dir)
			cmd := exec.CommandContext(ctx, "docker", args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("build failed: %v\n%s", err, output)
			}
			defer exec.Command("docker", "image", "rm", tag).Run()
			cmd = exec.CommandContext(ctx, "docker", "run", "-d", "--memory=512m", "--cpus=1", "-p", fmt.Sprintf("127.0.0.1::%d", detection.Plan.Port), tag)
			output, err = cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run: %v %s", err, output)
			}
			id := strings.TrimSpace(string(output))
			defer exec.Command("docker", "rm", "-f", id).Run()
			output, err = exec.CommandContext(ctx, "docker", "port", id, fmt.Sprint(detection.Plan.Port)).Output()
			if err != nil {
				t.Fatal(err)
			}
			endpoint := "http://" + strings.TrimSpace(string(output))
			httpClient := &http.Client{Timeout: 2 * time.Second}
			deadline := time.Now().Add(45 * time.Second)
			for {
				res, e := httpClient.Get(endpoint)
				if e == nil {
					body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
					res.Body.Close()
					if res.StatusCode == 200 && strings.Contains(string(body), "hakopod-framework-ready") {
						break
					}
				}
				if time.Now().After(deadline) {
					logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
					t.Fatalf("runtime not ready: %v\n%s", e, logs)
				}
				time.Sleep(250 * time.Millisecond)
			}
			if name == "static-secret" {
				out, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-ec", `test ! -e /run/secrets/probe; test -z "$BUILD_PROBE"; ! grep -R -l synthetic-build-secret /usr/share/nginx/html`).CombinedOutput()
				if err != nil {
					t.Fatalf("secret persisted: %v %s", err, out)
				}
			}
			t.Log("built and served", name, "as UID 10001")
		})
	}
}
