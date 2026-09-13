package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func awsBindingFixture(t Target) AWSIdentityBinding {
	return AWSIdentityBinding{Name: "sender", Project: t.Project, Environment: t.Environment, Application: t.Spec.Name, Service: "api", RoleARN: "arn:aws:iam::123456789012:role/hakopod/sender", Region: "ap-south-1"}
}

func awsFakeAudience(kube *fake.Clientset, acceptsAPIAudience bool) {
	kube.PrependReactor("create", "serviceaccounts", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "token" {
			return false, nil, nil
		}
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: "test-sts-audience-token"}}, nil
	})
	kube.PrependReactor("create", "tokenreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: acceptsAPIAudience}}, nil
	})
}

func TestAWSIdentityScopeAndNoImplicitGrant(t *testing.T) {
	target := testTarget(t)
	binding := awsBindingFixture(target)
	c := &Client{options: Options{AWSIdentityBindings: []AWSIdentityBinding{binding}}}
	svc := target.Spec.Services["api"]
	svc.AWSIdentity = binding.Name
	target.Spec.Services["api"] = svc
	if err := c.ValidateAWSIdentities(target.Project, target.Environment, target.Spec); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][]string{
		{"other", target.Environment, target.Spec.Name, "api", binding.Name},
		{target.Project, "production", target.Spec.Name, "api", binding.Name},
		{target.Project, target.Environment, "other", "api", binding.Name},
		{target.Project, target.Environment, target.Spec.Name, "web", binding.Name},
		{target.Project, target.Environment, target.Spec.Name, "api", "other"},
	} {
		if _, err := c.resolveAWSIdentity(values[0], values[1], values[2], values[3], values[4]); err == nil {
			t.Fatalf("unapproved binding accepted: %v", values)
		}
	}
	c.options.AWSIdentityBindings = nil
	if err := c.ValidateAWSIdentities(target.Project, target.Environment, target.Spec); err == nil {
		t.Fatal("unconfigured binding accepted")
	}
	c.options.AWSIdentityBindings = []AWSIdentityBinding{binding}
	target.Spec.Networks["default"] = spec.Network{Internal: true}
	if err := c.ValidateAWSIdentities(target.Project, target.Environment, target.Spec); err == nil {
		t.Fatal("unreachable STS configuration accepted")
	}
}

func TestAWSIdentityProjectionAndAudienceIsolation(t *testing.T) {
	ctx := context.Background()
	target := testTarget(t)
	binding := awsBindingFixture(target)
	kube := fake.NewClientset()
	awsFakeAudience(kube, false)
	c := &Client{kube: kube, options: Options{AWSIdentityBindings: []AWSIdentityBinding{binding}}}
	svc := target.Spec.Services["api"]
	svc.AWSIdentity = binding.Name
	target.Spec.Services["api"] = svc
	wanted := deployment(target, "api", svc, 0)
	if err := c.prepareAWSIdentity(ctx, target, "api", svc, wanted); err != nil {
		t.Fatal(err)
	}
	pod := wanted.Spec.Template.Spec
	if pod.ServiceAccountName != AWSIdentityServiceAccount(binding) || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || len(pod.Volumes) != 1 {
		t.Fatal("unexpected workload service account or volume")
	}
	projection := pod.Volumes[0].Projected
	if projection == nil || len(projection.Sources) != 1 || *projection.DefaultMode != 0440 || projection.Sources[0].ServiceAccountToken.Audience != awsIdentityAudience || *projection.Sources[0].ServiceAccountToken.ExpirationSeconds != 3600 {
		t.Fatal("missing audience-bound, short-lived restricted token projection")
	}
	container := pod.Containers[0]
	if !container.VolumeMounts[0].ReadOnly || container.VolumeMounts[0].SubPath != "" || container.VolumeMounts[0].MountPath != spec.AWSIdentityTokenDirectory {
		t.Fatal("token projection is writable or cannot rotate")
	}
	env := map[string]string{}
	for _, value := range container.Env {
		env[value.Name] = value.Value
	}
	if env["AWS_ROLE_ARN"] != binding.RoleARN || env["AWS_EC2_METADATA_DISABLED"] != "true" || env["AWS_SHARED_CREDENTIALS_FILE"] != "/dev/null" || env["AWS_WEB_IDENTITY_TOKEN_FILE"] != spec.AWSIdentityTokenDirectory+"/token" {
		t.Fatal("SDK credential chain not pinned to approved role and token")
	}
	account, err := kube.CoreV1().ServiceAccounts(Namespace(target.ApplicationID)).Get(ctx, pod.ServiceAccountName, metav1.GetOptions{})
	if err != nil || account.AutomountServiceAccountToken == nil || *account.AutomountServiceAccountToken || len(account.Secrets) != 0 {
		t.Fatal("cluster credential attached to service account", err)
	}
	state, err := c.AWSIdentityStatus(ctx, target, "api")
	if err != nil || state.Status != "prepared" || state.AWSVerified {
		t.Fatal("Kubernetes preparation incorrectly claims verified AWS access", err)
	}
	awsFakeAudience(kube, true)
	if err := c.prepareAWSIdentity(ctx, target, "api", svc, deployment(target, "api", svc, 0)); err == nil || !strings.Contains(err.Error(), "unsafe Kubernetes API audiences") {
		t.Fatal("STS token accepted by Kubernetes was mounted", err)
	}
}

func TestAWSIdentityRefusesUnownedOrCredentialBearingAccount(t *testing.T) {
	for _, unowned := range []bool{true, false} {
		target := testTarget(t)
		binding := awsBindingFixture(target)
		labels := labelsFor(target, "api")
		labels[awsIdentityLabel] = binding.Name
		if unowned {
			labels[ownerKey] = "other"
		}
		kube := fake.NewClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: AWSIdentityServiceAccount(binding), Namespace: Namespace(target.ApplicationID), Labels: labels}, AutomountServiceAccountToken: ptr(!unowned)})
		c := &Client{kube: kube, options: Options{AWSIdentityBindings: []AWSIdentityBinding{binding}}}
		svc := target.Spec.Services["api"]
		svc.AWSIdentity = binding.Name
		if err := c.prepareAWSIdentity(context.Background(), target, "api", svc, deployment(target, "api", svc, 0)); err == nil {
			t.Fatalf("unsafe existing service account accepted; unowned=%v", unowned)
		}
	}
}

func TestAWSIdentityBindingsFileValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identities.toml")
	valid := `schema_version = 1
[[bindings]]
name = "mail-sender"
project = "mail"
environment = "production"
application = "mail"
service = "smtp"
role_arn = "arn:aws:iam::123456789012:role/hakopod/sender"
region = "ap-south-1"
`
	for _, data := range []string{valid, strings.Replace(valid, `project = "mail"`, `project = "*"`, 1), strings.Replace(valid, "arn:aws:iam", "arn:aws-cn:iam", 1), valid + "unsupported = true\n", valid + strings.TrimPrefix(valid, "schema_version = 1\n")} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		bindings, err := ReadAWSIdentityBindingsFile(path)
		if data == valid && (err != nil || len(bindings) != 1) {
			t.Fatal("valid exact binding rejected", err)
		}
		if data != valid && err == nil {
			t.Fatal("invalid or duplicate identity accepted")
		}
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAWSIdentityBindingsFile(path); err == nil {
		t.Fatal("group-writable identity authorization file accepted")
	}
	binding := awsBindingFixture(testTarget(t))
	next := binding
	next.RoleARN += "-changed"
	if AWSIdentityServiceAccount(binding) == AWSIdentityServiceAccount(next) {
		t.Fatal("role change reuses an IAM trust subject")
	}
}
