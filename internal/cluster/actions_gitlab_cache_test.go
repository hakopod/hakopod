package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
)

func TestGitLabCacheCredentialsStayInManagerAndPolicyIsScopeBound(t *testing.T) {
	target, service, registration, runtime := gitlabActionsDevelopmentFixture(t)
	service.Actions.Cache = &spec.ActionsCache{Credential: "runner-cache"}
	target.Spec.Services["runner"] = service
	runtime.Images.CacheProtocolVersion = 1
	runtime.Cache = &GitLabActionsCache{ServerAddress: "cache.example.test", Bucket: "runner-cache", Region: "us-east-1", MaxArchiveBytes: 2 << 20}
	registration.CacheCredentials = &actions.GitLabCacheCredentials{AccessKey: "synthetic-cache-key", SecretKey: "synthetic-secret-cache-key"}
	secret, policy, err := gitlabActionsArtifacts(target, "runner", gitlabActionsFixtureSlot, service, registration, runtime)
	if err != nil {
		t.Fatal(err)
	}
	var config gitlabNativeConfig
	if err = toml.Unmarshal(secret.Data["config.toml"], &config); err != nil {
		t.Fatal(err)
	}
	cache := config.Runners[0].Cache
	if cache == nil || cache.Type != "hakopod-s3" || !cache.Shared || !cache.RedactURL || cache.MaxUploadedArchiveSize != 2<<20 || cache.S3.SecretKey != registration.CacheCredentials.SecretKey || cache.Path != gitlabCachePrefix(target, "runner") {
		t.Fatal("manager lost scoped cache configuration")
	}
	var public gitlabCachePolicy
	if json.Unmarshal([]byte(policy.Data["cache-policy.json"]), &public) != nil || public.Prefix != cache.Path || public.MaxArchiveBytes != cache.MaxUploadedArchiveSize {
		t.Fatal("helper policy differs from manager scope")
	}
	pod, err := gitlabActionsPod(target, "runner", gitlabActionsFixtureSlot, service, registration, runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{pod, policy} {
		data, _ := json.Marshal(value)
		for _, private := range []string{registration.CacheCredentials.AccessKey, registration.CacheCredentials.SecretKey, service.Actions.Cache.Credential} {
			// The bucket happens to share the fixture secret reference's spelling;
			// only provider-issued credential values are confidential.
			if private != service.Actions.Cache.Credential && strings.Contains(string(data), private) {
				t.Fatal("private storage credential entered public runtime data")
			}
		}
	}
	original := gitlabCachePrefix(target, "runner")
	target.Environment = "other"
	if gitlabCachePrefix(target, "runner") == original {
		t.Fatal("cache crossed environment boundary")
	}
	target.Environment = "development"
	service.Architecture = "arm64"
	target.Spec.Services["runner"] = service
	if gitlabCachePrefix(target, "runner") == original {
		t.Fatal("cache crossed architecture boundary")
	}
}

func TestGitLabCacheCannotUseUnqualifiedTransportOrMissingCredentials(t *testing.T) {
	target, service, registration, runtime := gitlabActionsDevelopmentFixture(t)
	service.Actions.Cache = &spec.ActionsCache{Credential: "runner-cache"}
	target.Spec.Services["runner"] = service
	runtime.Cache = &GitLabActionsCache{ServerAddress: "cache.example.test", Bucket: "runner-cache", Region: "us-east-1", MaxArchiveBytes: 2 << 20}
	registration.CacheCredentials = &actions.GitLabCacheCredentials{AccessKey: "synthetic-cache-key", SecretKey: "synthetic-secret-cache-key"}
	if _, _, err := gitlabActionsArtifacts(target, "runner", gitlabActionsFixtureSlot, service, registration, runtime); err == nil {
		t.Fatal("old helper image gained cache without qualification")
	}
	runtime.Images.CacheProtocolVersion = 1
	registration.CacheCredentials = nil
	if _, _, err := gitlabActionsArtifacts(target, "runner", gitlabActionsFixtureSlot, service, registration, runtime); err == nil {
		t.Fatal("cache started without separate credentials")
	}
}

func TestGitLabConfiguredCacheCanBeDisabledByPool(t *testing.T) {
	target, service, registration, runtime := gitlabActionsDevelopmentFixture(t)
	runtime.Images.CacheProtocolVersion = 1
	runtime.Cache = &GitLabActionsCache{ServerAddress: "cache.example.test", Bucket: "runner-cache", Region: "us-east-1", MaxArchiveBytes: 2 << 20}
	secret, policy, err := gitlabActionsArtifacts(target, "runner", gitlabActionsFixtureSlot, service, registration, runtime)
	if err != nil {
		t.Fatal("installation storage made an opted-out pool require credentials", err)
	}
	var native gitlabNativeConfig
	if toml.Unmarshal(secret.Data["config.toml"], &native) != nil || native.Runners[0].Cache != nil || policy.Data["cache-policy.json"] != "" {
		t.Fatal("opted-out pool acquired native cache configuration")
	}
	pod, err := gitlabActionsPod(target, "runner", gitlabActionsFixtureSlot, service, registration, runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pod.Spec.Volumes[4].ConfigMap.Items {
		if strings.HasPrefix(item.Key, "cache-") {
			t.Fatal("opted-out pool mounted cache policy or CA")
		}
	}
}
