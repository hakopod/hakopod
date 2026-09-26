package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
	"go.yaml.in/yaml/v3"
)

func TestGitLabBuildWorkflowArchitectureAndSyntax(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, mode := range []string{"dockerfile", "buildpacks"} {
			for _, preset := range []string{"auto", "go", "dotnet"} {
				c := buildConfig{BuildArgs: map[string]string{"NEXT_PUBLIC_API_URL": "https://api.example.com", "PUBLIC_LABEL": "test with spaces and ' quotes"}, Provider: "gitlab", ID: strings.Repeat("a", 32), Repository: "group/subgroup/source", Branch: "release", Mode: mode, Preset: preset, ContextPath: "app", Dockerfile: "app/Dockerfile", Architecture: architecture, AutoBuild: true}
				workflow := buildWorkflow(c)
				var parsed struct {
					Workflow struct {
						Rules []map[string]string `yaml:"rules"`
					} `yaml:"workflow"`
				}
				if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
					t.Fatal(err)
				}
				if len(parsed.Workflow.Rules) != 3 {
					t.Fatal("manual/push CI rules missing")
				}
				var root map[string]any
				if err := yaml.Unmarshal([]byte(workflow), &root); err != nil {
					t.Fatal(err)
				}
				job := root[c.gitlabJobName()].(map[string]any)
				if job["image"] != gitlabDockerCLI || !strings.Contains(workflow, gitlabDockerDaemon) || !strings.Contains(workflow, "saas-linux-small-"+architecture) || !strings.Contains(workflow, "--mtu=1400") {
					t.Fatal("CI toolchain or native architecture is not pinned")
				}
				for _, script := range job["script"].([]any) {
					command := exec.Command("sh", "-n")
					command.Stdin = strings.NewReader(script.(string))
					if out, err := command.CombinedOutput(); err != nil {
						t.Fatalf("invalid generated POSIX shell: %s", out)
					}
				}
				if strings.Contains(workflow, "{{") || strings.Contains(workflow, "compgen") || strings.Contains(workflow, "PRIVATE-TOKEN") {
					t.Fatal("CI workflow contains unresolved or unsafe setup")
				}
				if mode == "buildpacks" && !strings.Contains(workflow, "sha256sum -c -") {
					t.Fatal("pack download has no pinned checksum")
				}
				if strings.Contains(workflow, "submodule") {
					t.Fatal("a build without the submodules option must generate the CI file it generates today")
				}
				submodules := c
				submodules.Submodules = true
				enabled := buildWorkflow(submodules)
				variables := "    GIT_SUBMODULE_STRATEGY: \"recursive\"\n    GIT_SUBMODULE_FORCE_HTTPS: \"true\"\n"
				if !strings.Contains(enabled, "    GIT_DEPTH: \"1\"\n"+variables) {
					t.Fatal("submodule clone variables missing beside GIT_DEPTH")
				}
				update := "      git submodule update --init --recursive\n"
				detach := strings.Index(enabled, "      git checkout --detach")
				if detach < 0 || strings.Index(enabled, update) < detach {
					t.Fatal("submodules must be updated after HEAD moves to the source commit")
				}
				var enabledRoot map[string]any
				if err := yaml.Unmarshal([]byte(enabled), &enabledRoot); err != nil {
					t.Fatal(err)
				}
				for _, script := range enabledRoot[c.gitlabJobName()].(map[string]any)["script"].([]any) {
					command := exec.Command("sh", "-n")
					command.Stdin = strings.NewReader(script.(string))
					if out, err := command.CombinedOutput(); err != nil {
						t.Fatalf("invalid generated POSIX shell: %s", out)
					}
				}
				if strings.Replace(strings.Replace(enabled, variables, "", 1), update, "", 1) != workflow {
					t.Fatal("submodules changed the CI file outside the variables and the update command")
				}
			}
		}
	}
}

func TestGitLabBuildDispatchAmbiguity(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server error may follow acceptance", 503, `{}`, "dispatch_unknown"},
		{"request timeout may follow acceptance", 408, `{}`, "dispatch_unknown"},
		{"truncated accepted response", 201, `{`, "dispatch_unknown"},
		{"explicit request rejection", 400, `{}`, "failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(scenario.status)
				w.Write([]byte(scenario.body))
			}))
			defer remote.Close()
			server := &Server{gitlabAPIURL: remote.URL, gitlabHTTP: remote.Client(), gitlabTestCredentials: func(context.Context) (map[string][]byte, error) {
				return map[string][]byte{"token": []byte("local-fixture-token")}, nil
			}}
			state, _, id := server.dispatchGitLabBuild(context.Background(), buildConfig{Provider: "gitlab", ID: strings.Repeat("a", 32), Repository: "group/project"}, "main", strings.Repeat("b", 40), strings.Repeat("c", 32))
			if state != scenario.want || id != 0 {
				t.Fatalf("dispatch state %q/%d, want %q/0", state, id, scenario.want)
			}
		})
	}
}

func TestGitLabBuildManualAutomaticRecoveryAndOwnership(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "GitLab Build Owner", "gitlab-build@example.test", "gitlab build owner password", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	workflow := "stages: [existing-ci]\n"
	workflowSHA := strings.Repeat("f", 40)
	sourceSHA := strings.Repeat("a", 40)
	config := buildConfig{}
	pipelines := map[int64]gitlabPipeline{}
	variables := map[int64][]map[string]string{}
	requestIDs := map[int64]string{}
	sourceCommits := map[int64]string{}
	writes, dispatches, cancels := 0, 0, 0
	nextPipeline := int64(901)
	dispatchStatus := "success"
	customCIPath := ""
	oversized, wrongJobCommit, ambiguous := false, false, false
	projectPath := "/projects/group/subgroup/source"
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("PRIVATE-TOKEN") != "local-gitlab-build-token" {
			t.Error("GitLab fixture authentication missing")
		}
		switch {
		case r.URL.Path == projectPath && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main", "ci_config_path": customCIPath})
		case strings.HasPrefix(r.URL.Path, projectPath+"/repository/commits/"):
			json.NewEncoder(w).Encode(map[string]string{"id": sourceSHA})
		case r.URL.Path == projectPath+"/repository/files/.gitlab-ci.yml" && r.Method == "GET":
			if workflow == "" {
				http.NotFound(w, r)
				return
			}
			commit := r.URL.Query().Get("ref")
			if !commitPattern.MatchString(commit) {
				commit = workflowSHA
			}
			json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(workflow)), "size": len(workflow), "commit_id": commit, "last_commit_id": workflowSHA})
		case r.URL.Path == projectPath+"/repository/files/.gitlab-ci.yml" && (r.Method == "POST" || r.Method == "PUT"):
			var input struct {
				Content    string `json:"content"`
				Branch     string `json:"branch"`
				LastCommit string `json:"last_commit_id"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid CI write")
			}
			if input.Branch != "main" || (r.Method == "PUT" && input.LastCommit != workflowSHA) {
				t.Error("CI write lost branch/file revision")
			}
			workflow = input.Content
			writes++
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"file_path": ".gitlab-ci.yml", "branch": "main"})
		case r.URL.Path == projectPath+"/pipeline" && r.Method == "POST":
			var input struct {
				Ref       string              `json:"ref"`
				Variables []map[string]string `json:"variables"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid pipeline input")
			}
			id := nextPipeline
			nextPipeline++
			dispatches++
			vars := map[string]string{}
			for _, v := range input.Variables {
				vars[v["key"]] = v["value"]
			}
			if vars["HAKOPOD_BUILD_ID"] != config.ID || vars["HAKOPOD_SOURCE_SHA"] != sourceSHA || input.Ref != "main" {
				t.Error("pipeline was not pinned to the requested source and build")
			}
			requestIDs[id] = vars["HAKOPOD_REQUEST_ID"]
			sourceCommits[id] = vars["HAKOPOD_SOURCE_SHA"]
			variables[id] = input.Variables
			pipelines[id] = gitlabPipeline{ID: id, SHA: workflowSHA, Ref: "main", Source: "api", Status: dispatchStatus}
			w.WriteHeader(201)
			if ambiguous {
				w.Write([]byte("incomplete response"))
				return
			}
			json.NewEncoder(w).Encode(pipelines[id])
		case r.URL.Path == projectPath+"/pipelines" && r.Method == "GET":
			items := []gitlabPipeline{}
			for _, p := range pipelines {
				items = append(items, p)
			}
			json.NewEncoder(w).Encode(items)
		case strings.HasPrefix(r.URL.Path, projectPath+"/pipelines/"):
			suffix := strings.TrimPrefix(r.URL.Path, projectPath+"/pipelines/")
			parts := strings.Split(suffix, "/")
			id, _ := strconv.ParseInt(parts[0], 10, 64)
			if len(parts) == 1 {
				json.NewEncoder(w).Encode(pipelines[id])
				return
			}
			switch parts[1] {
			case "variables":
				json.NewEncoder(w).Encode(variables[id])
			case "jobs":
				commit := pipelines[id].SHA
				if wrongJobCommit {
					commit = strings.Repeat("e", 40)
				}
				json.NewEncoder(w).Encode([]map[string]any{{"id": id + 1000, "name": config.gitlabJobName(), "status": "success", "commit": map[string]string{"id": commit}, "pipeline": map[string]int64{"id": id}}})
			case "cancel":
				p := pipelines[id]
				p.Status = "canceled"
				pipelines[id] = p
				cancels++
				json.NewEncoder(w).Encode(p)
			default:
				http.NotFound(w, r)
			}
		case strings.HasPrefix(r.URL.Path, projectPath+"/jobs/") && strings.HasSuffix(r.URL.Path, "/artifacts/result.json"):
			if oversized {
				w.Write(bytes.Repeat([]byte("x"), (32<<10)+1))
				return
			}
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, projectPath+"/jobs/"), "/")
			job, _ := strconv.ParseInt(parts[0], 10, 64)
			id := job - 1000
			json.NewEncoder(w).Encode(buildArtifactResult{BuildID: config.ID, RequestID: requestIDs[id], CommitSHA: sourceCommits[id], Image: config.imageName() + "@sha256:" + fmt.Sprintf("%064x", id)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitlab.Close()
	secret := bytes.Repeat([]byte("g"), 32)
	server := &Server{Store: db, gitlabAPIURL: gitlab.URL, gitlabHTTP: gitlab.Client(), gitlabTestCredentials: func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"token": []byte("local-gitlab-build-token"), "webhook-secret": secret}, nil
	}, githubTestCredentials: func(context.Context) (map[string][]byte, error) {
		t.Error("GitLab build accessed GitHub credentials")
		return nil, fmt.Errorf("wrong provider")
	}}
	handler := server.Handler()
	call := func(method, path, idem string, input any, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, bytes.NewReader(store.JSON(input)))
		r.Header.Set("Authorization", "Bearer "+session.Token)
		r.Header.Set("Content-Type", "application/json")
		if idem != "" {
			r.Header.Set("Idempotency-Key", idem)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d: %s", method, path, w.Code, want, w.Body.String())
		}
		var result map[string]any
		if json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("invalid API response")
		}
		return result
	}
	created := call("POST", "/builds", "", map[string]any{"provider": "gitlab", "project": "demo", "environment": "development", "name": "gitlab-built", "repository": "Group/Subgroup/Source", "branch": "release", "mode": "buildpacks", "preset": "go", "architecture": "arm64", "auto_build": true, "auto_deploy": true}, 201)
	mu.Lock()
	err = json.Unmarshal(store.JSON(created), &config)
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	base := "/builds/" + config.ID
	preview := call("POST", base+"/preview", "", map[string]any{}, 200)
	if preview["workflow_path"] != ".gitlab-ci.yml" || !strings.HasPrefix(preview["image_repository"].(string), "registry.gitlab.com/") {
		t.Fatal("GitLab preview used the wrong CI or registry")
	}
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 1}, 409)
	mu.Lock()
	if writes != 0 {
		t.Fatal("unowned CI was overwritten")
	}
	workflow = ""
	customCIPath = "custom.yml"
	mu.Unlock()
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 1}, 409)
	mu.Lock()
	customCIPath = ""
	mu.Unlock()
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 1}, 200)
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 1}, 200)
	if _, err = db.Pool.Exec(ctx, "UPDATE build_configs SET installed_revision=0,installed_commit='' WHERE id=$1", config.ID); err != nil {
		t.Fatal(err)
	}
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 1}, 200)
	first := call("POST", base+"/run", "gitlab-manual-first", map[string]int64{"expected_config_revision": 1}, 202)
	again := call("POST", base+"/run", "gitlab-manual-first", map[string]int64{"expected_config_revision": 1}, 202)
	if first["id"] != again["id"] || first["provider"] != "gitlab" || first["remote_run_id"] != float64(901) || first["github_run_id"] != float64(0) {
		t.Fatal("GitLab idempotency or provider-specific observation failed")
	}
	mu.Lock()
	oversized = true
	mu.Unlock()
	runPath := base + "/runs/" + first["id"].(string)
	if call("GET", runPath, "", nil, 200)["image"] != "" {
		t.Fatal("oversized GitLab artifact was accepted")
	}
	mu.Lock()
	oversized = false
	wrongJobCommit = true
	mu.Unlock()
	if call("GET", runPath, "", nil, 200)["image"] != "" {
		t.Fatal("job from another commit was accepted")
	}
	mu.Lock()
	wrongJobCommit = false
	customCIPath = "custom.yml"
	mu.Unlock()
	call("GET", runPath, "", nil, 503)
	mu.Lock()
	customCIPath = ""
	workflow += "# modified after review\n"
	mu.Unlock()
	call("GET", runPath, "", nil, 503)
	mu.Lock()
	workflow = buildWorkflow(config)
	mu.Unlock()
	observed := call("GET", runPath, "", nil, 200)
	if observed["image"] == "" || observed["status"] != "completed" {
		t.Fatal("verified GitLab image missing")
	}
	plan := call("POST", runPath+"/plan", "", map[string]any{}, 200)
	if plan["expected_revision"] != float64(0) {
		t.Fatal("GitLab new application did not use canonical revision-zero plan")
	}
	deployed := call("POST", runPath+"/deploy", "", map[string]int64{"expected_revision": 0, "expected_config_revision": 1}, 202)
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=spec WHERE id=$1", deployed["id"]); err != nil {
		t.Fatal(err)
	}
	enqueue := func(id int64, sha string) {
		t.Helper()
		mu.Lock()
		pipelines[id] = gitlabPipeline{ID: id, SHA: sha, Ref: "release", Source: "push", Status: "success"}
		requestIDs[id] = gitlabBuildRequestID(config.ID, id)
		sourceCommits[id] = sha
		mu.Unlock()
		body := store.JSON(map[string]any{"object_kind": "pipeline", "project": map[string]string{"path_with_namespace": "Group/Subgroup/Source"}, "object_attributes": map[string]any{"id": id, "sha": sha, "ref": "release", "source": "push", "status": "success", "tag": false}})
		r := httptest.NewRequest("POST", "http://localhost/api/v1/webhooks/gitlab", bytes.NewReader(body))
		r.Header.Set("X-Gitlab-Event", "Pipeline Hook")
		r.Header.Set("X-Gitlab-Token", string(secret))
		r.Header.Set("X-Gitlab-Event-UUID", fmt.Sprintf("pipeline-completion-%d", id))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatalf("GitLab Pipeline Hook: %d %s", w.Code, w.Body.String())
		}
	}
	mu.Lock()
	sourceSHA = strings.Repeat("b", 40)
	mu.Unlock()
	enqueue(1001, sourceSHA)
	enqueue(1001, sourceSHA)
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	automatic, err := server.readBuildRun(ctx, config.ID, gitlabBuildRequestID(config.ID, 1001))
	if err != nil || automatic.AutoStatus != "deployed" || automatic.DeploymentID == "" {
		t.Fatalf("GitLab auto deploy failed: %+v %v", automatic, err)
	}
	mu.Lock()
	sourceSHA = strings.Repeat("c", 40)
	mu.Unlock()
	enqueue(1002, strings.Repeat("b", 40))
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	stale, _ := server.readBuildRun(ctx, config.ID, gitlabBuildRequestID(config.ID, 1002))
	if stale.AutoStatus != "superseded" {
		t.Fatal("GitLab stale source was deployed")
	}
	mu.Lock()
	ambiguous = true
	mu.Unlock()
	unknown := call("POST", base+"/run", "gitlab-ambiguous-request", map[string]int64{"expected_config_revision": 1}, 202)
	if unknown["status"] != "dispatch_unknown" {
		t.Fatal("ambiguous pipeline creation was not fenced")
	}
	call("POST", base+"/run", "gitlab-ambiguous-request", map[string]int64{"expected_config_revision": 1}, 202)
	mu.Lock()
	ambiguous = false
	mu.Unlock()
	recovered := call("GET", base+"/runs/"+unknown["id"].(string), "", nil, 200)
	if recovered["remote_run_id"] != float64(902) || recovered["image"] == "" {
		t.Fatal("GitLab exact request discovery did not recover ambiguous dispatch")
	}
	mu.Lock()
	dispatchStatus = "running"
	mu.Unlock()
	cancelRun := call("POST", base+"/run", "gitlab-cancel-request", map[string]int64{"expected_config_revision": 1}, 202)
	cancelPath := base + "/runs/" + cancelRun["id"].(string)
	call("POST", cancelPath+"/cancel", "", map[string]any{}, 202)
	if call("GET", cancelPath, "", nil, 200)["status"] != "cancelled" {
		t.Fatal("GitLab cancellation was not observed")
	}
	enqueue(1003, sourceSHA)
	currentConfig, err := server.readBuild(ctx, config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", currentConfig.GrantID); err != nil {
		t.Fatal(err)
	}
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	revoked, _ := server.readBuildRun(ctx, config.ID, gitlabBuildRequestID(config.ID, 1003))
	if revoked.AutoStatus != "blocked" {
		t.Fatal("GitLab automatic grant revocation was bypassed")
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || dispatches != 3 || cancels != 1 {
		t.Fatalf("unexpected remote fixture mutations: writes%d dispatches%d cancels%d", writes, dispatches, cancels)
	}
	t.Log("real PostgreSQL + local GitLab fixture: unowned CI protection, reviewed install, exact-source idempotent dispatch, bounded/commit-verified artifact, new-app deployment, Pipeline Hook dedup/auto deploy, stale suppression, ambiguous dispatch recovery, cancellation and grant revocation; no external repository writes or pipeline runs")
}
