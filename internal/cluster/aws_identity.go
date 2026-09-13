package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const awsIdentityAudience = "sts.amazonaws.com"
const awsIdentityLabel = "hakopod.io/aws-identity"

// Bindings are controlled by the installer, never by an application revision.
// An exact project/environment/application/service tuple receives one role.
type AWSIdentityBinding struct {
	Name        string `toml:"name" json:"name"`
	Project     string `toml:"project" json:"project"`
	Environment string `toml:"environment" json:"environment"`
	Application string `toml:"application" json:"application"`
	Service     string `toml:"service" json:"service"`
	RoleARN     string `toml:"role_arn" json:"role_arn"`
	Region      string `toml:"region" json:"region"`
}

type AWSIdentityState struct {
	Binding        string `json:"binding"`
	RoleARN        string `json:"role_arn"`
	Region         string `json:"region"`
	ServiceAccount string `json:"service_account"`
	TokenAudience  string `json:"token_audience"`
	Status         string `json:"status"`
	AWSVerified    bool   `json:"aws_verified"`
	Message        string `json:"message,omitempty"`
}

var awsBindingName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var awsScopeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)
var awsRoleARN = regexp.MustCompile(`^arn:(aws|aws-us-gov|aws-cn):iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
var awsRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z]+){1,3}-[0-9]$`)

func ValidateAWSIdentityBindings(bindings []AWSIdentityBinding) error {
	if len(bindings) > 128 {
		return fmt.Errorf("AWS identities: at most 128 bindings are supported")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !awsBindingName.MatchString(b.Name) || seen[b.Name] {
			return fmt.Errorf("AWS identities: binding names must be unique lowercase names of at most 63 characters")
		}
		seen[b.Name] = true
		for _, scope := range []string{b.Project, b.Environment, b.Application, b.Service} {
			if !awsScopeName.MatchString(scope) {
				return fmt.Errorf("AWS identity %s: project, environment, application and service require exact names", b.Name)
			}
		}
		if !awsRoleARN.MatchString(b.RoleARN) || strings.HasSuffix(b.RoleARN, "/") || strings.Contains(b.RoleARN, "//") || !awsRegion.MatchString(b.Region) {
			return fmt.Errorf("AWS identity %s: use an IAM role ARN and AWS region", b.Name)
		}
		partition := strings.Split(b.RoleARN, ":")[1]
		if (partition == "aws-cn") != strings.HasPrefix(b.Region, "cn-") || (partition == "aws-us-gov") != strings.HasPrefix(b.Region, "us-gov-") {
			return fmt.Errorf("AWS identity %s: role partition and region do not match", b.Name)
		}
	}
	return nil
}

func ReadAWSIdentityBindingsFile(path string) ([]AWSIdentityBinding, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read AWS identity bindings file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("AWS identity bindings must be a regular file of at most 64 KiB, not writable by group or others")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, fmt.Errorf("cannot read bounded AWS identity bindings file")
	}
	var config struct {
		SchemaVersion int                  `toml:"schema_version"`
		Bindings      []AWSIdentityBinding `toml:"bindings"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("AWS identities require valid strict TOML with schema_version = 1")
	}
	if err := ValidateAWSIdentityBindings(config.Bindings); err != nil {
		return nil, err
	}
	return config.Bindings, nil
}

func (c *Client) resolveAWSIdentity(project, environment, application, service, ref string) (AWSIdentityBinding, error) {
	if err := ValidateAWSIdentityBindings(c.options.AWSIdentityBindings); err != nil {
		return AWSIdentityBinding{}, err
	}
	for _, binding := range c.options.AWSIdentityBindings {
		if binding.Name == ref && binding.Project == project && binding.Environment == environment && binding.Application == application && binding.Service == service {
			return binding, nil
		}
	}
	return AWSIdentityBinding{}, fmt.Errorf("%s: aws_identity is not approved for this project, environment, application and service", service)
}

// ValidateAWSIdentities is also called before accepting a durable deployment.
func (c *Client) ValidateAWSIdentities(project, environment string, app spec.Application) error {
	for _, name := range spec.Names(app) {
		if app.Services[name].AWSIdentity == "" {
			continue
		}
		if _, err := c.resolveAWSIdentity(project, environment, app.Name, name, app.Services[name].AWSIdentity); err != nil {
			return err
		}
		external := false
		for _, network := range app.Services[name].Networks {
			external = external || !app.Networks[network].Internal
		}
		if !external {
			return fmt.Errorf("%s: aws_identity requires an egress-enabled network to reach AWS STS", name)
		}
	}
	return nil
}

// Include the binding scope and role in the subject. A new role or binding
// requires an explicit IAM trust update instead of reusing an existing subject.
func AWSIdentityServiceAccount(binding AWSIdentityBinding) string {
	data := strings.Join([]string{binding.Name, binding.Project, binding.Environment, binding.Application, binding.Service, binding.RoleARN, binding.Region}, "\x00")
	return fmt.Sprintf("hp-aws-%x", sha256.Sum256([]byte(data)))[:39]
}

func (c *Client) prepareAWSIdentity(ctx context.Context, t Target, name string, svc spec.Service, wanted *appsv1.Deployment) error {
	if svc.AWSIdentity == "" {
		return nil
	}
	b, err := c.resolveAWSIdentity(t.Project, t.Environment, t.Spec.Name, name, svc.AWSIdentity)
	if err != nil {
		return err
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	accountName := AWSIdentityServiceAccount(b)
	labels := labelsFor(t, name)
	labels[awsIdentityLabel] = b.Name
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: accountName, Namespace: Namespace(t.ApplicationID), Labels: labels}, AutomountServiceAccountToken: ptr(false)}
	api := c.kube.CoreV1().ServiceAccounts(account.Namespace)
	old, err := api.Get(ctx, accountName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, account, metav1.CreateOptions{})
	} else if err == nil {
		if err = owned(old, t); err != nil {
			return err
		}
		if old.Labels[serviceKey] != name || old.Labels[awsIdentityLabel] != b.Name || old.AutomountServiceAccountToken == nil || *old.AutomountServiceAccountToken || len(old.Secrets) != 0 || len(old.ImagePullSecrets) != 0 {
			return fmt.Errorf("AWS identity service account ownership or credential settings changed")
		}
	}
	if err != nil {
		return err
	}
	// The API audience must exclude STS. Detect a misconfigured API server
	// before giving the workload a token it could use as cluster credentials.
	token, err := api.CreateToken(ctx, accountName, &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{Audiences: []string{awsIdentityAudience}, ExpirationSeconds: ptr(int64(600))}}, metav1.CreateOptions{})
	if err != nil || token.Status.Token == "" {
		return fmt.Errorf("cannot verify AWS identity token audience isolation")
	}
	review, err := c.kube.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token.Status.Token}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("cannot verify AWS identity token audience isolation")
	}
	if review.Status.Authenticated {
		return fmt.Errorf("unsafe Kubernetes API audiences: sts.amazonaws.com must not authenticate to the Kubernetes API")
	}
	pod := &wanted.Spec.Template.Spec
	pod.ServiceAccountName = accountName
	pod.AutomountServiceAccountToken = ptr(false)
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "hakopod-aws-identity", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		DefaultMode: ptr(int32(0440)), Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: awsIdentityAudience, ExpirationSeconds: ptr(int64(3600)), Path: "token"}}},
	}}})
	container := &pod.Containers[0]
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "hakopod-aws-identity", MountPath: spec.AWSIdentityTokenDirectory, ReadOnly: true})
	container.Env = append(container.Env,
		corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: b.RoleARN},
		corev1.EnvVar{Name: "AWS_WEB_IDENTITY_TOKEN_FILE", Value: spec.AWSIdentityTokenDirectory + "/token"},
		corev1.EnvVar{Name: "AWS_ROLE_SESSION_NAME", Value: "hakopod-" + name},
		corev1.EnvVar{Name: "AWS_REGION", Value: b.Region},
		corev1.EnvVar{Name: "AWS_DEFAULT_REGION", Value: b.Region},
		corev1.EnvVar{Name: "AWS_STS_REGIONAL_ENDPOINTS", Value: "regional"},
		corev1.EnvVar{Name: "AWS_EC2_METADATA_DISABLED", Value: "true"},
		corev1.EnvVar{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/dev/null"},
		corev1.EnvVar{Name: "AWS_CONFIG_FILE", Value: "/dev/null"},
	)
	return nil
}

// Status reports Kubernetes preparation only. AWS role assumption is verified
// separately against AWS; a created service account is never reported as proof.
func (c *Client) AWSIdentityStatus(ctx context.Context, t Target, name string) (*AWSIdentityState, error) {
	svc, ok := t.Spec.Services[name]
	if !ok {
		return nil, fmt.Errorf("service not found")
	}
	if svc.AWSIdentity == "" {
		return nil, nil
	}
	state := &AWSIdentityState{Binding: svc.AWSIdentity, TokenAudience: awsIdentityAudience, Status: "unavailable", AWSVerified: false}
	b, err := c.resolveAWSIdentity(t.Project, t.Environment, t.Spec.Name, name, svc.AWSIdentity)
	if err != nil {
		state.Message = "AWS identity binding is missing or is no longer approved for this service."
		return state, nil
	}
	state.RoleARN, state.Region, state.ServiceAccount = b.RoleARN, b.Region, AWSIdentityServiceAccount(b)
	account, err := c.kube.CoreV1().ServiceAccounts(Namespace(t.ApplicationID)).Get(ctx, state.ServiceAccount, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		state.Status = "configured"
		state.Message = "AWS identity service account has not been prepared."
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if owned(account, t) != nil || account.DeletionTimestamp != nil || account.Labels[serviceKey] != name || account.Labels[awsIdentityLabel] != b.Name || account.AutomountServiceAccountToken == nil || *account.AutomountServiceAccountToken || len(account.Secrets) != 0 || len(account.ImagePullSecrets) != 0 {
		state.Message = "AWS identity service account is unavailable or its ownership and credential settings changed."
		return state, nil
	}
	state.Status = "prepared"
	state.Message = "Kubernetes identity is prepared. AWS role assumption and application permissions remain unverified."
	return state, nil
}

// Delete unused owned accounts after a successful rollout. Accounts still used
// by terminating pods are retained so draining connections can finish.
func (c *Client) cleanupAWSIdentities(ctx context.Context, t Target) error {
	keep := map[string]bool{}
	relevant := false
	for _, name := range spec.Names(t.Spec) {
		ref := t.Spec.Services[name].AWSIdentity
		if ref == "" {
			continue
		}
		relevant = true
		binding, err := c.resolveAWSIdentity(t.Project, t.Environment, t.Spec.Name, name, ref)
		if err != nil {
			return err
		}
		keep[AWSIdentityServiceAccount(binding)] = true
	}
	if t.Previous != nil {
		for _, svc := range t.Previous.Services {
			relevant = relevant || svc.AWSIdentity != ""
		}
	}
	if !relevant {
		return nil
	}
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID)
	accounts, err := c.kube.CoreV1().ServiceAccounts(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: selector + "," + awsIdentityLabel, Limit: 200})
	if err != nil {
		return err
	}
	if accounts.Continue != "" {
		return fmt.Errorf("too many AWS identity service accounts for bounded cleanup")
	}
	pods, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "status.phase!=Succeeded,status.phase!=Failed", Limit: 100})
	if err != nil {
		return err
	}
	if pods.Continue != "" {
		return fmt.Errorf("too many pods for AWS identity cleanup")
	}
	for _, pod := range pods.Items {
		keep[pod.Spec.ServiceAccountName] = true
	}
	for _, account := range accounts.Items {
		if keep[account.Name] || !strings.HasPrefix(account.Name, "hp-aws-") {
			continue
		}
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		if err := owned(&account, t); err != nil {
			return err
		}
		if err := c.kube.CoreV1().ServiceAccounts(account.Namespace).Delete(ctx, account.Name, deleteOptions(&account)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
