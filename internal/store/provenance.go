package store

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
)

// SourceBuild is a claim supplied by an authorized deployment client, not a
// verified provider attestation. It is bound to an exact service image digest.
type SourceBuild struct {
	Image      string `json:"image"`
	CommitSHA  string `json:"commit_sha"`
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	RunURL     string `json:"run_url,omitempty"`
}

var sourceCommit = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
var sourceDigest = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)
var sourceRepository = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)

func ValidateProvenance(next spec.Application, provenance map[string]SourceBuild) error {
	if len(provenance) > 20 {
		return fmt.Errorf("provenance supports at most 20 services")
	}
	for name, build := range provenance {
		svc, ok := next.Services[name]
		if !ok {
			return fmt.Errorf("provenance service %q is not in the deployment", name)
		}
		if !sourceDigest.MatchString(build.Image) || svc.Image != build.Image {
			return fmt.Errorf("provenance for %s requires the exact digest-pinned service image", name)
		}
		if !sourceCommit.MatchString(build.CommitSHA) {
			return fmt.Errorf("provenance for %s requires a full lowercase commit SHA", name)
		}
		if build.Provider != "github" && build.Provider != "gitlab" && build.Provider != "other" {
			return fmt.Errorf("provenance provider must be github, gitlab or other")
		}
		if len(build.Repository) > 256 || !sourceRepository.MatchString(build.Repository) {
			return fmt.Errorf("provenance repository must be an owner/repository path")
		}
		if len(build.Branch) > 256 || strings.ContainsAny(build.Branch, "\r\n\x00") {
			return fmt.Errorf("invalid provenance branch")
		}
		if build.RunURL != "" {
			u, err := url.Parse(build.RunURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(build.RunURL) > 2048 {
				return fmt.Errorf("provenance run_url must be an HTTPS URL without credentials, query or fragment")
			}
		}
	}
	return nil
}

func (s *Store) AcceptWithProvenance(ctx context.Context, p Principal, project, env string, next spec.Application, expected int64, idem string, provenance map[string]SourceBuild) (Deployment, error) {
	return s.acceptGuarded(ctx, p, project, env, next, expected, idem, nil, nil, nil, nil, provenance)
}
