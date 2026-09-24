package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func (c buildConfig) gitlabProjectPath() string { return "/projects/" + url.PathEscape(c.Repository) }
func (s *Server) gitlabBuildRequest(ctx context.Context, method, endpoint string, body any, connections ...string) (*http.Response, error) {
	credentials, err := s.connectionCredentials(ctx, "gitlab", selectedGitConnection("gitlab", connections...), "", nil)
	if err != nil {
		return nil, err
	}
	if len(credentials["token"]) == 0 {
		return nil, errors.New("configure a GitLab integration token before installing or running builds")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(store.JSON(body))
	}
	base := "https://gitlab.com/api/v4"
	if s.gitlabAPIURL != "" {
		base = s.gitlabAPIURL
	}
	request, err := http.NewRequestWithContext(ctx, method, base+endpoint, reader)
	if err != nil {
		return nil, err
	}
	if string(credentials["auth-kind"]) == "gitlab_oauth" {
		if method != "GET" && !gitScopeAllows(string(credentials["scopes"]), "api") {
			return nil, errors.New("this GitLab OAuth connection needs api scope to install or run builds")
		}
		request.Header.Set("Authorization", "Bearer "+string(credentials["token"]))
	} else {
		request.Header.Set("PRIVATE-TOKEN", string(credentials["token"]))
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "hakopod")
	client := s.gitlabHTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("GitLab request did not complete; refresh its observed state before retrying")
	}
	return response, nil
}

type gitlabCIFile struct {
	Content    string `json:"content"`
	Encoding   string `json:"encoding"`
	LastCommit string `json:"last_commit_id"`
	Commit     string `json:"commit_id"`
	Size       int    `json:"size"`
}

func (s *Server) gitlabCIContents(ctx context.Context, c buildConfig, ref string) (string, gitlabCIFile, int, error) {
	response, err := s.gitlabBuildRequest(ctx, "GET", c.gitlabProjectPath()+"/repository/files/"+url.PathEscape(c.workflowPath())+"?ref="+url.QueryEscape(ref), nil, c.ConnectionID)
	if err != nil {
		return "", gitlabCIFile{}, 0, err
	}
	defer response.Body.Close()
	var file gitlabCIFile
	if response.StatusCode != 200 {
		return "", file, response.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(data) > 128<<10 || json.Unmarshal(data, &file) != nil || file.Encoding != "base64" || file.Size < 0 || file.Size > 64<<10 {
		return "", file, 200, errors.New("GitLab CI content exceeds supported bounds")
	}
	decoded, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil || len(decoded) > 64<<10 {
		return "", file, 200, errors.New("GitLab returned invalid CI content")
	}
	if commitPattern.MatchString(ref) && file.Commit != ref {
		return "", file, 200, errors.New("GitLab CI was not returned at the reviewed commit")
	}
	return string(decoded), file, 200, nil
}
func (s *Server) gitlabDefaultBranch(ctx context.Context, c buildConfig) (string, error) {
	var project struct {
		DefaultBranch string `json:"default_branch"`
		CIConfigPath  string `json:"ci_config_path"`
	}
	if err := s.gitlabGET(ctx, c.gitlabProjectPath(), &project, c.ConnectionID); err != nil {
		return "", err
	}
	if project.DefaultBranch == "" || len(project.DefaultBranch) > 200 {
		return "", errors.New("GitLab project has no usable default branch")
	}
	if project.CIConfigPath != "" && project.CIConfigPath != c.workflowPath() {
		return "", errors.New("GitLab project uses a custom CI configuration path; select the default .gitlab-ci.yml entrypoint before installing Hakopod builds")
	}
	return project.DefaultBranch, nil
}
func (s *Server) gitlabBuildCommit(ctx context.Context, c buildConfig, ref string) (string, error) {
	var commit struct {
		ID string `json:"id"`
	}
	if err := s.gitlabGET(ctx, c.gitlabProjectPath()+"/repository/commits/"+url.PathEscape(ref), &commit, c.ConnectionID); err != nil {
		return "", err
	}
	if !commitPattern.MatchString(commit.ID) {
		return "", errors.New("GitLab returned an invalid source commit")
	}
	return commit.ID, nil
}
func (s *Server) prepareGitLabBuildDispatch(ctx context.Context, c buildConfig, ref string) (string, string, error) {
	commit, err := s.gitlabBuildCommit(ctx, c, ref)
	if err != nil {
		return "", "", err
	}
	branch, err := s.gitlabDefaultBranch(ctx, c)
	if err != nil {
		return "", "", err
	}
	text, _, status, err := s.gitlabCIContents(ctx, c, branch)
	if err != nil {
		return "", "", err
	}
	if status != 200 || text != buildWorkflow(c) {
		return "", "", errors.New("the installed GitLab CI entrypoint changed; review and install the current workflow")
	}
	return commit, branch, nil
}
func (s *Server) installGitLabBuild(w http.ResponseWriter, r *http.Request, c buildConfig) {
	branch, err := s.gitlabDefaultBranch(r.Context(), c)
	if err != nil {
		problem(w, 409, "gitlab_unavailable", err.Error())
		return
	}
	text, current, status, err := s.gitlabCIContents(r.Context(), c, branch)
	if err != nil {
		problem(w, 409, "gitlab_unavailable", err.Error())
		return
	}
	if status != 200 && status != 404 {
		problem(w, 409, "workflow_access", "GitLab denied reading the CI entrypoint; verify repository permissions")
		return
	}
	if status == 200 && !strings.HasPrefix(text, "# Managed by Hakopod build "+c.ID+".") {
		problem(w, 409, "unowned_workflow", "This GitLab repository already has unowned CI or another build's entrypoint. Hakopod supports one managed build per repository and will not overwrite it.")
		return
	}
	if status == 200 && !commitPattern.MatchString(current.LastCommit) {
		problem(w, 409, "workflow_conflict", "GitLab did not return an immutable file revision")
		return
	}
	// GitLab rejects an update with identical contents as an empty commit. A
	// verified matching file also recovers a write whose response or DB update
	// was interrupted, without creating another repository mutation.
	installed, file := text, current
	if status != 200 || text != buildWorkflow(c) {
		method := "POST"
		body := map[string]any{"branch": branch, "content": buildWorkflow(c), "commit_message": "Install reviewed Hakopod build " + c.ID}
		if status == 200 {
			method = "PUT"
			body["last_commit_id"] = current.LastCommit
		}
		response, err := s.gitlabBuildRequest(r.Context(), method, c.gitlabProjectPath()+"/repository/files/"+url.PathEscape(c.workflowPath()), body, c.ConnectionID)
		if err != nil {
			problem(w, 503, "install_unknown", err.Error())
			return
		}
		response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 201 {
			problem(w, 409, "install_failed", fmt.Sprintf("GitLab returned HTTP %d; verify file revision, API permissions and branch protection", response.StatusCode))
			return
		}
		installed, file, status, err = s.gitlabCIContents(r.Context(), c, branch)
	}
	if err != nil || status != 200 || installed != buildWorkflow(c) || !commitPattern.MatchString(file.LastCommit) {
		problem(w, 503, "install_unknown", "GitLab accepted the file write but its committed contents could not be verified; inspect the repository")
		return
	}
	tag, err := s.Store.Pool.Exec(r.Context(), "UPDATE build_configs SET installed_revision=$2,installed_commit=$3,updated_at=now() WHERE id=$1 AND revision=$2", c.ID, c.Revision, file.LastCommit)
	if err != nil {
		authFailure(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		problem(w, 409, "build_conflict", "Build settings changed during installation; review and install the current version")
		return
	}
	_, _ = s.Store.Pool.Exec(r.Context(), "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'build.workflow.install',$3)", who(r).ID, who(r).KeyID, c.ID)
	c.InstalledRevision = c.Revision
	c.InstalledCommit = file.LastCommit
	write(w, 200, map[string]any{"config": c, "commit_sha": file.LastCommit, "workflow_branch": branch, "workflow_path": c.workflowPath()})
}

type gitlabPipeline struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Source string `json:"source"`
	Status string `json:"status"`
}

func (s *Server) dispatchGitLabBuild(ctx context.Context, c buildConfig, branch, commit, requestID string) (string, string, int64) {
	variables := []map[string]string{{"key": "HAKOPOD_BUILD_ID", "value": c.ID, "variable_type": "env_var"}, {"key": "HAKOPOD_REQUEST_ID", "value": requestID, "variable_type": "env_var"}, {"key": "HAKOPOD_SOURCE_SHA", "value": commit, "variable_type": "env_var"}}
	response, err := s.gitlabBuildRequest(ctx, "POST", c.gitlabProjectPath()+"/pipeline", map[string]any{"ref": branch, "variables": variables}, c.ConnectionID)
	if err != nil {
		return "dispatch_unknown", "Pipeline response was interrupted. Refresh to locate the remote pipeline before creating another request.", 0
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout {
		return "dispatch_unknown", "GitLab returned an interrupted or unavailable response. Refresh to locate the remote pipeline before creating another request.", 0
	}
	if response.StatusCode != 201 {
		return "failed", fmt.Sprintf("GitLab denied pipeline creation (HTTP %d); check CI configuration, pipeline variable permissions and runner availability", response.StatusCode), 0
	}
	var pipeline gitlabPipeline
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&pipeline) != nil || pipeline.ID <= 0 || !commitPattern.MatchString(pipeline.SHA) {
		return "dispatch_unknown", "GitLab accepted pipeline creation but returned an invalid result; refresh before another request", 0
	}
	return "queued", "Waiting for the reviewed GitLab CI job", pipeline.ID
}
