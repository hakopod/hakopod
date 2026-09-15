package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/framework"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) detectBuild(w http.ResponseWriter, r *http.Request) {
	var in buildInput
	if !decode(w, r, &in) {
		return
	}
	c, err := normalizeBuild(in)
	if err != nil {
		authFailure(w, err)
		return
	}
	if !who(r).Allows("deployments:write", c.Project, c.Environment, c.Name) {
		authFailure(w, store.ErrForbidden)
		return
	}
	// Reuse an administrator-approved source binding for this exact application
	// and repository. Never expose installation credentials to arbitrary paths.
	if !who(r).CanManageGit() {
		if c.ApplicationID == "" || s.validateBuildApplication(r.Context(), c) != nil {
			problem(w, 403, "repository_approval_required", "An administrator must approve this application's source repository before detection")
			return
		}
		binding, e := s.readSource(r.Context(), c.ApplicationID)
		if e != nil || binding.Provider != c.Provider || binding.Repository != c.Repository || binding.ConnectionID != c.ConnectionID {
			problem(w, 403, "repository_approval_required", "Detection requires this application's approved repository and Git connection")
			return
		}
	}
	if err = s.validateGitConnection(r.Context(), c.Provider, c.ConnectionID); err != nil {
		authFailure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	commit, err := s.buildDetectionCommit(ctx, c)
	if err != nil {
		problem(w, 400, "source_unavailable", err.Error())
		return
	}
	files := map[string][]byte{}
	// A bounded directory inventory avoids a separate request for each absent
	// lockfile/config. Never traverse subdirectories or execute repository code.
	entries, err := s.buildDetectionFiles(ctx, c, commit)
	if err != nil {
		problem(w, 400, "source_unavailable", err.Error())
		return
	}
	for name := range entries {
		files[name] = nil
	}
	for _, name := range []string{"package.json", "next.config.js", "next.config.mjs", "next.config.ts"} {
		if _, ok := entries[name]; !ok {
			continue
		}
		content, e := s.buildDetectionFile(ctx, c, commit, path.Join(c.ContextPath, name))
		if e != nil {
			problem(w, 400, "source_unavailable", e.Error())
			return
		}
		files[name] = content
	}
	if _, ok := entries["Dockerfile"]; ok {
		write(w, 200, map[string]any{"mode": "dockerfile", "commit_sha": commit, "dockerfile": path.Join(c.ContextPath, "Dockerfile"), "warnings": []string{"The repository already contains a Dockerfile; review it before using automatic framework setup."}})
		return
	}
	detection, err := framework.Detect(files)
	if err != nil {
		problem(w, 400, "framework_not_detected", err.Error())
		return
	}
	recipe, err := framework.Dockerfile(detection.Plan, nil)
	if err != nil {
		authFailure(w, store.ErrInput)
		return
	}
	write(w, 200, map[string]any{"mode": "framework", "commit_sha": commit, "framework": detection.Plan, "dockerfile_content": recipe, "warnings": detection.Warnings})
}
func (s *Server) buildDetectionCommit(ctx context.Context, c buildConfig) (string, error) {
	var sha string
	if c.Provider == "gitlab" {
		var ref struct {
			ID string `json:"id"`
		}
		err := s.gitlabGET(ctx, "/projects/"+url.PathEscape(c.Repository)+"/repository/commits/"+url.PathEscape(c.Branch), &ref, c.ConnectionID)
		if err != nil {
			return "", err
		}
		sha = ref.ID
	} else {
		var ref struct {
			SHA string `json:"sha"`
		}
		err := s.githubGET(ctx, "/repos/"+c.Repository+"/commits/"+url.PathEscape(c.Branch), &ref, c.ConnectionID)
		if err != nil {
			return "", err
		}
		sha = ref.SHA
	}
	if !commitPattern.MatchString(sha) {
		return "", fmt.Errorf("Provider returned an invalid commit")
	}
	return sha, nil
}
func (s *Server) buildDetectionFiles(ctx context.Context, c buildConfig, commit string) (map[string]bool, error) {
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	var err error
	if c.Provider == "gitlab" {
		dir := c.ContextPath
		if dir == "." {
			dir = ""
		}
		err = s.gitlabGET(ctx, "/projects/"+url.PathEscape(c.Repository)+"/repository/tree?per_page=100&path="+url.QueryEscape(dir)+"&ref="+url.QueryEscape(commit), &entries, c.ConnectionID)
	} else {
		dir := c.ContextPath
		if dir == "." {
			dir = ""
		}
		err = s.githubGET(ctx, "/repos/"+c.Repository+"/contents/"+url.PathEscape(dir)+"?ref="+url.QueryEscape(commit), &entries, c.ConnectionID)
	}
	if err != nil {
		return nil, err
	}
	if len(entries) >= 100 {
		return nil, fmt.Errorf("Build directory inventory exceeds 99 entries; choose a smaller build context or configure the build manually")
	}
	files := map[string]bool{}
	for _, item := range entries {
		if item.Type == "file" || item.Type == "blob" {
			files[item.Name] = true
		}
	}
	return files, nil
}
func (s *Server) buildDetectionFile(ctx context.Context, c buildConfig, commit, filePath string) ([]byte, error) {
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	var err error
	if c.Provider == "gitlab" {
		err = s.gitlabGET(ctx, "/projects/"+url.PathEscape(c.Repository)+"/repository/files/"+url.PathEscape(filePath)+"?ref="+url.QueryEscape(commit), &file, c.ConnectionID)
	} else {
		err = s.githubGET(ctx, "/repos/"+c.Repository+"/contents/"+url.PathEscape(filePath)+"?ref="+url.QueryEscape(commit), &file, c.ConnectionID)
	}
	if err != nil {
		return nil, err
	}
	if (c.Provider == "github" && file.Type != "file") || file.Encoding != "base64" || file.Size < 0 || file.Size > 256<<10 {
		return nil, fmt.Errorf("Framework metadata must be a regular file under 256 KiB")
	}
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || len(body) > 256<<10 {
		return nil, fmt.Errorf("Invalid or oversized framework metadata")
	}
	return body, nil
}
