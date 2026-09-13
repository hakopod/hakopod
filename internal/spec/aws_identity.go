package spec

import (
	"fmt"
	"strings"
)

// AWSIdentityTokenDirectory is reserved for the audience-bound workload token.
const AWSIdentityTokenDirectory = "/var/run/secrets/hakopod/aws"

func validateAWSIdentity(s Service) error {
	if s.AWSIdentity == "" {
		return nil
	}
	if !runtimeName.MatchString(s.AWSIdentity) {
		return fmt.Errorf("aws_identity: use an operator-approved binding name of at most 63 lowercase letters, digits or hyphens")
	}
	// Keep one credential chain. The SDK must not select a static key, shared
	// profile, container endpoint or application-chosen role before web identity.
	for key := range s.Env {
		if awsIdentityReservedEnv(key) {
			return fmt.Errorf("env.%s: managed by aws_identity", key)
		}
	}
	for key := range s.Secrets {
		if awsIdentityReservedEnv(key) {
			return fmt.Errorf("secrets.%s: managed by aws_identity", key)
		}
	}
	paths := []string{}
	if s.Volume != nil {
		paths = append(paths, s.Volume.MountPath)
	}
	for _, m := range s.Mounts {
		paths = append(paths, m.MountPath)
	}
	for _, m := range s.CertificateMounts {
		paths = append(paths, m.MountPath)
	}
	for _, m := range s.TemporaryMounts {
		paths = append(paths, m.MountPath)
	}
	for _, p := range paths {
		if p == AWSIdentityTokenDirectory || strings.HasPrefix(p, AWSIdentityTokenDirectory+"/") || strings.HasPrefix(AWSIdentityTokenDirectory, p+"/") {
			return fmt.Errorf("aws_identity: a mount overlaps the reserved token directory")
		}
	}
	return nil
}

func awsIdentityReservedEnv(key string) bool {
	switch strings.ToUpper(key) {
	case "AWS_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN", "AWS_CREDENTIAL_EXPIRATION", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_STS_REGIONAL_ENDPOINTS", "AWS_EC2_METADATA_DISABLED", "AWS_EC2_METADATA_SERVICE_ENDPOINT", "AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_STS", "AWS_CA_BUNDLE":
		return true
	}
	return false
}
