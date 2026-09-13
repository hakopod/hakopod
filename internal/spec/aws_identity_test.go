package spec

import (
	"strings"
	"testing"
)

func TestAWSIdentityTOMLAndDiff(t *testing.T) {
	base := "schema_version = 1\nname = 'mail'\n[services.smtp]\nimage = 'python:3.13'\n"
	before, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse([]byte(base + "aws_identity = 'mail-sender'\n"))
	if err != nil || after.Services["smtp"].AWSIdentity != "mail-sender" {
		t.Fatal("identity reference did not parse", err)
	}
	changes := Diff(&before, after)
	if len(changes) != 1 || changes[0].Field != "aws_identity" || changes[0].After != "mail-sender" || changes[0].Sensitive {
		t.Fatalf("identity permission change not reviewable: %+v", changes)
	}
	for _, bad := range []string{"arn:aws:iam::123456789012:role/admin", "../admin", "*", strings.Repeat("a", 64)} {
		if _, err := Parse([]byte(base + "aws_identity = '" + bad + "'\n")); err == nil {
			t.Fatalf("unscoped role reference accepted: %s", bad)
		}
	}
}

func TestAWSIdentityCredentialOverridesRejected(t *testing.T) {
	for _, key := range []string{"AWS_ROLE_ARN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_CONFIG_FILE", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_EC2_METADATA_DISABLED", "AWS_ENDPOINT_URL_STS", "AWS_CA_BUNDLE"} {
		for _, secret := range []bool{false, true} {
			s := Service{AWSIdentity: "sender"}
			if secret {
				s.Secrets = map[string]SecretRef{key: {Ref: "other"}}
			} else {
				s.Env = map[string]string{key: "other"}
			}
			if validateAWSIdentity(s) == nil {
				t.Fatalf("credential override accepted: %s secret=%v", key, secret)
			}
		}
	}
	if err := validateAWSIdentity(Service{AWSIdentity: "sender", Env: map[string]string{"AWS_S3_USE_ARN_REGION": "true", "MAIL_FROM": "sender@example.com"}}); err != nil {
		t.Fatal("non-credential SDK settings rejected", err)
	}
	if err := validateAWSIdentity(Service{AWSIdentity: "sender", TemporaryMounts: []TemporaryMount{{MountPath: "/var/run"}}}); err == nil {
		t.Fatal("token path could be hidden by a mount")
	}
}
