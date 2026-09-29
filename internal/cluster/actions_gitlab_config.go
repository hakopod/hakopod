package cluster

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const gitlabActionsSourceCommit = "3c39fcebf73d01d464db3dee8a5267155273a6c5"
const gitlabActionsRunnerVersion = "19.4.1"
const gitlabActionsProviderLabel = "hakopod.io/actions-provider"
const gitlabActionsSlotLabel = "hakopod.io/actions-slot"
const gitlabActionsBindingAnnotation = "hakopod.io/actions-registration"
const gitlabActionsManagerDirectory = "/run/hakopod-manager"
const gitlabActionsPolicyDirectory = "/run/hakopod-provider"
const gitlabActionsDaemonPolicyDirectory = "/run/hakopod-provider-public"

var gitlabActionsSlotID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var gitlabActionsServiceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var gitlabActionsSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

// GitLabActionsImages is resolved from an installation's trusted paired-image
// qualification report, never from service TOML. A syntactically valid digest is
// not image qualification: Status must be passed after image/binary verification.
type GitLabActionsImages struct {
	Manager                  string
	Helper                   string
	DefaultJobImage          string
	Architecture             string
	Status                   string
	RunnerVersion            string
	SourceCommit             string
	TransportSourceSHA256    string
	ManagerBinarySHA256      string
	HelperBinarySHA256       string
	VerificationReportSHA256 string
	CacheProtocolVersion     int
	ExecutionReportSHA256    string
	Execution                *GitLabActionsExecutionQualification
}

// GitLabActionsTransportPolicy contains only public network policy. Its file
// schema is shared with the patched native manager and helper binaries.
type GitLabActionsTransportPolicy struct {
	SchemaVersion   int      `json:"schema_version"`
	CoordinatorURL  string   `json:"coordinator_url"`
	ArtifactOrigins []string `json:"artifact_origins"`
}

type GitLabActionsRuntime struct {
	Images              GitLabActionsImages
	TransportPolicy     GitLabActionsTransportPolicy
	CAPEM               []byte
	ControlPlaneTrust   *actions.GitLabTrustPolicy
	PrivateDestinations []ManagedActionsDestination
	RegistryCAs         []ManagedActionsRegistryCA
	ClusterPodCIDRs     []string
	ClusterServiceCIDRs []string
	Cache               *GitLabActionsCache
}

func ValidateGitLabActionsRuntime(runtime GitLabActionsRuntime) error {
	images := runtime.Images
	if (images.Architecture != "amd64" && images.Architecture != "arm64") || images.Status != "passed" || images.RunnerVersion != gitlabActionsRunnerVersion || images.SourceCommit != gitlabActionsSourceCommit {
		return errors.New("GitLab requires a qualified manager and helper image pair for the selected architecture")
	}
	for _, digest := range []string{images.TransportSourceSHA256, images.ManagerBinarySHA256, images.HelperBinarySHA256, images.VerificationReportSHA256} {
		if !gitlabActionsSHA256.MatchString(digest) || digest == strings.Repeat("0", 64) {
			return errors.New("GitLab image qualification metadata is missing or invalid")
		}
	}
	for index, image := range []string{images.Manager, images.Helper, images.DefaultJobImage} {
		ref, err := parseReference(image)
		if image == "" || ValidateReadinessProbeImage(image) != nil || err != nil || !strings.HasPrefix(ref.reference, "sha256:") {
			return errors.New("GitLab runtime images must be explicit trusted digest-pinned references")
		}
		if index < 2 && (ref.canonical == "docker.io/gitlab/gitlab-runner" || strings.HasPrefix(ref.canonical, "registry.gitlab.com/gitlab-org/gitlab-runner/")) {
			return errors.New("GitLab requires the qualified patched manager and helper images")
		}
	}
	if images.Manager == images.Helper || images.ManagerBinarySHA256 == images.HelperBinarySHA256 {
		return errors.New("GitLab manager and helper qualification must identify both distinct binaries")
	}
	policy := runtime.TransportPolicy
	canonical, err := actions.CanonicalGitLabURL(policy.CoordinatorURL)
	if err != nil || canonical != policy.CoordinatorURL || policy.SchemaVersion != 1 || policy.ArtifactOrigins == nil || len(policy.ArtifactOrigins) > 16 {
		return errors.New("GitLab requires a canonical versioned native transport policy")
	}
	seen := map[string]bool{}
	for _, origin := range policy.ArtifactOrigins {
		canonical, err := actions.CanonicalGitLabURL(origin)
		u, parseErr := url.Parse(origin)
		if err != nil || parseErr != nil || canonical != origin || u.Path != "" || seen[origin] {
			return errors.New("GitLab artifact destinations must be distinct approved HTTPS origins")
		}
		seen[origin] = true
	}
	encoded, err := json.Marshal(policy)
	if err != nil || len(encoded) > 32<<10 || len(runtime.CAPEM) > 64<<10 {
		return errors.New("GitLab native transport policy exceeds its size bound")
	}
	if err := validateGitLabCA(runtime.CAPEM); err != nil {
		return err
	}
	if err := validateGitLabRuntimeDestinations(runtime); err != nil {
		return err
	}
	if err := validateGitLabCache(runtime); err != nil {
		return err
	}
	encodedRuntime, err := json.Marshal(runtime)
	if err != nil || len(encodedRuntime) > 96<<10 {
		return errors.New("GitLab resolved runtime exceeds its storage bound")
	}
	return nil
}

func validateGitLabCA(data []byte) error {
	if len(data) > 64<<10 {
		return errors.New("GitLab native trust exceeds its size bound")
	}
	remaining := bytes.TrimSpace(data)
	for count := 0; len(remaining) > 0; count++ {
		block, rest := pem.Decode(remaining)
		if count >= 32 || block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("GitLab native trust requires a bounded CA certificate bundle")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return errors.New("GitLab native trust requires CA certificates")
		}
		remaining = bytes.TrimSpace(rest)
	}
	return nil
}

func validateGitLabActions(t Target, service, id string, s spec.Service, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) error {
	if !gitlabActionsSlotID.MatchString(t.ApplicationID) || !gitlabActionsSlotID.MatchString(id) || !gitlabActionsServiceName.MatchString(service) || t.Revision <= 0 || s.Actions == nil {
		return errors.New("GitLab runner slot identity is invalid")
	}
	if current, ok := t.Spec.Services[service]; !ok || current.Actions == nil || !reflect.DeepEqual(current, s) {
		return errors.New("GitLab runner service does not match its original application revision")
	}
	target, err := s.Actions.ProviderTarget().Canonical()
	if err != nil || target.Provider != actions.ProviderGitLab || registration.SchemaVersion != 1 || registration.Name != "hakopod-"+id || registration.URL != target.GitLab.URL || runtime.TransportPolicy.CoordinatorURL != registration.URL {
		return errors.New("GitLab runner registration does not match its original slot and provider target")
	}
	runnerID, err := strconv.ParseInt(registration.RunnerID, 10, 64)
	if err != nil || runnerID <= 0 || strconv.FormatInt(runnerID, 10) != registration.RunnerID || !strings.HasPrefix(registration.Token, "glrt-") || len(registration.Token) < 16 || len(registration.Token) > 4096 || strings.IndexFunc(registration.Token, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
		return errors.New("GitLab native registration material is invalid")
	}
	if s.Actions.TimeoutMinutes < 5 || s.Actions.TimeoutMinutes > 360 || registration.TimeoutSeconds != s.Actions.TimeoutMinutes*60 || (registration.ExpiresAt != nil && !registration.ExpiresAt.After(time.Now())) {
		return errors.New("GitLab registration is expired or does not match its bounded job timeout")
	}
	if s.Image != runtime.Images.Manager || s.Architecture != runtime.Images.Architecture || spec.ActionsWorkspaceGiB(s.Actions) < 2 || spec.ActionsWorkspaceGiB(s.Actions) > 16 {
		return errors.New("GitLab requires explicit matching architecture and bounded temporary storage")
	}
	if (s.Actions.Cache == nil) != (registration.CacheCredentials == nil) || (s.Actions.Cache != nil && runtime.Cache == nil) {
		return errors.New("GitLab cache must match its installation binding and private credentials")
	}
	if s.Actions.Cache != nil {
		if s.Actions.Cache.Credential == "" || !(spec.SecretRef{Ref: s.Actions.Cache.Credential}).Valid() {
			return errors.New("GitLab cache requires an application secret reference")
		}
		if err := registration.CacheCredentials.Validate(); err != nil {
			return err
		}
	}
	profile := spec.EffectiveResources(s)
	for _, bounds := range []struct{ value, minimum, maximum string }{
		{profile.CPURequest, "200m", "64"}, {profile.CPULimit, "1", "64"},
		{profile.MemoryRequest, "2Gi", "256Gi"}, {profile.MemoryLimit, "4Gi", "256Gi"},
	} {
		value, err := resource.ParseQuantity(bounds.value)
		if err != nil || value.Cmp(resource.MustParse(bounds.minimum)) < 0 || value.Cmp(resource.MustParse(bounds.maximum)) > 0 {
			return errors.New("GitLab slot resources are outside the supported bounded envelope")
		}
	}
	cpuRequest, memoryRequest := resource.MustParse(profile.CPURequest), resource.MustParse(profile.MemoryRequest)
	if cpuRequest.Cmp(resource.MustParse(profile.CPULimit)) > 0 || memoryRequest.Cmp(resource.MustParse(profile.MemoryLimit)) > 0 {
		return errors.New("GitLab resource requests must fit within their limits")
	}
	return ValidateGitLabActionsRuntime(runtime)
}

type gitlabNativeDockerConfig struct {
	Host                string   `toml:"host"`
	Image               string   `toml:"image"`
	HelperImage         string   `toml:"helper_image"`
	Privileged          bool     `toml:"privileged"`
	ServicesPrivileged  bool     `toml:"services_privileged"`
	DisableCache        bool     `toml:"disable_cache"`
	PullPolicy          []string `toml:"pull_policy"`
	AllowedPullPolicies []string `toml:"allowed_pull_policies"`
	Volumes             []string `toml:"volumes"`
	Memory              string   `toml:"memory"`
	MemorySwap          string   `toml:"memory_swap"`
	ServiceMemory       string   `toml:"service_memory"`
	ServiceMemorySwap   string   `toml:"service_memory_swap"`
	CPUs                string   `toml:"cpus"`
	ServiceCPUs         string   `toml:"service_cpus"`
	ServicesLimit       int      `toml:"services_limit"`
	PidsLimit           int64    `toml:"pids_limit"`
}

type gitlabNativeRunnerConfig struct {
	Name               string                   `toml:"name"`
	URL                string                   `toml:"url"`
	Token              string                   `toml:"token"`
	Executor           string                   `toml:"executor"`
	TLSCAFile          string                   `toml:"tls-ca-file,omitempty"`
	Limit              int                      `toml:"limit"`
	RequestConcurrency int                      `toml:"request_concurrency"`
	OutputLimit        int                      `toml:"output_limit"`
	Environment        []string                 `toml:"environment"`
	Docker             gitlabNativeDockerConfig `toml:"docker"`
	Cache              *gitlabNativeCacheConfig `toml:"cache,omitempty"`
}

type gitlabNativeConfig struct {
	Concurrent      int                        `toml:"concurrent"`
	CheckInterval   int                        `toml:"check_interval"`
	ShutdownTimeout int                        `toml:"shutdown_timeout"`
	Runners         []gitlabNativeRunnerConfig `toml:"runners"`
}

func gitlabActionsConfig(t Target, service string, s spec.Service, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) ([]byte, error) {
	daemon := actionsResources(s, 3)
	memory, cpu := daemon.Limits[corev1.ResourceMemory], daemon.Limits[corev1.ResourceCPU]
	jobMemory, serviceMemory := memory.Value()/2, memory.Value()/8
	jobCPU, serviceCPU := max(int64(1), cpu.MilliValue()/2), max(int64(1), cpu.MilliValue()/8)
	volumes := []string{
		"/var/run/docker.sock:/var/run/docker.sock",
		gitlabActionsDaemonPolicyDirectory + "/transport-policy.json:" + gitlabActionsPolicyDirectory + "/transport-policy.json:ro",
	}
	caFile := ""
	if len(runtime.CAPEM) > 0 {
		caFile = gitlabActionsPolicyDirectory + "/ca.crt"
		volumes = append(volumes, gitlabActionsDaemonPolicyDirectory+"/ca.crt:"+caFile+":ro")
	}
	if s.Actions.Cache != nil {
		volumes = append(volumes, gitlabActionsDaemonPolicyDirectory+"/cache-policy.json:"+gitlabActionsPolicyDirectory+"/cache-policy.json:ro")
		if len(runtime.Cache.CAPEM) > 0 {
			volumes = append(volumes, gitlabActionsDaemonPolicyDirectory+"/cache-ca.crt:"+gitlabActionsPolicyDirectory+"/cache-ca.crt:ro")
		}
	}
	config := gitlabNativeConfig{Concurrent: 1, CheckInterval: 3, ShutdownTimeout: 45, Runners: []gitlabNativeRunnerConfig{{
		Name: registration.Name, URL: registration.URL, Token: registration.Token, Executor: "docker", TLSCAFile: caFile,
		Limit: 1, RequestConcurrency: 1, OutputLimit: 1024,
		Environment: []string{"FF_NETWORK_PER_BUILD=1", "DOCKER_HOST=unix:///var/run/docker.sock"},
		Docker: gitlabNativeDockerConfig{
			Host: "tcp://127.0.0.1:2375", Image: runtime.Images.DefaultJobImage, HelperImage: runtime.Images.Helper,
			DisableCache: true, PullPolicy: []string{"always"}, AllowedPullPolicies: []string{"always"}, Volumes: volumes,
			Memory: strconv.FormatInt(jobMemory, 10), MemorySwap: strconv.FormatInt(jobMemory, 10), ServiceMemory: strconv.FormatInt(serviceMemory, 10), ServiceMemorySwap: strconv.FormatInt(serviceMemory, 10),
			CPUs: fmt.Sprintf("%d.%03d", jobCPU/1000, jobCPU%1000), ServiceCPUs: fmt.Sprintf("%d.%03d", serviceCPU/1000, serviceCPU%1000), ServicesLimit: 2, PidsLimit: 512,
		},
	}}}
	if s.Actions.Cache != nil {
		config.Runners[0].Cache = gitlabCacheConfig(t, service, runtime.Cache, registration.CacheCredentials)
		config.Runners[0].Environment = append(config.Runners[0].Environment, "CACHE_COMPRESSION_FORMAT=tarzstd", "CACHE_COMPRESSION_LEVEL=fast", "FF_HASH_CACHE_KEYS=1")
	}
	encoded, err := toml.Marshal(config)
	if err != nil || len(encoded) > 128<<10 {
		return nil, errors.New("GitLab native configuration could not be encoded within its bound")
	}
	return encoded, nil
}

func gitlabActionsMetadata(t Target, service, id string, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) metav1.ObjectMeta {
	// Bind immutable artifacts to their original scope and qualified runtime.
	// The registration token is deliberately absent from public annotations.
	data, _ := json.Marshal(struct {
		ApplicationID, Service, SlotID, RunnerID string
		Target                                   actions.ProviderTarget
		TimeoutSeconds, WorkspaceGiB             int64
		Resources                                spec.Profile
		Runtime                                  GitLabActionsRuntime
	}{t.ApplicationID, service, id, registration.RunnerID, t.Spec.Services[service].Actions.ProviderTarget(), registration.TimeoutSeconds, spec.ActionsWorkspaceGiB(t.Spec.Services[service].Actions), spec.EffectiveResources(t.Spec.Services[service]), runtime})
	digest := sha256.Sum256(data)
	labels := labelsFor(t, service)
	labels[gitlabActionsProviderLabel], labels[gitlabActionsSlotLabel] = "gitlab", id
	return metav1.ObjectMeta{Name: "actions-" + id, Namespace: Namespace(t.ApplicationID), Labels: labels, Annotations: map[string]string{gitlabActionsBindingAnnotation: hex.EncodeToString(digest[:]), "hakopod.io/actions-runner-id": registration.RunnerID}}
}

func gitlabActionsArtifacts(t Target, service, id string, s spec.Service, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) (*corev1.Secret, *corev1.ConfigMap, error) {
	if err := validateGitLabActions(t, service, id, s, registration, runtime); err != nil {
		return nil, nil, err
	}
	config, err := gitlabActionsConfig(t, service, s, registration, runtime)
	if err != nil {
		return nil, nil, err
	}
	meta := gitlabActionsMetadata(t, service, id, registration, runtime)
	secret := &corev1.Secret{ObjectMeta: meta, Immutable: ptr(true), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"config.toml": config}}
	policy, _ := json.Marshal(runtime.TransportPolicy)
	public := &corev1.ConfigMap{ObjectMeta: *meta.DeepCopy(), Immutable: ptr(true), Data: map[string]string{"transport-policy.json": string(policy)}}
	if len(runtime.CAPEM) > 0 {
		public.Data["ca.crt"] = string(runtime.CAPEM)
	}
	if s.Actions.Cache != nil {
		data, err := json.Marshal(gitlabCachePolicyFor(t, service, runtime.Cache))
		if err != nil {
			return nil, nil, errors.New("GitLab cache policy could not be encoded")
		}
		public.Data["cache-policy.json"] = string(data)
		if len(runtime.Cache.CAPEM) > 0 {
			public.Data["cache-ca.crt"] = string(runtime.Cache.CAPEM)
		}
	}
	for i, ca := range runtime.RegistryCAs {
		public.Data[fmt.Sprintf("registry-ca-%d.crt", i)] = string(ca.CAPEM)
	}
	return secret, public, nil
}
