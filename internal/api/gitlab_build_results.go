package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) matchesGitLabManualRun(ctx context.Context, run buildRun, pipeline gitlabPipeline) (bool, error) {
	if pipeline.Source != "api" {
		return false, nil
	}
	var variables []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := s.gitlabGET(ctx, run.Config.gitlabProjectPath()+"/pipelines/"+strconv.FormatInt(pipeline.ID, 10)+"/variables", &variables, run.Config.ConnectionID); err != nil {
		return false, err
	}
	if len(variables) > 128 {
		return false, errors.New("GitLab pipeline variables exceed supported bounds")
	}
	want := map[string]string{"HAKOPOD_BUILD_ID": run.BuildID, "HAKOPOD_REQUEST_ID": run.ID, "HAKOPOD_SOURCE_SHA": run.CommitSHA}
	seen := map[string]bool{}
	for _, v := range variables {
		if expected, ok := want[v.Key]; ok {
			if seen[v.Key] || v.Value != expected {
				return false, nil
			}
			seen[v.Key] = true
		}
	}
	return len(seen) == len(want), nil
}
func (s *Server) gitlabPipelineForRun(ctx context.Context, run buildRun) (gitlabPipeline, error) {
	var pipeline gitlabPipeline
	if run.RemoteRunID != 0 {
		if err := s.gitlabGET(ctx, run.Config.gitlabProjectPath()+"/pipelines/"+strconv.FormatInt(run.RemoteRunID, 10), &pipeline, run.Config.ConnectionID); err != nil {
			return pipeline, err
		}
		if pipeline.ID != run.RemoteRunID {
			return pipeline, errors.New("GitLab pipeline ID did not match the accepted run")
		}
	} else {
		var pipelines []gitlabPipeline
		if err := s.gitlabGET(ctx, run.Config.gitlabProjectPath()+"/pipelines?source=api&order_by=id&sort=desc&per_page=25", &pipelines, run.Config.ConnectionID); err != nil {
			return pipeline, err
		}
		lookups := 0
		for _, candidate := range pipelines {
			if candidate.ID <= 0 || candidate.Source != "api" {
				continue
			}
			lookups++
			if lookups > 10 {
				break
			}
			matches, err := s.matchesGitLabManualRun(ctx, run, candidate)
			if err != nil {
				return pipeline, err
			}
			if matches {
				if pipeline.ID != 0 {
					return pipeline, errors.New("more than one GitLab pipeline matched this request; inspect GitLab before deploying")
				}
				pipeline = candidate
			}
		}
		if pipeline.ID == 0 {
			return pipeline, nil
		}
	}
	if !commitPattern.MatchString(pipeline.SHA) {
		return pipeline, errors.New("GitLab pipeline returned an invalid committed revision")
	}
	if run.Automatic {
		if pipeline.Source != "push" || pipeline.SHA != run.CommitSHA || pipeline.Ref != run.Config.Branch || run.ID != gitlabBuildRequestID(run.BuildID, pipeline.ID) {
			return pipeline, errors.New("GitLab automatic pipeline did not match its source and branch")
		}
	} else {
		matches, err := s.matchesGitLabManualRun(ctx, run, pipeline)
		if err != nil {
			return pipeline, err
		}
		if !matches {
			return pipeline, errors.New("GitLab pipeline variables did not match the exact build request")
		}
	}
	return pipeline, nil
}
func (s *Server) verifyGitLabPipelineWorkflow(ctx context.Context, c buildConfig, pipeline gitlabPipeline) error {
	if _, err := s.gitlabDefaultBranch(ctx, c); err != nil {
		return err
	}
	text, _, status, err := s.gitlabCIContents(ctx, c, pipeline.SHA)
	if err != nil {
		return err
	}
	if status != 200 || text != buildWorkflow(c) {
		return errors.New("the GitLab pipeline did not use the reviewed CI entrypoint; its output cannot be deployed")
	}
	return nil
}
func (s *Server) readGitLabBuildArtifact(ctx context.Context, run buildRun, workflowCommit string) (string, error) {
	var jobs []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
		Pipeline struct {
			ID int64 `json:"id"`
		} `json:"pipeline"`
	}
	if err := s.gitlabGET(ctx, run.Config.gitlabProjectPath()+"/pipelines/"+strconv.FormatInt(run.RemoteRunID, 10)+"/jobs?include_retried=false&per_page=50", &jobs, run.Config.ConnectionID); err != nil {
		return "", err
	}
	if len(jobs) > 50 {
		return "", errors.New("GitLab pipeline job list exceeds supported bounds")
	}
	var jobID int64
	for _, job := range jobs {
		if job.Name != run.Config.gitlabJobName() {
			continue
		}
		if jobID != 0 || job.ID <= 0 || job.Status != "success" || job.Pipeline.ID != run.RemoteRunID || job.Commit.ID != workflowCommit {
			return "", errors.New("GitLab build job is absent, ambiguous or unsuccessful")
		}
		jobID = job.ID
	}
	if jobID == 0 {
		return "", errors.New("GitLab build-result job is missing")
	}
	response, err := s.gitlabBuildRequest(ctx, "GET", run.Config.gitlabProjectPath()+"/jobs/"+strconv.FormatInt(jobID, 10)+"/artifacts/result.json", nil, run.Config.ConnectionID)
	if err != nil {
		return "", err
	}
	if response.StatusCode == 302 || response.StatusCode == 307 {
		location := response.Header.Get("Location")
		response.Body.Close()
		u, err := url.Parse(location)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") || (u.Hostname() != "storage.googleapis.com" && !strings.HasSuffix(u.Hostname(), ".storage.googleapis.com")) {
			return "", errors.New("GitLab returned an unsupported artifact storage URL")
		}
		request, err := http.NewRequestWithContext(ctx, "GET", location, nil)
		if err != nil {
			return "", err
		}
		response, err = (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
		if err != nil {
			return "", errors.New("GitLab artifact storage is unavailable")
		}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("GitLab artifact returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > 32<<10 {
		return "", errors.New("GitLab build result exceeds 32 KiB")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if err != nil {
		return "", errors.New("GitLab build result could not be read")
	}
	return parseBuildResultJSON(body, run)
}
func (s *Server) refreshGitLabBuildRun(parent context.Context, run buildRun) (buildRun, error) {
	if run.Status == "completed" && run.Image != "" || run.Status == "failed" || run.Status == "cancelled" {
		return run, nil
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	pipeline, err := s.gitlabPipelineForRun(ctx, run)
	if err != nil {
		return run, err
	}
	if pipeline.ID == 0 {
		return run, nil
	}
	run.RemoteRunID = pipeline.ID
	run.RunURL = "https://gitlab.com/" + run.Config.Repository + "/-/pipelines/" + strconv.FormatInt(pipeline.ID, 10)
	run.Message = "Build execution and logs are provided by GitLab CI"
	switch pipeline.Status {
	case "success":
		if err = s.verifyGitLabPipelineWorkflow(ctx, run.Config, pipeline); err != nil {
			return run, err
		}
		run.Status = "completed"
		run.Conclusion = "success"
		run.Image, err = s.readGitLabBuildArtifact(ctx, run, pipeline.SHA)
		if err != nil {
			run.Message = "Pipeline finished, but its verified image result is unavailable: " + err.Error()
		} else {
			run.Message = "Immutable image digest verified; review and deploy when ready"
		}
	case "failed", "skipped":
		run.Status = "failed"
		run.Conclusion = pipeline.Status
	case "canceled":
		run.Status = "cancelled"
		run.Conclusion = "cancelled"
	case "running":
		run.Status = "in_progress"
	default:
		run.Status = "queued"
	}
	_, err = s.Store.Pool.Exec(ctx, "UPDATE build_runs SET status=$2,github_run_id=$3,conclusion=$4,image=$5,run_url=$6,message=$7,updated_at=now() WHERE id=$1", run.ID, run.Status, run.RemoteRunID, run.Conclusion, run.Image, run.RunURL, run.Message)
	run.UpdatedAt = time.Now().UTC()
	return run, err
}
func (s *Server) cancelGitLabBuildRun(w http.ResponseWriter, r *http.Request, run buildRun) {
	if run.RemoteRunID == 0 {
		problem(w, 409, "run_not_observed", "refresh the build until its GitLab pipeline is observed before cancelling")
		return
	}
	pipeline, err := s.gitlabPipelineForRun(r.Context(), run)
	if err != nil {
		problem(w, 409, "pipeline_identity", err.Error())
		return
	}
	if err = s.verifyGitLabPipelineWorkflow(r.Context(), run.Config, pipeline); err != nil {
		problem(w, 409, "workflow_changed", err.Error())
		return
	}
	response, err := s.gitlabBuildRequest(r.Context(), "POST", run.Config.gitlabProjectPath()+"/pipelines/"+strconv.FormatInt(run.RemoteRunID, 10)+"/cancel", map[string]any{}, run.Config.ConnectionID)
	if err != nil {
		problem(w, 503, "cancel_unknown", err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 {
		problem(w, 409, "cancel_failed", "GitLab did not accept cancellation; refresh the pipeline status")
		return
	}
	if _, err = s.Store.Pool.Exec(r.Context(), "UPDATE build_runs SET status='cancelling',message='Cancellation requested from GitLab CI',updated_at=now() WHERE id=$1", run.ID); err != nil {
		authFailure(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "cancelling"})
}
