package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type githubBuildRun struct {
	HeadBranch   string `json:"head_branch"`
	ID           int64  `json:"id"`
	DisplayTitle string `json:"display_title"`
	HeadSHA      string `json:"head_sha"`
	Event        string `json:"event"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HTMLURL      string `json:"html_url"`
}

func (s *Server) readBuildRun(ctx context.Context, build, id string) (buildRun, error) {
	return scanBuildRun(s.Store.Pool.QueryRow(ctx, "SELECT "+buildRunColumns+" FROM build_runs WHERE id=$1 AND build_id=$2", id, build))
}
func (s *Server) observeBuildRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:read")
	if !ok {
		return
	}
	run, err := s.readBuildRun(r.Context(), c.ID, r.PathValue("run"))
	if err != nil {
		authFailure(w, err)
		return
	}
	run, err = s.refreshBuildRun(r.Context(), run)
	if err != nil {
		problem(w, 503, "build_observation_failed", err.Error())
		return
	}
	write(w, 200, run)
}
func (s *Server) refreshBuildRun(ctx context.Context, run buildRun) (buildRun, error) {
	if run.Config.Provider == "gitlab" {
		return s.refreshGitLabBuildRun(ctx, run)
	}
	if run.Status == "completed" && run.Image != "" || run.Status == "failed" || run.Status == "cancelled" {
		return run, nil
	}
	var remote githubBuildRun
	if run.GitHubRunID == 0 {
		var list struct {
			Runs []githubBuildRun `json:"workflow_runs"`
		}
		if err := s.githubGET(ctx, "/repos/"+run.Config.Repository+"/actions/workflows/"+url.PathEscape(path.Base(run.Config.workflowPath()))+"/runs?event=workflow_dispatch&per_page=50", &list); err != nil {
			return run, err
		}
		for _, candidate := range list.Runs {
			if candidate.DisplayTitle == "Hakopod "+run.ID && candidate.Event == "workflow_dispatch" {
				if remote.ID != 0 {
					return run, errors.New("more than one remote run matched this request; inspect GitHub before deploying")
				}
				remote = candidate
			}
		}
		if remote.ID == 0 {
			return run, nil
		}
	} else {
		if err := s.githubGET(ctx, "/repos/"+run.Config.Repository+"/actions/runs/"+strconv.FormatInt(run.GitHubRunID, 10), &remote); err != nil {
			return run, err
		}
	}
	validIdentity := remote.Event == "workflow_dispatch" && remote.DisplayTitle == "Hakopod "+run.ID
	if run.Automatic {
		validIdentity = remote.Event == "push" && remote.DisplayTitle == "Hakopod push "+run.CommitSHA && remote.HeadSHA == run.CommitSHA && remote.HeadBranch == run.Config.Branch
	}
	if remote.ID <= 0 || !validIdentity || !commitPattern.MatchString(remote.HeadSHA) {
		return run, errors.New("GitHub run identity did not match the dispatched build")
	}
	run.GitHubRunID = remote.ID
	run.RemoteRunID = remote.ID
	run.Status = remote.Status
	run.Conclusion = remote.Conclusion
	run.RunURL = "https://github.com/" + run.Config.Repository + "/actions/runs/" + strconv.FormatInt(remote.ID, 10)
	run.Message = "Build execution and logs are provided by GitHub Actions"
	if remote.Status == "completed" {
		if remote.Conclusion == "success" {
			content, status, err := s.workflowContents(ctx, run.Config, remote.HeadSHA)
			text, decodeErr := decodeWorkflow(content)
			if err != nil || status != 200 || decodeErr != nil || text != buildWorkflow(run.Config) {
				return run, errors.New("the completed run did not use the reviewed workflow; its output cannot be deployed")
			}
			image, err := s.readBuildArtifact(ctx, run)
			if err != nil {
				run.Message = "Build finished, but its verified image result is unavailable: " + err.Error()
			} else {
				run.Image = image
				run.Message = "Immutable image digest verified; review and deploy when ready"
			}
		} else if remote.Conclusion == "cancelled" {
			run.Status = "cancelled"
			run.Message = "GitHub Actions cancelled the build"
		} else {
			run.Status = "failed"
			run.Message = "GitHub Actions build failed: " + remote.Conclusion
		}
	}
	_, err := s.Store.Pool.Exec(ctx, "UPDATE build_runs SET status=$2,github_run_id=$3,conclusion=$4,image=$5,run_url=$6,message=$7,updated_at=now() WHERE id=$1", run.ID, run.Status, run.GitHubRunID, run.Conclusion, run.Image, run.RunURL, run.Message)
	run.UpdatedAt = time.Now().UTC()
	return run, err
}

type buildArtifactResult struct {
	BuildID   string `json:"build_id"`
	RequestID string `json:"request_id"`
	CommitSHA string `json:"commit_sha"`
	Image     string `json:"image"`
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func parseBuildArtifact(data []byte, run buildRun) (string, error) {
	if len(data) > 512<<10 {
		return "", errors.New("artifact archive exceeds 512 KiB")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 8 {
		return "", errors.New("invalid build artifact archive")
	}
	var resultFile *zip.File
	for _, file := range archive.File {
		if file.Name == "result.json" {
			if resultFile != nil {
				return "", errors.New("duplicate build result")
			}
			resultFile = file
		}
	}
	if resultFile == nil || resultFile.UncompressedSize64 > 32<<10 {
		return "", errors.New("build result missing or too large")
	}
	reader, err := resultFile.Open()
	if err != nil {
		return "", errors.New("invalid build result")
	}
	defer reader.Close()
	data, err = io.ReadAll(io.LimitReader(reader, (32<<10)+1))
	if err != nil {
		return "", errors.New("invalid build result contents")
	}
	return parseBuildResultJSON(data, run)
}
func parseBuildResultJSON(data []byte, run buildRun) (string, error) {
	if len(data) > 32<<10 {
		return "", errors.New("build result exceeds 32 KiB")
	}
	var result buildArtifactResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return "", errors.New("invalid build result JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", errors.New("build result contains extra content")
	}
	prefix := run.Config.imageName() + "@"
	if result.BuildID != run.BuildID || result.RequestID != run.ID || result.CommitSHA != run.CommitSHA || !strings.HasPrefix(result.Image, prefix) || !digestPattern.MatchString(strings.TrimPrefix(result.Image, prefix)) {
		return "", errors.New("build result did not match its immutable source and image repository")
	}
	return result.Image, nil
}
func (s *Server) readBuildArtifact(ctx context.Context, run buildRun) (string, error) {
	var listing struct {
		Artifacts []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Expired bool   `json:"expired"`
			Size    int64  `json:"size_in_bytes"`
		} `json:"artifacts"`
	}
	if err := s.githubGET(ctx, "/repos/"+run.Config.Repository+"/actions/runs/"+strconv.FormatInt(run.GitHubRunID, 10)+"/artifacts?per_page=30", &listing); err != nil {
		return "", err
	}
	var artifactID int64
	for _, a := range listing.Artifacts {
		if a.Name == "hakopod-result-"+run.ID && !a.Expired && a.Size <= 512<<10 {
			if artifactID != 0 {
				return "", errors.New("ambiguous build artifacts")
			}
			artifactID = a.ID
		}
	}
	if artifactID == 0 {
		return "", errors.New("result artifact is absent, expired or exceeds the size limit")
	}
	response, err := s.githubBuildRequest(ctx, "GET", "/repos/"+run.Config.Repository+"/actions/artifacts/"+strconv.FormatInt(artifactID, 10)+"/zip", nil)
	if err != nil {
		return "", err
	}
	if response.StatusCode == 302 {
		location := response.Header.Get("Location")
		response.Body.Close()
		u, err := url.Parse(location)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !(strings.HasSuffix(u.Hostname(), ".blob.core.windows.net") || strings.HasSuffix(u.Hostname(), ".actions.githubusercontent.com") || strings.HasSuffix(u.Hostname(), ".githubusercontent.com")) {
			return "", errors.New("GitHub returned an unsupported artifact storage URL")
		}
		request, err := http.NewRequestWithContext(ctx, "GET", location, nil)
		if err != nil {
			return "", err
		}
		response, err = (&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
		if err != nil {
			return "", errors.New("artifact storage is unavailable")
		}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("artifact download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > 512<<10 {
		return "", errors.New("artifact archive exceeds 512 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 {
		return "", errors.New("artifact archive exceeds supported bounds")
	}
	return parseBuildArtifact(data, run)
}
func (s *Server) cancelBuildRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizedBuild(w, r, "deployments:write")
	if !ok {
		return
	}
	run, err := s.readBuildRun(r.Context(), c.ID, r.PathValue("run"))
	if err != nil {
		authFailure(w, err)
		return
	}
	if run.Config.Provider == "gitlab" {
		s.cancelGitLabBuildRun(w, r, run)
		return
	}
	if run.GitHubRunID == 0 {
		problem(w, 409, "run_not_observed", "refresh the build until its GitHub run is observed before cancelling")
		return
	}
	response, err := s.githubBuildRequest(r.Context(), "POST", "/repos/"+run.Config.Repository+"/actions/runs/"+strconv.FormatInt(run.GitHubRunID, 10)+"/cancel", map[string]any{})
	if err != nil {
		problem(w, 503, "cancel_unknown", err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 202 {
		problem(w, 409, "cancel_failed", "GitHub did not accept cancellation; refresh the run status")
		return
	}
	_, err = s.Store.Pool.Exec(r.Context(), "UPDATE build_runs SET status='cancelling',message='Cancellation requested from GitHub Actions',updated_at=now() WHERE id=$1", run.ID)
	if err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "cancelling"})
}
