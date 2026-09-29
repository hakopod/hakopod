package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

// ManagedActionsInstallation is immutable startup configuration. It is owned
// by the installation administrator and never populated from application TOML.
// Native execution requires matching real-job qualification in each image report.
type ManagedActionsInstallation struct {
	PodCIDRs     []string
	ServiceCIDRs []string
	GitLab       []ManagedGitLabBinding
}

type ManagedGitLabBinding struct {
	Project, Environment, Application, Service string
	Target                                     actions.ProviderTarget
	Runtime                                    GitLabActionsRuntime
}

// ManagedActionsDestination grants one HTTPS origin access to exact private
// IPv4 hosts. Public internet access remains subject to the sandbox policy.
type ManagedActionsDestination struct {
	Origin    string   `toml:"origin"`
	Addresses []string `toml:"addresses"`
}

type ManagedActionsRegistryCA struct {
	Registry string
	CAPEM    []byte
}

type managedActionsFile struct {
	SchemaVersion int      `toml:"schema_version"`
	PodCIDRs      []string `toml:"pod_cidrs"`
	ServiceCIDRs  []string `toml:"service_cidrs"`
	Images        []struct {
		Name            string `toml:"name"`
		ReportFile      string `toml:"report_file"`
		ReportSHA256    string `toml:"report_sha256"`
		DefaultJobImage string `toml:"default_job_image"`
	} `toml:"images"`
	GitLab []struct {
		Project             string                      `toml:"project"`
		Environment         string                      `toml:"environment"`
		Application         string                      `toml:"application"`
		Service             string                      `toml:"service"`
		URL                 string                      `toml:"url"`
		ProjectID           int64                       `toml:"project_id"`
		GroupID             int64                       `toml:"group_id"`
		TrustPolicy         string                      `toml:"trust_policy"`
		ImagePair           string                      `toml:"image_pair"`
		CAFile              string                      `toml:"ca_file"`
		ArtifactOrigins     []string                    `toml:"artifact_origins"`
		PrivateDestinations []ManagedActionsDestination `toml:"private_destinations"`
		RegistryCAs         []struct {
			Registry string `toml:"registry"`
			CAFile   string `toml:"ca_file"`
		} `toml:"registry_cas"`
		Cache *struct {
			ServerAddress   string `toml:"server_address"`
			Bucket          string `toml:"bucket"`
			Region          string `toml:"region"`
			MaxArchiveBytes int64  `toml:"max_archive_bytes"`
			CAFile          string `toml:"ca_file"`
		} `toml:"cache"`
	} `toml:"gitlab"`
}

type nativeImageQualification struct {
	SchemaVersion                int    `json:"schema_version"`
	Status                       string `json:"status"`
	SentinelOnly                 bool   `json:"sentinel_only"`
	Architecture                 string `json:"architecture"`
	CredentialForwardingDetected bool   `json:"credential_forwarding_detected"`
	PublishedImagesVerified      bool   `json:"published_images_verified"`
	CacheProtocolVersion         int    `json:"cache_protocol_version,omitempty"`
	Source                       struct {
		Commit, Version       string
		TransportSourceSHA256 string `json:"transport_source_sha256"`
	} `json:"source"`
	Manager nativeQualifiedImage `json:"manager"`
	Helper  nativeQualifiedImage `json:"helper"`
	Policy  struct {
		Path          string `json:"path"`
		SchemaVersion int    `json:"schema_version"`
		ReadOnly      bool   `json:"read_only"`
	} `json:"policy"`
	Coverage struct {
		Verify struct {
			Passed           bool `json:"passed"`
			Cases            int  `json:"cases"`
			PositiveControls int  `json:"positive_controls"`
		} `json:"verify"`
		Helper struct {
			Passed                 bool `json:"passed"`
			Cases                  int  `json:"cases"`
			DirectControls         int  `json:"direct_controls"`
			UploadRedirects        int  `json:"upload_redirects"`
			MissingPolicyDownloads int  `json:"missing_policy_downloads"`
			AllowlistedDownloads   int  `json:"allowlisted_downloads"`
			DeniedDowngrades       int  `json:"denied_downgrades"`
		} `json:"helper"`
		Execution *GitLabActionsExecutionQualification `json:"execution,omitempty"`
		Cache     struct {
			Passed bool `json:"passed"`
			Cases  int  `json:"cases"`
		} `json:"cache,omitempty"`
	} `json:"coverage"`
	ReportHashes struct {
		Verify    string `json:"verify"`
		Helper    string `json:"helper"`
		Cache     string `json:"cache,omitempty"`
		Execution string `json:"execution,omitempty"`
	} `json:"report_hashes"`
	ImageBinaryVerification struct {
		Passed                bool   `json:"passed"`
		TransportSourceSHA256 string `json:"transport_source_sha256"`
		ManagerSHA256         string `json:"manager_sha256"`
		HelperSHA256          string `json:"helper_sha256"`
	} `json:"image_binary_verification"`
	Unverified         []string `json:"unverified"`
	QualificationScope string   `json:"qualification_scope"`
}

type nativeQualifiedImage struct {
	ImageRef               string `json:"image_ref"`
	PlatformManifestDigest string `json:"platform_manifest_digest"`
	BinaryPath             string `json:"binary_path"`
	BinarySHA256           string `json:"binary_sha256"`
}

func qualifiedGitLabImages(data []byte, expectedHash, defaultImage string) (GitLabActionsImages, error) {
	invalid := errors.New("managed actions image report does not verify the exact qualified manager and helper pair")
	sum := sha256.Sum256(data)
	if len(data) > 64<<10 || !gitlabActionsSHA256.MatchString(expectedHash) || hex.EncodeToString(sum[:]) != expectedHash {
		return GitLabActionsImages{}, invalid
	}
	var report nativeImageQualification
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF || report.SchemaVersion != 1 || report.Status != "passed" || !report.SentinelOnly || report.CredentialForwardingDetected || !report.PublishedImagesVerified || !report.ImageBinaryVerification.Passed {
		return GitLabActionsImages{}, invalid
	}
	if report.Policy.SchemaVersion != 1 || report.Policy.Path != gitlabActionsPolicyDirectory+"/transport-policy.json" || !report.Policy.ReadOnly || !report.Coverage.Verify.Passed || report.Coverage.Verify.Cases < 25 || report.Coverage.Verify.PositiveControls < 10 || !report.Coverage.Helper.Passed || report.Coverage.Helper.Cases < 102 || report.Coverage.Helper.DirectControls < 2 || report.Coverage.Helper.UploadRedirects < 25 || report.Coverage.Helper.MissingPolicyDownloads < 25 || report.Coverage.Helper.AllowlistedDownloads < 40 || report.Coverage.Helper.DeniedDowngrades < 10 || len(report.Unverified) > 32 || len(report.QualificationScope) > 1024 {
		return GitLabActionsImages{}, invalid
	}
	for _, hash := range []string{report.ReportHashes.Verify, report.ReportHashes.Helper} {
		if !gitlabActionsSHA256.MatchString(hash) || hash == strings.Repeat("0", 64) {
			return GitLabActionsImages{}, invalid
		}
	}
	for i, image := range []nativeQualifiedImage{report.Manager, report.Helper} {
		ref, err := parseReference(image.ImageRef)
		binary := "/usr/bin/gitlab-runner"
		if i == 1 {
			binary += "-helper"
		}
		if err != nil || ref.reference != image.PlatformManifestDigest || image.BinaryPath != binary {
			return GitLabActionsImages{}, invalid
		}
	}
	if report.Source.TransportSourceSHA256 != report.ImageBinaryVerification.TransportSourceSHA256 || report.Manager.BinarySHA256 != report.ImageBinaryVerification.ManagerSHA256 || report.Helper.BinarySHA256 != report.ImageBinaryVerification.HelperSHA256 {
		return GitLabActionsImages{}, invalid
	}
	images := GitLabActionsImages{Manager: report.Manager.ImageRef, Helper: report.Helper.ImageRef, DefaultJobImage: defaultImage, Architecture: report.Architecture, Status: report.Status, RunnerVersion: report.Source.Version, SourceCommit: report.Source.Commit, TransportSourceSHA256: report.Source.TransportSourceSHA256, ManagerBinarySHA256: report.Manager.BinarySHA256, HelperBinarySHA256: report.Helper.BinarySHA256, VerificationReportSHA256: expectedHash}
	if report.CacheProtocolVersion != 0 {
		if report.CacheProtocolVersion != 1 || !report.Coverage.Cache.Passed || report.Coverage.Cache.Cases < 16 || !gitlabActionsSHA256.MatchString(report.ReportHashes.Cache) || report.ReportHashes.Cache == strings.Repeat("0", 64) {
			return GitLabActionsImages{}, invalid
		}
		images.CacheProtocolVersion = report.CacheProtocolVersion
	}
	if report.Coverage.Execution != nil || report.ReportHashes.Execution != "" {
		images.Execution, images.ExecutionReportSHA256 = report.Coverage.Execution, report.ReportHashes.Execution
		if validateGitLabExecutionReport(images) != nil {
			return GitLabActionsImages{}, invalid
		}
	}
	if err := ValidateGitLabActionsRuntime(GitLabActionsRuntime{Images: images, TransportPolicy: GitLabActionsTransportPolicy{SchemaVersion: 1, CoordinatorURL: "https://gitlab.com", ArtifactOrigins: []string{}}}); err != nil {
		return GitLabActionsImages{}, invalid
	}
	return images, nil
}

func ReadManagedActionsFile(path string) (*ManagedActionsInstallation, error) {
	if path == "" {
		return nil, nil
	}
	data, err := readBoundedFile(path, 64<<10)
	if err != nil {
		return nil, errors.New("cannot read bounded installation-owned managed actions configuration")
	}
	var file managedActionsFile
	if toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&file) != nil || file.SchemaVersion != 1 || len(file.Images) > 8 || len(file.GitLab) > 64 {
		return nil, errors.New("managed actions require strict schema_version = 1 TOML with at most eight image pairs and 64 exact bindings")
	}
	read := func(name string) ([]byte, error) {
		if name == "" || len(name) > 1024 {
			return nil, errors.New("managed actions trust file reference is invalid")
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(filepath.Dir(path), name)
		}
		return readBoundedFile(name, 64<<10)
	}
	images := map[string]GitLabActionsImages{}
	for _, pair := range file.Images {
		if !awsBindingName.MatchString(pair.Name) || images[pair.Name].Manager != "" {
			return nil, errors.New("managed actions image pair names must be distinct")
		}
		report, e := read(pair.ReportFile)
		if e != nil {
			return nil, e
		}
		images[pair.Name], e = qualifiedGitLabImages(report, pair.ReportSHA256, pair.DefaultJobImage)
		if e != nil {
			return nil, e
		}
	}
	installation := &ManagedActionsInstallation{PodCIDRs: file.PodCIDRs, ServiceCIDRs: file.ServiceCIDRs}
	for _, binding := range file.GitLab {
		target := actions.ProviderTarget{Provider: actions.ProviderGitLab, GitLab: &actions.GitLabTarget{URL: binding.URL, ProjectID: binding.ProjectID, GroupID: binding.GroupID, TrustPolicy: binding.TrustPolicy}}
		canonical, e := target.Canonical()
		if e != nil || canonical.GitLab.URL != binding.URL {
			return nil, errors.New("managed actions require an exact canonical GitLab scope")
		}
		runtime := GitLabActionsRuntime{Images: images[binding.ImagePair], TransportPolicy: GitLabActionsTransportPolicy{SchemaVersion: 1, CoordinatorURL: binding.URL, ArtifactOrigins: binding.ArtifactOrigins}, PrivateDestinations: binding.PrivateDestinations}
		runtime.ClusterPodCIDRs, runtime.ClusterServiceCIDRs = slices.Clone(file.PodCIDRs), slices.Clone(file.ServiceCIDRs)
		if runtime.TransportPolicy.ArtifactOrigins == nil {
			runtime.TransportPolicy.ArtifactOrigins = []string{}
		}
		if binding.CAFile != "" {
			runtime.CAPEM, e = read(binding.CAFile)
			if e != nil {
				return nil, e
			}
		}
		runtime.ControlPlaneTrust = &actions.GitLabTrustPolicy{Name: binding.TrustPolicy, BaseURL: binding.URL, CAPEM: slices.Clone(runtime.CAPEM)}
		for _, destination := range binding.PrivateDestinations {
			coordinator, _ := url.Parse(binding.URL)
			if destination.Origin == coordinator.Scheme+"://"+coordinator.Host {
				runtime.ControlPlaneTrust.AllowedPrivateCIDRs = slices.Clone(destination.Addresses)
			}
		}
		for _, ca := range binding.RegistryCAs {
			pem, e := read(ca.CAFile)
			if e != nil {
				return nil, e
			}
			runtime.RegistryCAs = append(runtime.RegistryCAs, ManagedActionsRegistryCA{Registry: ca.Registry, CAPEM: pem})
		}
		if binding.Cache != nil {
			cache := binding.Cache
			runtime.Cache = &GitLabActionsCache{ServerAddress: cache.ServerAddress, Bucket: cache.Bucket, Region: cache.Region, MaxArchiveBytes: cache.MaxArchiveBytes}
			if cache.CAFile != "" {
				runtime.Cache.CAPEM, e = read(cache.CAFile)
				if e != nil {
					return nil, e
				}
			}
		}
		installation.GitLab = append(installation.GitLab, ManagedGitLabBinding{Project: binding.Project, Environment: binding.Environment, Application: binding.Application, Service: binding.Service, Target: canonical, Runtime: runtime})
	}
	if err := ValidateManagedActionsInstallation(installation); err != nil {
		return nil, err
	}
	return cloneManagedActionsInstallation(installation), nil
}

func ValidateManagedActionsInstallation(installation *ManagedActionsInstallation) error {
	if installation == nil {
		return nil
	}
	if len(installation.GitLab) > 64 {
		return errors.New("managed actions installation bindings exceed their bound")
	}
	var networks []netip.Prefix
	for _, cidrs := range [][]string{installation.PodCIDRs, installation.ServiceCIDRs} {
		if len(cidrs) < 1 || len(cidrs) > 16 {
			return errors.New("managed actions require the installation's actual Pod and Service CIDRs")
		}
		for _, raw := range cidrs {
			prefix, e := netip.ParsePrefix(raw)
			if e != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() || prefix.Bits() == 0 {
				return errors.New("managed actions cluster CIDRs are invalid")
			}
			for _, existing := range networks {
				if prefix.Overlaps(existing) {
					return errors.New("managed actions cluster CIDRs overlap")
				}
			}
			networks = append(networks, prefix)
		}
	}
	seen := map[string]bool{}
	for _, binding := range installation.GitLab {
		for _, scope := range []string{binding.Project, binding.Environment, binding.Application, binding.Service} {
			if !awsScopeName.MatchString(scope) {
				return errors.New("managed actions bindings require exact project, environment, application and service names")
			}
		}
		key := strings.Join([]string{binding.Project, binding.Environment, binding.Application, binding.Service}, "\x00")
		if seen[key] {
			return errors.New("managed actions service bindings must be unique")
		}
		seen[key] = true
		target, err := binding.Target.Canonical()
		if err != nil || target.Provider != actions.ProviderGitLab || binding.Runtime.TransportPolicy.CoordinatorURL != target.GitLab.URL || binding.Runtime.ControlPlaneTrust == nil || binding.Runtime.ControlPlaneTrust.Name != target.GitLab.TrustPolicy {
			return errors.New("managed actions binding and original transport trust differ")
		}
		if err := ValidateGitLabActionsRuntime(binding.Runtime); err != nil {
			return err
		}
		if !slices.Equal(binding.Runtime.ClusterPodCIDRs, installation.PodCIDRs) || !slices.Equal(binding.Runtime.ClusterServiceCIDRs, installation.ServiceCIDRs) {
			return errors.New("managed actions runtime must retain its original cluster networks")
		}
		for _, grant := range binding.Runtime.PrivateDestinations {
			for _, raw := range grant.Addresses {
				prefix, _ := netip.ParsePrefix(raw)
				for _, network := range networks {
					if prefix.Overlaps(network) {
						return errors.New("managed actions destination is inside a cluster network")
					}
				}
			}
		}
	}
	return nil
}

func cloneManagedActionsInstallation(in *ManagedActionsInstallation) *ManagedActionsInstallation {
	if in == nil {
		return nil
	}
	out := *in
	out.PodCIDRs, out.ServiceCIDRs, out.GitLab = slices.Clone(in.PodCIDRs), slices.Clone(in.ServiceCIDRs), slices.Clone(in.GitLab)
	for i := range out.GitLab {
		b := &out.GitLab[i]
		if b.Target.GitLab != nil {
			target := *b.Target.GitLab
			b.Target.GitLab = &target
		}
		b.Runtime = cloneGitLabRuntime(b.Runtime)
	}
	return &out
}

func cloneGitLabRuntime(in GitLabActionsRuntime) GitLabActionsRuntime {
	out := in
	out.Cache = cloneGitLabCache(in.Cache)
	if in.Images.Execution != nil {
		proof := *in.Images.Execution
		proof.Coordinators = slices.Clone(proof.Coordinators)
		proof.RunnerScopes = slices.Clone(proof.RunnerScopes)
		out.Images.Execution = &proof
	}
	out.CAPEM, out.TransportPolicy.ArtifactOrigins = slices.Clone(in.CAPEM), slices.Clone(in.TransportPolicy.ArtifactOrigins)
	out.ClusterPodCIDRs, out.ClusterServiceCIDRs = slices.Clone(in.ClusterPodCIDRs), slices.Clone(in.ClusterServiceCIDRs)
	if in.ControlPlaneTrust != nil {
		policy := *in.ControlPlaneTrust
		policy.CAPEM = slices.Clone(policy.CAPEM)
		policy.AllowedPrivateCIDRs = slices.Clone(policy.AllowedPrivateCIDRs)
		policy.DeniedCIDRs = slices.Clone(policy.DeniedCIDRs)
		out.ControlPlaneTrust = &policy
	}
	out.PrivateDestinations = slices.Clone(in.PrivateDestinations)
	for i := range out.PrivateDestinations {
		out.PrivateDestinations[i].Addresses = slices.Clone(in.PrivateDestinations[i].Addresses)
	}
	out.RegistryCAs = slices.Clone(in.RegistryCAs)
	for i := range out.RegistryCAs {
		out.RegistryCAs[i].CAPEM = slices.Clone(in.RegistryCAs[i].CAPEM)
	}
	return out
}

func (c *Client) ManagedActionsConfigured() bool { return c.options.ManagedActions != nil }

func (c *Client) ResolveGitLabActions(ctx context.Context, t Target, service string, config spec.Service) (GitLabActionsRuntime, error) {
	if c.options.ManagedActions == nil || config.Actions == nil {
		return GitLabActionsRuntime{}, errors.New("managed actions native installation binding is unavailable")
	}
	target, err := config.Actions.ProviderTarget().Canonical()
	if err != nil {
		return GitLabActionsRuntime{}, err
	}
	for _, binding := range c.options.ManagedActions.GitLab {
		if binding.Project != t.Project || binding.Environment != t.Environment || binding.Application != t.Spec.Name || binding.Service != service {
			continue
		}
		if target != binding.Target { // ProviderTarget contains a pointer; compare its canonical identity below.
			left, _ := json.Marshal(target)
			right, _ := json.Marshal(binding.Target)
			if !bytes.Equal(left, right) {
				return GitLabActionsRuntime{}, errors.New("managed actions provider scope does not match its installation binding")
			}
		}
		if binding.Runtime.Images.Manager != config.Image || binding.Runtime.Images.Architecture != config.Architecture {
			return GitLabActionsRuntime{}, errors.New("managed actions image pair does not match its installation binding")
		}
		if err := ValidateGitLabActionsExecution(binding.Runtime, config.Actions.Cache != nil); err != nil {
			return GitLabActionsRuntime{}, err
		}
		if err := validateGitLabExecutionScope(binding.Runtime, target.GitLab); err != nil {
			return GitLabActionsRuntime{}, err
		}
		if _, err := c.validateGitLabPrivateNetworks(ctx, binding.Runtime); err != nil {
			return GitLabActionsRuntime{}, err
		}
		runtime := cloneGitLabRuntime(binding.Runtime)
		if config.Actions.Cache != nil && runtime.Cache == nil {
			return GitLabActionsRuntime{}, errors.New("GitLab cache storage is not configured for this pool")
		}
		return runtime, nil
	}
	return GitLabActionsRuntime{}, errors.New("managed actions native installation binding is unavailable")
}

func managedActionsOrigin(value string) (string, error) {
	canonical, err := actions.CanonicalGitLabURL(value)
	u, parseErr := url.Parse(value)
	if err != nil || parseErr != nil || canonical != value || u.Path != "" {
		return "", errors.New("managed actions destinations require exact canonical HTTPS origins")
	}
	return u.Host, nil
}
