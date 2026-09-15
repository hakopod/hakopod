package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
	"go.yaml.in/yaml/v3"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func buildZip(t *testing.T, value any) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	file, err := archive.Create("result.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(store.JSON(value)); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func TestBuildWorkflowArchitecturePinsAndArtifactBounds(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, mode := range []string{"dockerfile", "buildpacks"} {
			for _, preset := range []string{"auto", "go"} {
				c := buildConfig{BuildArgs: map[string]string{"NEXT_PUBLIC_API_URL": "https://api.example.com", "PUBLIC_LABEL": "test with spaces and ' quotes"}, ID: strings.Repeat("a", 32), Repository: "example/source", Branch: "main", Mode: mode, Preset: preset, ContextPath: "app", Dockerfile: "app/Dockerfile", Architecture: architecture, AutoBuild: true}
				workflow := buildWorkflow(c)
				if strings.Contains(workflow, "{{BUILD_") || strings.Contains(workflow, "{{PACK_") {
					t.Fatal("workflow contains unresolved template tokens")
				}
				var parsed struct {
					On   map[string]any `yaml:"on"`
					Jobs map[string]struct {
						Runner string `yaml:"runs-on"`
						Steps  []struct {
							Uses string `yaml:"uses"`
							Run  string `yaml:"run"`
						} `yaml:"steps"`
					} `yaml:"jobs"`
				}
				if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
					t.Fatal(err)
				}
				if parsed.On["push"] == nil || parsed.On["workflow_dispatch"] == nil {
					t.Fatal("reviewed automatic/manual triggers missing")
				}
				want := "ubuntu-24.04"
				if architecture == "arm64" {
					want += "-arm"
				}
				job := parsed.Jobs["build"]
				if job.Runner != want {
					t.Fatal("wrong runner architecture")
				}
				for _, step := range job.Steps {
					if step.Uses != "" {
						parts := strings.Split(step.Uses, "@")
						if len(parts) != 2 || len(parts[1]) != 40 {
							t.Fatal("action not pinned to immutable commit")
						}
					}
					if step.Run != "" {
						command := exec.Command("bash", "-n")
						command.Stdin = strings.NewReader(step.Run)
						if output, err := command.CombinedOutput(); err != nil {
							t.Fatalf("generated shell invalid: %s", output)
						}
					}
				}
				if mode == "buildpacks" && !strings.Contains(workflow, "builder-jammy-buildpackless-base@sha256:") {
					t.Fatal("buildpacks did not use verified multi-architecture builder")
				}
			}
		}
	}
	config := buildConfig{ID: strings.Repeat("a", 32), Repository: "example/source"}
	run := buildRun{ID: strings.Repeat("b", 32), BuildID: config.ID, CommitSHA: strings.Repeat("c", 40), Config: config}
	result := buildArtifactResult{BuildID: run.BuildID, RequestID: run.ID, CommitSHA: run.CommitSHA, Image: config.imageName() + "@sha256:" + strings.Repeat("d", 64)}
	if image, err := parseBuildArtifact(buildZip(t, result), run); err != nil || image != result.Image {
		t.Fatal("valid artifact rejected")
	}
	result.CommitSHA = strings.Repeat("e", 40)
	if _, err := parseBuildArtifact(buildZip(t, result), run); err == nil {
		t.Fatal("artifact from another source accepted")
	}
	if _, err := parseBuildArtifact(make([]byte, (512<<10)+1), run); err == nil {
		t.Fatal("oversized archive accepted")
	}
}

func TestSourceBuildNewApplicationManualAndAutomatic(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Build Installer", "builder@example.test", "build owner password long", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	workflow := ""
	sourceSHA := strings.Repeat("a", 40)
	config := buildConfig{}
	remote := map[int64]githubBuildRun{}
	requestIDs := map[int64]string{}
	dispatches, writes := 0, 0
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer local-github-token" {
			t.Error("missing local fixture GitHub authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/source":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/example/source/commits/"):
			json.NewEncoder(w).Encode(map[string]string{"sha": sourceSHA})
		case strings.Contains(r.URL.Path, "/contents/") && r.Method == "GET":
			if workflow == "" {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"sha": "workflow-blob", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(workflow))})
		case strings.Contains(r.URL.Path, "/contents/") && r.Method == "PUT":
			var body struct{ Content, Branch string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			decoded, err := base64.StdEncoding.DecodeString(body.Content)
			if err != nil {
				t.Error(err)
			}
			workflow = string(decoded)
			writes++
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": strings.Repeat("f", 40)}})
		case strings.HasSuffix(r.URL.Path, "/dispatches") && r.Method == "POST":
			var body struct {
				Ref    string            `json:"ref"`
				Inputs map[string]string `json:"inputs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Inputs["commit"] != sourceSHA {
				t.Error("dispatch did not pin source commit")
			}
			dispatches++
			requestIDs[111] = body.Inputs["request_id"]
			remote[111] = githubBuildRun{ID: 111, DisplayTitle: "Hakopod " + requestIDs[111], HeadSHA: strings.Repeat("f", 40), HeadBranch: "main", Event: "workflow_dispatch", Status: "completed", Conclusion: "success"}
			w.WriteHeader(204)
		case strings.Contains(r.URL.Path, "/actions/workflows/") && strings.HasSuffix(r.URL.Path, "/runs"):
			runs := []githubBuildRun{}
			for _, v := range remote {
				runs = append(runs, v)
			}
			json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		case strings.HasSuffix(r.URL.Path, "/artifacts"):
			pieces := strings.Split(r.URL.Path, "/")
			id, _ := strconv.ParseInt(pieces[len(pieces)-2], 10, 64)
			json.NewEncoder(w).Encode(map[string]any{"artifacts": []map[string]any{{"id": id + 1000, "name": "hakopod-result-" + requestIDs[id], "expired": false, "size_in_bytes": 500}}})
		case strings.HasSuffix(r.URL.Path, "/zip"):
			pieces := strings.Split(r.URL.Path, "/")
			artifact, _ := strconv.ParseInt(pieces[len(pieces)-2], 10, 64)
			id := artifact - 1000
			sha := sourceSHA
			if remote[id].Event == "push" {
				sha = remote[id].HeadSHA
			}
			result := buildArtifactResult{BuildID: config.ID, RequestID: requestIDs[id], CommitSHA: sha, Image: config.imageName() + "@sha256:" + strings.Repeat("d", 64)}
			w.Header().Set("Content-Type", "application/zip")
			w.Write(buildZip(t, result))
		case strings.Contains(r.URL.Path, "/actions/runs/"):
			id, _ := strconv.ParseInt(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], 10, 64)
			json.NewEncoder(w).Encode(remote[id])
		default:
			http.NotFound(w, r)
		}
	}))
	defer github.Close()
	server := &Server{Store: db, Auth: AuthConfig{PublicURL: "http://localhost:4173"}, githubAPIURL: github.URL, githubTestCredentials: func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"token": []byte("local-github-token"), "webhook-secret": bytes.Repeat([]byte("w"), 32)}, nil
	}}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	call := func(method, path, idem string, body any, want int) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(store.JSON(body))
		}
		request, _ := http.NewRequest(method, httpServer.URL+"/api/v1"+path, reader)
		request.Header.Set("Authorization", "Bearer "+session.Token)
		request.Header.Set("Content-Type", "application/json")
		if idem != "" {
			request.Header.Set("Idempotency-Key", idem)
		}
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out map[string]any
		if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s expected %d got %d: %v", method, path, want, response.StatusCode, out)
		}
		return out
	}
	created := call("POST", "/builds", "", map[string]any{"project": "demo", "environment": "development", "name": "built-app", "service": "web", "repository": "example/source", "branch": "main", "mode": "buildpacks", "preset": "go", "architecture": "arm64", "auto_build": true, "auto_deploy": true, "port": 8080, "size": "small"}, 201)
	encoded := store.JSON(created)
	if err = json.Unmarshal(encoded, &config); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 0 {
		t.Fatal("creating a build created a placeholder application image")
	}
	base := "/builds/" + config.ID
	preview := call("POST", base+"/preview", "", map[string]any{}, 200)
	if !strings.Contains(preview["workflow"].(string), "ubuntu-24.04-arm") || strings.Contains(preview["workflow"].(string), "local-github-token") {
		t.Fatal("workflow leaked token or chose wrong architecture")
	}
	if writes != 0 {
		t.Fatal("preview wrote to repository")
	}
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 0}, 409)
	call("POST", base+"/install", "", map[string]int64{"expected_config_revision": 1}, 200)
	if writes != 1 {
		t.Fatal("explicit install did not write exactly once to local fixture")
	}
	first := call("POST", base+"/run", "manual-build-001", map[string]int64{"expected_config_revision": 1}, 202)
	again := call("POST", base+"/run", "manual-build-001", map[string]int64{"expected_config_revision": 1}, 202)
	if first["id"] != again["id"] || dispatches != 1 {
		t.Fatal("build retry dispatched duplicate remote execution")
	}
	call("POST", base+"/run", "manual-build-001", map[string]any{"expected_config_revision": 1, "commit": strings.Repeat("b", 40)}, 409)
	runID := first["id"].(string)
	observed := call("GET", base+"/runs/"+runID, "", nil, 200)
	if observed["image"] == "" || observed["status"] != "completed" {
		t.Fatal("successful immutable artifact was not observed")
	}
	plan := call("POST", base+"/runs/"+runID+"/plan", "", map[string]any{}, 200)
	if plan["expected_revision"] != float64(0) || plan["expected_config_revision"] != float64(1) {
		t.Fatal("new application plan did not start at revision zero")
	}
	deployed := call("POST", base+"/runs/"+runID+"/deploy", "", map[string]int64{"expected_revision": 0, "expected_config_revision": 1}, 202)
	same := call("POST", base+"/runs/"+runID+"/deploy", "", map[string]int64{"expected_revision": 0, "expected_config_revision": 1}, 202)
	if same["id"] != deployed["id"] {
		t.Fatal("deployment retry did not retain operation")
	}
	var application spec.Application
	if err = json.Unmarshal(store.JSON(deployed["spec"]), &application); err != nil {
		t.Fatal(err)
	}
	if application.Services["web"].Architecture != "arm64" {
		t.Fatal("built image architecture was not preserved in runtime placement")
	}
	web := application.Services["web"]
	web.Env = map[string]string{"EXISTING": "kept"}
	web.Command = []string{"uvicorn"}
	web.Args = []string{"main:app", "--host", "0.0.0.0"}
	application.Services["web"] = web
	application.Services["sidecar"] = spec.Service{Image: "python:3.13-alpine", Size: "small"}
	application, err = spec.Normalize(application)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := spec.Normalize(application)
	sidecar := resolved.Services["sidecar"]
	sidecar.Image = "python@sha256:" + strings.Repeat("9", 64)
	resolved.Services["sidecar"] = sidecar
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET spec=$2,resolved_spec=$3,status='succeeded' WHERE id=$1", deployed["id"], store.JSON(application), store.JSON(resolved)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE applications SET spec=$2,status='healthy' WHERE id=$1", deployed["application_id"], store.JSON(application)); err != nil {
		t.Fatal(err)
	}
	{
		// Linked builds preserve overrides unless an explicit replacement or reset is saved.
		currentConfig, err := server.readBuild(ctx, config.ID)
		if err != nil {
			t.Fatal(err)
		}
		currentRun, err := server.readBuildRun(ctx, config.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		next, _, err := server.prepareBuildSpec(ctx, currentConfig, currentRun)
		if err != nil || strings.Join(next.Services["web"].Command, " ") != "uvicorn" || strings.Join(next.Services["web"].Args, " ") != "main:app --host 0.0.0.0" {
			t.Fatal("linked runtime override lost", err)
		}
		if next.Services["web"].Env["EXISTING"] != "kept" {
			t.Fatal("omitted runtime env replaced existing variables")
		}
		replacement := map[string]string{"NEW": "value", "EMPTY": ""}
		currentConfig.Env = &replacement
		next, _, err = server.prepareBuildSpec(ctx, currentConfig, currentRun)
		if err != nil || next.Services["web"].Env["NEW"] != "value" || len(next.Services["web"].Env) != 2 {
			t.Fatal("runtime env replacement failed", err)
		}
		next.Services["web"].Env["NEW"] = "changed"
		if replacement["NEW"] != "value" {
			t.Fatal("deployment mutated stored build configuration")
		}
		emptyEnv := map[string]string{}
		currentConfig.Env = &emptyEnv
		next, _, err = server.prepareBuildSpec(ctx, currentConfig, currentRun)
		if err != nil || len(next.Services["web"].Env) != 0 {
			t.Fatal("explicit empty runtime env did not clear variables", err)
		}
		override := []string{"python", "-m", "uvicorn"}
		args := []string{"other:app", "--port", "8000"}
		currentConfig.Command, currentConfig.Args = &override, &args
		next, _, err = server.prepareBuildSpec(ctx, currentConfig, currentRun)
		if err != nil || strings.Join(next.Services["web"].Command, " ") != "python -m uvicorn" || strings.Join(next.Services["web"].Args, " ") != "other:app --port 8000" {
			t.Fatal("linked command not replaced", err)
		}
		empty := []string{}
		currentConfig.Command, currentConfig.Args = &empty, &empty
		next, _, err = server.prepareBuildSpec(ctx, currentConfig, currentRun)
		if err != nil || len(next.Services["web"].Command) != 0 || len(next.Services["web"].Args) != 0 {
			t.Fatal("linked image defaults not restored", err)
		}
	}
	enqueue := func(id int64, sha string) {
		t.Helper()
		mu.Lock()
		remote[id] = githubBuildRun{ID: id, DisplayTitle: "Hakopod push " + sha, HeadSHA: sha, HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "success"}
		requestIDs[id] = fmt.Sprintf("%032x", id)
		mu.Unlock()
		body := store.JSON(map[string]any{"action": "completed", "repository": map[string]string{"full_name": "example/source"}, "workflow_run": map[string]any{"id": id, "name": "Hakopod build " + config.ID, "event": "push", "head_sha": sha, "head_branch": "main", "conclusion": "success"}})
		request, _ := http.NewRequest("POST", httpServer.URL+"/api/v1/webhooks/github", bytes.NewReader(body))
		mac := hmac.New(sha256.New, bytes.Repeat([]byte("w"), 32))
		mac.Write(body)
		request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		request.Header.Set("X-GitHub-Event", "workflow_run")
		request.Header.Set("X-GitHub-Delivery", fmt.Sprintf("build-completion-%d", id))
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 202 {
			t.Fatalf("signed build webhook returned %d", response.StatusCode)
		}
	}
	mu.Lock()
	sourceSHA = strings.Repeat("b", 40)
	mu.Unlock()
	enqueue(112, sourceSHA)
	enqueue(112, sourceSHA)
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	automatic, err := server.readBuildRun(ctx, config.ID, fmt.Sprintf("%032x", 112))
	if err != nil {
		t.Fatal(err)
	}
	if automatic.AutoStatus != "deployed" || automatic.DeploymentID == "" {
		t.Fatalf("automatic build not deployed: %+v", automatic)
	}
	autoDep, err := db.Deployment(ctx, automatic.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if autoDep.Spec.Services["sidecar"].Image != sidecar.Image {
		t.Fatal("automatic image update repinned an unaffected service")
	}
	// Simulate a process dying after accepting a release but before linking it
	// to the inbox record. Recovery sees the newer application revision.
	if _, err = db.Pool.Exec(ctx, "UPDATE build_runs SET deployment_id='',auto_status='queued',next_attempt_at=now() WHERE id=$1", automatic.ID); err != nil {
		t.Fatal(err)
	}
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := server.readBuildRun(ctx, config.ID, automatic.ID)
	if err != nil || replayed.DeploymentID != automatic.DeploymentID || replayed.AutoStatus != "deployed" {
		t.Fatalf("crash recovery did not reuse the accepted automatic deployment: %+v, %v", replayed, err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM deployments").Scan(&count); err != nil || count != 2 {
		t.Fatalf("automatic replay created another deployment: count=%d error=%v", count, err)
	}
	mu.Lock()
	sourceSHA = strings.Repeat("c", 40)
	mu.Unlock()
	enqueue(113, strings.Repeat("b", 40))
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	stale, _ := server.readBuildRun(ctx, config.ID, fmt.Sprintf("%032x", 113))
	if stale.AutoStatus != "superseded" || stale.DeploymentID != "" {
		t.Fatalf("stale source build was not suppressed: %+v", stale)
	}
	enqueue(114, sourceSHA)
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
	revoked, _ := server.readBuildRun(ctx, config.ID, fmt.Sprintf("%032x", 114))
	if revoked.AutoStatus != "blocked" {
		t.Fatal("revoked grant did not stop automatic deployment")
	}
	t.Log("real PostgreSQL + local GitHub fixture: image-free application setup, review-only preview, explicit workflow install, idempotent pinned source dispatch, verified ZIP image result, canonical plan/new-app deploy, signed completion inbox, automatic deploy, unaffected digests, stale-source suppression and grant revocation passed; no external repository writes or workflow runs")
}

func TestBuildInboxCapacityRetentionAndExhaustedRecovery(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "build-queue-test")
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte("w"), 32)
	server := &Server{Store: db, githubTestCredentials: func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"webhook-secret": secret}, nil
	}}
	handler := server.Handler()
	request := httptest.NewRequest("POST", "http://localhost/api/v1/builds", bytes.NewReader(store.JSON(buildInput{Project: "demo", Environment: "development", Name: "queue-test", Repository: "Example/Source", Architecture: "arm64", AutoBuild: true})))
	request.Header.Set("Authorization", "Bearer "+raw)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatalf("create build: %d %s", response.Code, response.Body.String())
	}
	var config buildConfig
	if err = json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config.Repository != "example/source" {
		t.Fatal("repository name was not normalized")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE build_configs SET installed_revision=revision WHERE id=$1", config.ID); err != nil {
		t.Fatal(err)
	}
	config, err = server.readBuild(ctx, config.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO build_runs(id,build_id,identity_id,key_id,idempotency_key,request_hash,config,config_revision,commit_sha,github_run_id,automatic,auto_status,next_attempt_at) SELECT lpad(to_hex(n),32,'0'),$1,identity_id,id,'seed-'||n,decode(repeat('a',64),'hex'),$3,1,$4,n,true,'queued',now()+interval '1 day' FROM api_keys CROSS JOIN generate_series(1,999) AS n WHERE id=$2`, config.ID, config.GrantID, store.JSON(config), commit); err != nil {
		t.Fatal(err)
	}
	body := func(id int64) []byte {
		return store.JSON(map[string]any{"action": "completed", "repository": map[string]string{"full_name": "Example/Source"}, "workflow_run": map[string]any{"id": id, "name": "Hakopod build " + config.ID, "event": "push", "head_sha": commit, "head_branch": config.Branch, "conclusion": "success"}})
	}
	results := make(chan error, 8)
	for id := int64(2000); id < 2008; id++ {
		go func(id int64) {
			results <- server.enqueueBuildWebhook(ctx, "workflow_run", body(id), fmt.Sprintf("completion-%d", id))
		}(id)
	}
	accepted, full := 0, 0
	for range 8 {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, errBuildQueueFull):
			full++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || full != 7 {
		t.Fatalf("concurrent queue capacity failed: accepted=%d full=%d", accepted, full)
	}
	if err = server.enqueueBuildWebhook(ctx, "workflow_run", body(1), "repeated-delivery"); err != nil {
		t.Fatalf("full queue rejected an already durable run: %v", err)
	}
	webhookBody := body(3000)
	request = httptest.NewRequest("POST", "http://localhost/api/v1/webhooks/github", bytes.NewReader(webhookBody))
	mac := hmac.New(sha256.New, secret)
	mac.Write(webhookBody)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-GitHub-Event", "workflow_run")
	request.Header.Set("X-GitHub-Delivery", "saturated-delivery")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 503 || response.Header().Get("Retry-After") != "30" || !strings.Contains(response.Body.String(), "queue_full") {
		t.Fatalf("full webhook queue did not return bounded retry: %d %s", response.Code, response.Body.String())
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE build_runs SET next_attempt_at=now()+interval '1 day'"); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id     int
		update string
	}{
		{1, "created_at=now()-interval '31 days'"},
		{2, "created_at=now()-interval '31 days',auto_status='processing',attempts=4,updated_at=now()-interval '3 minutes'"},
		{3, "created_at=now()-interval '31 days',auto_status='ready',status='completed'"},
		{4, "created_at=now()-interval '31 days',auto_status='deployed',status='completed',deployment_id='durable-release'"},
		{5, "created_at=now()-interval '31 days',automatic=false,auto_status='',status='dispatch_unknown'"},
		{6, "attempts=5"},
		{7, "auto_status='processing',attempts=5,updated_at=now()-interval '3 minutes'"},
	} {
		if _, err = db.Pool.Exec(ctx, "UPDATE build_runs SET "+fixture.update+" WHERE id=$1", fmt.Sprintf("%032x", fixture.id)); err != nil {
			t.Fatal(err)
		}
	}
	if err = server.processBuildQueue(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{1: "queued", 2: "processing", 4: "deployed", 5: "", 6: "blocked", 7: "blocked"} {
		run, err := server.readBuildRun(ctx, config.ID, fmt.Sprintf("%032x", id))
		if err != nil || run.AutoStatus != want {
			t.Fatalf("retention/recovery row %d: status=%q want=%q error=%v", id, run.AutoStatus, want, err)
		}
	}
	var remaining int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM build_runs WHERE id=$1", fmt.Sprintf("%032x", 3)).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("expired unused terminal build was not pruned")
	}
	t.Log("1,000-item global pending cap survives concurrent delivery; duplicates remain idempotent at capacity; pending, ambiguous, and deployed rows survive retention; exhausted crash leases become blocked")
}
