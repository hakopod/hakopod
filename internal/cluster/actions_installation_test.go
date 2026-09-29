package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// Synthetic qualification reports exercise the loader only. They are never
// live qualification evidence and their referenced images are not published.
func managedActionsReportFixture(t *testing.T) ([]byte, string, GitLabActionsRuntime) {
	t.Helper()
	_, _, _, runtime := gitlabActionsDevelopmentFixture(t)
	var report nativeImageQualification
	report.SchemaVersion, report.Status, report.SentinelOnly, report.PublishedImagesVerified, report.Architecture = 1, "passed", true, true, "amd64"
	report.Source.Commit, report.Source.Version, report.Source.TransportSourceSHA256 = runtime.Images.SourceCommit, runtime.Images.RunnerVersion, runtime.Images.TransportSourceSHA256
	report.Manager = nativeQualifiedImage{ImageRef: runtime.Images.Manager, PlatformManifestDigest: "sha256:" + strings.Repeat("a", 64), BinaryPath: "/usr/bin/gitlab-runner", BinarySHA256: runtime.Images.ManagerBinarySHA256}
	report.Helper = nativeQualifiedImage{ImageRef: runtime.Images.Helper, PlatformManifestDigest: "sha256:" + strings.Repeat("b", 64), BinaryPath: "/usr/bin/gitlab-runner-helper", BinarySHA256: runtime.Images.HelperBinarySHA256}
	report.Policy.Path, report.Policy.SchemaVersion, report.Policy.ReadOnly = gitlabActionsPolicyDirectory+"/transport-policy.json", 1, true
	report.Coverage.Verify.Passed, report.Coverage.Verify.Cases, report.Coverage.Verify.PositiveControls = true, 25, 10
	report.Coverage.Helper.Passed, report.Coverage.Helper.Cases, report.Coverage.Helper.DirectControls = true, 102, 2
	report.Coverage.Helper.UploadRedirects, report.Coverage.Helper.MissingPolicyDownloads, report.Coverage.Helper.AllowlistedDownloads, report.Coverage.Helper.DeniedDowngrades = 25, 25, 40, 10
	report.ReportHashes.Verify, report.ReportHashes.Helper = strings.Repeat("2", 64), strings.Repeat("3", 64)
	report.ImageBinaryVerification.Passed, report.ImageBinaryVerification.TransportSourceSHA256 = true, runtime.Images.TransportSourceSHA256
	report.ImageBinaryVerification.ManagerSHA256, report.ImageBinaryVerification.HelperSHA256 = runtime.Images.ManagerBinarySHA256, runtime.Images.HelperBinarySHA256
	report.Unverified, report.QualificationScope = []string{"synthetic fixture"}, "synthetic fixture"
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), runtime
}

func TestManagedActionsInstallationLoadsBoundedImmutableTrust(t *testing.T) {
	report, digest, runtime := managedActionsReportFixture(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "report.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`schema_version = 1
pod_cidrs = ["10.42.0.0/16"]
service_cidrs = ["10.43.0.0/16"]
[[images]]
name = "amd64"
report_file = "report.json"
report_sha256 = %q
default_job_image = %q
[[gitlab]]
project = "demo"
environment = "development"
application = "builds"
service = "runner"
url = "https://gitlab.com"
project_id = 12
image_pair = "amd64"
`, digest, runtime.Images.DefaultJobImage)
	path := filepath.Join(directory, "actions.toml")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	installation, err := ReadManagedActionsFile(path)
	if err != nil || len(installation.GitLab) != 1 || installation.GitLab[0].Runtime.Images.VerificationReportSHA256 != digest {
		t.Fatal("valid synthetic report rejected", err)
	}
	copy := cloneManagedActionsInstallation(installation)
	installation.GitLab[0].Runtime.ClusterPodCIDRs[0] = "10.99.0.0/16"
	installation.GitLab[0].Target.GitLab.ProjectID = 99
	if copy.GitLab[0].Runtime.ClusterPodCIDRs[0] != "10.42.0.0/16" || copy.GitLab[0].Target.GitLab.ProjectID != 12 {
		t.Fatal("installation configuration was not deeply copied")
	}
	if err := os.WriteFile(filepath.Join(directory, "report.json"), append(report, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedActionsFile(path); err == nil {
		t.Fatal("changed report bytes retained qualification")
	}
	if err := os.WriteFile(path, []byte(config+"unknown_option = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedActionsFile(path); err == nil {
		t.Fatal("unknown configuration field accepted")
	}
}

func TestManagedActionsQualificationRequiresMatchingBinaryEvidence(t *testing.T) {
	data, _, runtime := managedActionsReportFixture(t)
	for _, mutate := range []func(*nativeImageQualification){
		func(r *nativeImageQualification) {
			r.Manager.PlatformManifestDigest = "sha256:" + strings.Repeat("c", 64)
		},
		func(r *nativeImageQualification) { r.Helper.BinarySHA256 = strings.Repeat("a", 64) },
		func(r *nativeImageQualification) { r.PublishedImagesVerified = false },
		func(r *nativeImageQualification) { r.Coverage.Helper.AllowlistedDownloads = 0 },
		func(r *nativeImageQualification) { r.CredentialForwardingDetected = true },
	} {
		var report nativeImageQualification
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		mutate(&report)
		changed, _ := json.Marshal(report)
		sum := sha256.Sum256(changed)
		if _, err := qualifiedGitLabImages(changed, hex.EncodeToString(sum[:]), runtime.Images.DefaultJobImage); err == nil {
			t.Fatal("incomplete or conflicting image evidence was accepted")
		}
	}
}

func managedActionsNetworkFixture(t *testing.T) (*Client, Target, GitLabActionsRuntime) {
	t.Helper()
	target, service, _, runtime := gitlabActionsDevelopmentFixture(t)
	service.Image = runtime.Images.Manager
	target.Spec.Services["runner"] = service
	runtime.ClusterPodCIDRs, runtime.ClusterServiceCIDRs = []string{"10.42.0.0/16"}, []string{"10.43.0.0/16"}
	runtime.ControlPlaneTrust = &actions.GitLabTrustPolicy{BaseURL: "https://gitlab.com"}
	client, kube := gitlabActionsDevelopmentClient(t, target)
	client.execConfig = &rest.Config{Host: "https://10.11.0.1:6443"}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), "development-node", metav1.GetOptions{})
	node.Spec.PodCIDRs = []string{"10.42.0.0/24"}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.11.0.2"}}
	if _, err := kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().Services("default").Create(context.Background(), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes"}, Spec: corev1.ServiceSpec{ClusterIPs: []string{"10.43.0.1"}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.options.ManagedActions = &ManagedActionsInstallation{PodCIDRs: runtime.ClusterPodCIDRs, ServiceCIDRs: runtime.ClusterServiceCIDRs, GitLab: []ManagedGitLabBinding{{Project: target.Project, Environment: target.Environment, Application: target.Spec.Name, Service: "runner", Target: service.Actions.ProviderTarget(), Runtime: runtime}}}
	return client, target, runtime
}

func TestManagedActionsExactServiceBindingCannotBeInferredFromEqualConfig(t *testing.T) {
	c, target, runtime := managedActionsNetworkFixture(t)
	service := target.Spec.Services["runner"]
	target.Spec.Services["identical"] = service
	if _, err := c.ResolveGitLabActions(context.Background(), target, "identical", service); err == nil {
		t.Fatal("identical service borrowed another service's grant")
	}
	resolved, err := c.ResolveGitLabActions(context.Background(), target, "runner", service)
	if err != nil || !reflect.DeepEqual(resolved, runtime) {
		t.Fatal("exact installation binding failed", err)
	}
	c.options.ManagedActions = nil
	if _, err := c.ResolveGitLabActions(context.Background(), target, "runner", service); err == nil {
		t.Fatal("removed binding still admits execution")
	}
	if _, err := c.GitLabActionsDeniedNetworks(context.Background(), runtime); err != nil {
		t.Fatal("original cleanup trust was stranded after configuration removal", err)
	}
}

func TestGitLabSlotPolicyRevokesPrivateGrantWhenNodeInventoryChanges(t *testing.T) {
	c, target, runtime := managedActionsNetworkFixture(t)
	runtime.TransportPolicy.ArtifactOrigins = []string{"https://10.30.0.7"}
	runtime.PrivateDestinations = []ManagedActionsDestination{{Origin: "https://10.30.0.7", Addresses: []string{"10.30.0.7/32"}}}
	ctx := context.Background()
	if err := c.RefreshGitLabActionsNetwork(ctx, target, "runner", gitlabActionsFixtureSlot, runtime); err != nil {
		t.Fatal(err)
	}
	policies := c.kube.NetworkingV1().NetworkPolicies(Namespace(target.ApplicationID))
	policy, err := policies.Get(ctx, "actions-"+gitlabActionsFixtureSlot, metav1.GetOptions{})
	if err != nil || policy.Spec.PodSelector.MatchLabels[gitlabActionsSlotLabel] != gitlabActionsFixtureSlot {
		t.Fatal("slot selector missing", err)
	}
	allowed := false
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			allowed = allowed || (peer.IPBlock != nil && peer.IPBlock.CIDR == "10.30.0.7/32")
		}
	}
	if !allowed {
		t.Fatal("exact private grant missing")
	}
	node, _ := c.kube.CoreV1().Nodes().Get(ctx, "development-node", metav1.GetOptions{})
	node.Status.Addresses = append(node.Status.Addresses, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "10.30.0.7"})
	if _, err := c.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.managedActionsNetworks.at = time.Time{}
	if err := c.RefreshGitLabActionsNetwork(ctx, target, "runner", gitlabActionsFixtureSlot, runtime); err == nil {
		t.Fatal("new node address retained an application grant")
	}
	policy, _ = policies.Get(ctx, policy.Name, metav1.GetOptions{})
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 1 || policy.Spec.Egress[0].To[0].IPBlock != nil {
		t.Fatal("unsafe slot was not reduced to DNS-only egress")
	}
	for _, shared := range policiesForTest(target) {
		if shared.Name == "hakopod-service-runner" && len(shared.Spec.PodSelector.MatchExpressions) == 0 {
			t.Fatal("ordinary service policy still selects native slots")
		}
	}
}

func policiesForTest(target Target) []*networkingv1.NetworkPolicy { return policies(target) }
