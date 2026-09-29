package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"

	"github.com/hakopod/hakopod/internal/actions"
)

// GitLabActionsCache is an administrator-approved storage destination. It
// contains no credentials; the application opts in with a separate secret.
type GitLabActionsCache struct {
	ServerAddress   string
	Bucket          string
	Region          string
	MaxArchiveBytes int64
	CAPEM           []byte
}

var gitlabCacheBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var gitlabCacheRegion = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func validateGitLabCache(runtime GitLabActionsRuntime) error {
	c := runtime.Cache
	if c == nil {
		return nil
	}
	if runtime.Images.CacheProtocolVersion != 1 {
		return errors.New("GitLab cache requires the qualified bounded cache transport in both native images")
	}
	host, err := managedActionsOrigin("https://" + c.ServerAddress)
	if err != nil || host != c.ServerAddress || !gitlabCacheBucket.MatchString(c.Bucket) || !gitlabCacheRegion.MatchString(c.Region) || c.MaxArchiveBytes < 1<<20 || c.MaxArchiveBytes > 1<<30 {
		return errors.New("GitLab cache requires a canonical HTTPS endpoint, bucket, region and a 1 MiB to 1 GiB archive limit")
	}
	return validateGitLabCA(c.CAPEM)
}

// The stable namespace covers installation scope, exact provider instance and
// target, service and architecture. Native GitLab appends its verified project
// ID and cache key; rotating disposable runners can reuse only this namespace.
func gitlabCachePrefix(t Target, service string) string {
	config := t.Spec.Services[service]
	data, _ := json.Marshal(struct {
		Application, Project, Environment, Service, Architecture string
		Target                                                   actions.ProviderTarget
	}{t.ApplicationID, t.Project, t.Environment, service, config.Architecture, config.Actions.ProviderTarget()})
	sum := sha256.Sum256(data)
	return "hakopod/v1/" + hex.EncodeToString(sum[:])
}

type gitlabCachePolicy struct {
	SchemaVersion   int    `json:"schema_version"`
	Origin          string `json:"origin"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	MaxArchiveBytes int64  `json:"max_archive_bytes"`
}

func gitlabCachePolicyFor(t Target, service string, config *GitLabActionsCache) gitlabCachePolicy {
	return gitlabCachePolicy{SchemaVersion: 1, Origin: "https://" + config.ServerAddress, Bucket: config.Bucket, Prefix: gitlabCachePrefix(t, service), MaxArchiveBytes: config.MaxArchiveBytes}
}

type gitlabNativeCacheS3 struct {
	ServerAddress      string `toml:"ServerAddress"`
	BucketName         string `toml:"BucketName"`
	BucketLocation     string `toml:"BucketLocation"`
	AuthenticationType string `toml:"AuthenticationType"`
	AccessKey          string `toml:"AccessKey"`
	SecretKey          string `toml:"SecretKey"`
	SessionToken       string `toml:"SessionToken,omitempty"`
	PathStyle          bool   `toml:"PathStyle"`
}

type gitlabNativeCacheConfig struct {
	Type                   string              `toml:"Type"`
	Path                   string              `toml:"Path"`
	Shared                 bool                `toml:"Shared"`
	MaxUploadedArchiveSize int64               `toml:"MaxUploadedArchiveSize"`
	RedactURL              bool                `toml:"RedactURL"`
	S3                     gitlabNativeCacheS3 `toml:"s3"`
}

func gitlabCacheConfig(t Target, service string, config *GitLabActionsCache, credentials *actions.GitLabCacheCredentials) *gitlabNativeCacheConfig {
	return &gitlabNativeCacheConfig{
		Type: "hakopod-s3", Path: gitlabCachePrefix(t, service), Shared: true, MaxUploadedArchiveSize: config.MaxArchiveBytes, RedactURL: true,
		S3: gitlabNativeCacheS3{ServerAddress: config.ServerAddress, BucketName: config.Bucket, BucketLocation: config.Region, AuthenticationType: "access-key", AccessKey: credentials.AccessKey, SecretKey: credentials.SecretKey, SessionToken: credentials.SessionToken, PathStyle: true},
	}
}

func cloneGitLabCache(in *GitLabActionsCache) *GitLabActionsCache {
	if in == nil {
		return nil
	}
	out := *in
	out.CAPEM = slices.Clone(in.CAPEM)
	return &out
}
