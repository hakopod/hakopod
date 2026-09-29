package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

type Provider string

const (
	ProviderGitHub    Provider = "github"
	ProviderGitLab    Provider = "gitlab"
	ProviderBitbucket Provider = "bitbucket"
)

// Effective preserves the provider used by revisions saved before this field existed.
func (p Provider) Effective() Provider {
	if p == "" {
		return ProviderGitHub
	}
	return p
}

func (p Provider) Valid() bool {
	switch p.Effective() {
	case ProviderGitHub, ProviderGitLab, ProviderBitbucket:
		return true
	default:
		return false
	}
}

type GitLabTarget struct {
	URL         string `json:"url" toml:"url"`
	ProjectID   int64  `json:"project_id,omitempty" toml:"project_id,omitempty"`
	GroupID     int64  `json:"group_id,omitempty" toml:"group_id,omitempty"`
	TrustPolicy string `json:"trust_policy,omitempty" toml:"trust_policy,omitempty"`
}

type BitbucketTarget struct {
	Workspace  string `json:"workspace" toml:"workspace"`
	Repository string `json:"repository,omitempty" toml:"repository,omitempty"`
}

// ProviderTarget is distinct from Target so GitHub's comparable budget and
// inventory keys retain their existing representation. It contains no credentials.
type ProviderTarget struct {
	Provider  Provider         `json:"provider"`
	GitHub    Target           `json:"github,omitempty"`
	GitLab    *GitLabTarget    `json:"gitlab,omitempty"`
	Bitbucket *BitbucketTarget `json:"bitbucket,omitempty"`
}

var providerPolicyName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var providerUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var providerDNSLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (t ProviderTarget) Validate() error {
	_, err := t.Canonical()
	return err
}

// Canonical validates configuration syntax, not network access. A transport must
// still resolve the selected trust policy and validate every destination it dials.
// It copies nested targets so validation cannot mutate a saved revision.
func (t ProviderTarget) Canonical() (ProviderTarget, error) {
	t.Provider = t.Provider.Effective()
	switch t.Provider {
	case ProviderGitHub:
		if t.GitLab != nil || t.Bitbucket != nil || !t.GitHub.Valid() {
			return ProviderTarget{}, errors.New("select either a GitHub.com organization or owner/repository; runner groups apply only to organizations")
		}
	case ProviderGitLab:
		if t.GitHub != (Target{}) || t.GitLab == nil || t.Bitbucket != nil {
			return ProviderTarget{}, errors.New("GitLab requires only a gitlab target")
		}
		gitlab := *t.GitLab
		if (gitlab.ProjectID > 0) == (gitlab.GroupID > 0) || gitlab.ProjectID < 0 || gitlab.GroupID < 0 || gitlab.ProjectID > 9007199254740991 || gitlab.GroupID > 9007199254740991 {
			return ProviderTarget{}, errors.New("GitLab requires exactly one positive project_id or group_id")
		}
		if gitlab.TrustPolicy != "" && !providerPolicyName.MatchString(gitlab.TrustPolicy) {
			return ProviderTarget{}, errors.New("GitLab trust_policy must name an installation-approved policy")
		}
		canonical, err := CanonicalGitLabURL(gitlab.URL)
		if err != nil {
			return ProviderTarget{}, err
		}
		gitlab.URL = canonical
		if canonical != "https://gitlab.com" && gitlab.TrustPolicy == "" {
			return ProviderTarget{}, errors.New("a custom GitLab URL requires an installation-approved trust_policy")
		}
		t.GitLab = &gitlab
	case ProviderBitbucket:
		if t.GitHub != (Target{}) || t.Bitbucket == nil || t.GitLab != nil {
			return ProviderTarget{}, errors.New("Bitbucket requires only a bitbucket target")
		}
		bitbucket := *t.Bitbucket
		workspace, ok := canonicalProviderUUID(bitbucket.Workspace)
		if !ok {
			return ProviderTarget{}, errors.New("Bitbucket workspace must be its UUID")
		}
		bitbucket.Workspace = workspace
		if bitbucket.Repository != "" {
			repository, ok := canonicalProviderUUID(bitbucket.Repository)
			if !ok {
				return ProviderTarget{}, errors.New("Bitbucket repository must be its UUID")
			}
			bitbucket.Repository = repository
		}
		t.Bitbucket = &bitbucket
	default:
		return ProviderTarget{}, errors.New("provider must be github, gitlab or bitbucket")
	}
	return t, nil
}

func canonicalProviderUUID(value string) (string, bool) {
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		value = value[1 : len(value)-1]
	}
	if !providerUUID.MatchString(value) || value == "00000000-0000-0000-0000-000000000000" {
		return "", false
	}
	return "{" + strings.ToLower(value) + "}", true
}

// CanonicalGitLabURL retains an installation's relative URL prefix. URL syntax
// never grants access to private networks, even when a trust policy is named.
func CanonicalGitLabURL(raw string) (string, error) {
	if raw == "" {
		return "https://gitlab.com", nil
	}
	invalid := errors.New("GitLab url must be an HTTPS instance base without credentials, query, fragment or encoded path segments")
	if len(raw) > 2048 || strings.ContainsAny(raw, "\\%?#") || strings.IndexFunc(raw, func(r rune) bool { return r <= ' ' || r == 127 }) >= 0 {
		return "", invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || u.EscapedPath() != u.Path {
		return "", invalid
	}
	host := strings.ToLower(u.Hostname())
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if address.IsLoopback() || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
			return "", errors.New("GitLab url cannot address loopback, metadata, link-local or multicast networks")
		}
		host = address.String()
	} else {
		host, err = idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
		if err != nil || len(host) > 253 || !strings.Contains(host, ".") {
			return "", invalid
		}
		for _, label := range strings.Split(host, ".") {
			if !providerDNSLabel.MatchString(label) {
				return "", invalid
			}
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", invalid
		}
		if n == 443 {
			port = ""
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	if strings.Contains(u.Path, "//") {
		return "", invalid
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", invalid
		}
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if strings.Contains(u.Path+"/", "/api/v4/") {
		return "", errors.New("GitLab url must name the instance base, not its API endpoint")
	}
	if strings.HasSuffix(u.Path, "/ci") {
		return "", errors.New("GitLab Runner does not preserve an instance base ending in /ci")
	}
	return u.String(), nil
}

type CancellationScope string

const (
	CancellationNone     CancellationScope = "none"
	CancellationJob      CancellationScope = "job"
	CancellationPipeline CancellationScope = "pipeline"
)

type ProviderMinimumResources struct {
	CPURequest    string `json:"cpu_request"`
	CPULimit      string `json:"cpu_limit"`
	MemoryRequest string `json:"memory_request"`
	MemoryLimit   string `json:"memory_limit"`
	WorkspaceGiB  int64  `json:"workspace_gib"`
}

type ProviderCacheCapabilities struct {
	Persistent bool   `json:"persistent"`
	Backend    string `json:"backend,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ProviderBuildCapabilities struct {
	NativeArchitectures []string `json:"native_architectures"`
	CrossArchitecture   bool     `json:"cross_architecture"`
	Reason              string   `json:"reason,omitempty"`
}

type ProviderIsolationCapabilities struct {
	SingleJob          bool   `json:"single_job"`
	ManagerCredentials bool   `json:"manager_credentials_isolated"`
	Reason             string `json:"reason,omitempty"`
}

// Capabilities report qualified implementation behavior. Available does not
// imply a live node, available provider quota, or successful deployment.
type ProviderCapabilities struct {
	Provider          Provider                      `json:"provider"`
	Available         bool                          `json:"available"`
	Reason            string                        `json:"reason,omitempty"`
	Image             string                        `json:"image,omitempty"`
	MinimumResources  ProviderMinimumResources      `json:"minimum_resources"`
	Cache             ProviderCacheCapabilities     `json:"cache"`
	Build             ProviderBuildCapabilities     `json:"build"`
	Isolation         ProviderIsolationCapabilities `json:"isolation"`
	CancellationScope CancellationScope             `json:"cancellation_scope"`
}

// ProviderRunner retains UUIDs and numeric provider identifiers without a
// lossy conversion. An identifier is meaningful only in its recorded target.
type ProviderRunner struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Busy       bool      `json:"busy"`
	ObservedAt time.Time `json:"-"`
}

type ProviderRegistration struct {
	Runner            ProviderRunner `json:"runner"`
	ManagerConfig     []byte         `json:"-"`
	CleanupCredential []byte         `json:"-"`
	ExpiresAt         *time.Time     `json:"expires_at,omitempty"`
}

// Validate bounds a registration before its private payload is persisted. The
// adapter still validates its provider's identifier and configuration format.
func (r ProviderRegistration) Validate(expectedName string) error {
	if !labelPattern.MatchString(expectedName) || r.Runner.Name != expectedName || !ValidProviderID(r.Runner.ID) || len(r.Runner.Status) > 64 || len(r.ManagerConfig) < 1 || len(r.ManagerConfig) > 128<<10 || len(r.CleanupCredential) > 8<<10 {
		return errors.New("provider registration is incomplete or exceeds its bounds; reconcile the recorded name")
	}
	return nil
}

func ValidProviderID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	return strings.IndexFunc(id, func(c rune) bool {
		return c <= ' ' || c >= 127 || strings.ContainsRune("/\\?#%", c)
	}) < 0
}

// FindOwned returns all exact owned-name matches within a bounded target scan.
// A successful empty result confirms absence; a failed or truncated scan does not.
type ProviderClient interface {
	Capabilities() ProviderCapabilities
	Register(context.Context, ProviderTarget, string, []string) (ProviderRegistration, error)
	Get(context.Context, ProviderTarget, string) (ProviderRunner, error)
	FindOwned(context.Context, ProviderTarget, string) ([]ProviderRunner, error)
	Delete(context.Context, ProviderTarget, string) error
}

type ProviderJobIdentity struct {
	RunnerID   string `json:"runner_id"`
	RunnerName string `json:"runner_name"`
	// Repository is owner/repo for GitHub, a project ID for GitLab, and a
	// repository UUID for Bitbucket. Adapters validate it within the target.
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	JobID      string `json:"job_id"`
	Attempt    int64  `json:"attempt,omitempty"`
}

type ProviderJob struct {
	Identity       ProviderJobIdentity `json:"identity"`
	Name           string              `json:"name"`
	Status         string              `json:"status"`
	Conclusion     string              `json:"conclusion"`
	StartedAt      *time.Time          `json:"started_at,omitempty"`
	CompletedAt    *time.Time          `json:"completed_at,omitempty"`
	Steps          []Step              `json:"steps"`
	StepsTruncated bool                `json:"steps_truncated"`
}

// Job identity supplied by a runner remains untrusted until AssignedJob verifies
// the target, runner and job with the provider. Logs must revalidate that binding.
type ProviderJobClient interface {
	AssignedJob(context.Context, ProviderTarget, ProviderJobIdentity) (*ProviderJob, error)
	JobLogs(context.Context, ProviderTarget, ProviderJobIdentity) ([]LogLine, bool, error)
}

// Historical reads require a durable previously verified assignment and
// confirmed provider cleanup. Callers must never derive that proof from input.
type ProviderHistoricalJobClient interface {
	HistoricalJob(context.Context, ProviderTarget, ProviderJobIdentity) (*ProviderJob, error)
	HistoricalJobLogs(context.Context, ProviderTarget, ProviderJobIdentity) ([]LogLine, bool, error)
}

type ProviderCancellationClient interface {
	Cancel(context.Context, ProviderTarget, ProviderJobIdentity) (CancellationScope, error)
}

// A successful drain must prevent another job from being acquired while letting
// the current job finish. Idle polling alone does not satisfy this contract.
type ProviderDrainClient interface {
	Drain(context.Context, ProviderTarget, string) error
}

type UnsupportedProviderError struct {
	Provider Provider
	Reason   string
}

func (e *UnsupportedProviderError) Error() string {
	return fmt.Sprintf("unsupported provider %q: %s", e.Provider, e.Reason)
}
