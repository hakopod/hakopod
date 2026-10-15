package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func privateBindingFixture(t Target) PrivateEgressBinding {
	return PrivateEgressBinding{Name: "orders-db", Project: t.Project, Environment: t.Environment, Application: t.Spec.Name, Services: []string{"api"}, CIDRs: []string{"10.20.30.0/24"}, Ports: []int32{5432}}
}
func withPrivateRef(t Target) Target {
	s := t.Spec.Services["api"]
	s.PrivateEgress = []string{"orders-db"}
	t.Spec.Services["api"] = s
	return t
}
func TestPrivateEgressScopeAndCloud(t *testing.T) {
	target := withPrivateRef(testTarget(t))
	b := privateBindingFixture(target)
	c := &Client{options: Options{PrivateEgressBindings: []PrivateEgressBinding{b}}}
	if _, err := c.resolvePrivateEgress(context.Background(), target, false); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "project", "environment", "application", "service", "missing"} {
		bad := b
		switch field {
		case "name":
			bad.Name = "other"
		case "project":
			bad.Project = "other"
		case "environment":
			bad.Environment = "other"
		case "application":
			bad.Application = "other"
		case "service":
			bad.Services = []string{"web"}
		}
		c.options.PrivateEgressBindings = []PrivateEgressBinding{bad}
		if field == "missing" {
			c.options.PrivateEgressBindings = nil
		}
		if _, err := c.resolvePrivateEgress(context.Background(), target, false); err == nil {
			t.Fatalf("scope bypass: %s", field)
		}
		// Delivery must reject before reading Kubernetes or accepting work.
		if err := c.validateDeliveryPolicy(context.Background(), target); err == nil {
			t.Fatalf("delivery accepted %s", field)
		}
	}
	c.options.PrivateEgressBindings = []PrivateEgressBinding{b}
	c.options.DeploymentMode = DeploymentManagedCloud
	if _, err := c.resolvePrivateEgress(context.Background(), target, false); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("Cloud grant accepted", err)
	}
	if err := c.ValidateCloudSpec(target.Spec); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("Cloud spec accepted", err)
	}
	c.options.DeploymentMode = DeploymentSelfHosted
	c.options.WorkloadPolicy = func(context.Context, string, string, spec.Application) (WorkloadPolicy, error) {
		return WorkloadPolicy{}, nil
	}
	if _, err := c.resolvePrivateEgress(context.Background(), target, false); !errors.Is(err, ErrCloudLimit) {
		t.Fatal("embedded pool grant accepted", err)
	}
}

func TestPrivateEgressPolicyAndRemoval(t *testing.T) {
	target := withPrivateRef(testTarget(t))
	target.Spec.Networks["default"] = spec.Network{Internal: true}
	c := &Client{kube: fake.NewClientset(), options: Options{PrivateEgressBindings: []PrivateEgressBinding{privateBindingFixture(target)}}}
	var err error
	target.privateEgress, err = c.resolvePrivateEgress(context.Background(), target, false)
	if err != nil {
		t.Fatal(err)
	}
	want := privateEgressRules(c.options.PrivateEgressBindings)[0]
	hasGrant := func(p *networkingv1.NetworkPolicy) bool {
		for _, r := range p.Spec.Egress {
			if reflect.DeepEqual(r, want) {
				return true
			}
		}
		return false
	}
	for _, p := range policies(target) {
		if hasGrant(p) != (p.Name == "hakopod-service-api") {
			t.Fatal("grant leaked or disappeared", p.Name)
		}
	}
	if len(want.To) != 1 || want.To[0].IPBlock.CIDR != "10.20.30.0/24" || len(want.Ports) != 1 || want.Ports[0].Port.IntVal != 5432 || *want.Ports[0].Protocol != "TCP" {
		t.Fatal("grant broadened")
	}
	ctx := context.Background()
	if err := c.applyPolicies(ctx, target); err != nil {
		t.Fatal(err)
	}
	svc := target.Spec.Services["api"]
	svc.PrivateEgress = nil
	target.Spec.Services["api"] = svc
	target.privateEgress, err = c.resolvePrivateEgress(context.Background(), target, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyPolicies(ctx, target); err != nil {
		t.Fatal(err)
	}
	p, err := c.kube.NetworkingV1().NetworkPolicies(Namespace(target.ApplicationID)).Get(ctx, "hakopod-service-api", metav1.GetOptions{})
	if err != nil || hasGrant(p) {
		t.Fatal("removed grant retained", err)
	}
	// Defense in depth: a workload policy must not consume private grants even
	// if an internal caller accidentally supplies a pre-resolved target.
	target.privateEgress = map[string][]PrivateEgressBinding{"api": c.options.PrivateEgressBindings}
	target.policy = &WorkloadPolicy{}
	for _, p := range policies(target) {
		if hasGrant(p) {
			t.Fatal("runtime policy bypass")
		}
	}
}

func TestPrivateEgressBindingsValidation(t *testing.T) {
	b := privateBindingFixture(testTarget(t))
	for _, cidr := range []string{"10.20.30.0/24", "172.31.32.0/20", "192.168.1.2/32"} {
		b.CIDRs = []string{cidr}
		if err := ValidatePrivateEgressBindings([]PrivateEgressBinding{b}); err != nil {
			t.Fatal(cidr, err)
		}
	}
	for _, cidr := range []string{"0.0.0.0/0", "10.0.0.0/8", "10.42.1.0/24", "10.43.1.1/32", "169.254.169.254/32", "127.0.0.1/32", "100.64.0.0/16", "8.8.8.8/32", "fd00::/64", "::ffff:10.1.1.1/128", "10.20.30.1/24", "db.example.com"} {
		b.CIDRs = []string{cidr}
		if err := ValidatePrivateEgressBindings([]PrivateEgressBinding{b}); err == nil {
			t.Fatalf("unsafe CIDR accepted: %s", cidr)
		}
	}
	b = privateBindingFixture(testTarget(t))
	for _, ports := range [][]int32{nil, {0}, {65536}, {5432, 5432}} {
		bad := b
		bad.Ports = ports
		if err := ValidatePrivateEgressBindings([]PrivateEgressBinding{bad}); err == nil {
			t.Fatal("invalid ports accepted", ports)
		}
	}
	for _, services := range [][]string{nil, {"*"}, {"api", "api"}} {
		bad := b
		bad.Services = services
		if err := ValidatePrivateEgressBindings([]PrivateEgressBinding{bad}); err == nil {
			t.Fatal("invalid scope accepted", services)
		}
	}
	if err := ValidatePrivateEgressBindings([]PrivateEgressBinding{b, b}); err == nil {
		t.Fatal("duplicate binding accepted")
	}
}
func TestPrivateEgressFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.toml")
	valid := `schema_version = 1
[[bindings]]
name = "orders-db"
project = "shop"
environment = "production"
application = "orders"
services = ["api", "setup"]
cidrs = ["10.20.30.0/24"]
ports = [5432]
`
	for _, data := range []string{valid, valid + "unknown = 'DO-NOT-ECHO'", strings.Replace(valid, "schema_version = 1", "schema_version = 2", 1), strings.Repeat("#", (64<<10)+1)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadPrivateEgressBindingsFile(path)
		if data == valid && (err != nil || len(got) != 1) {
			t.Fatal("valid file rejected", err)
		}
		if data != valid && (err == nil || strings.Contains(err.Error(), "DO-NOT-ECHO")) {
			t.Fatal("unsafe error or invalid file accepted", err)
		}
	}
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0660); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateEgressBindingsFile(path); err == nil {
		t.Fatal("group-writable authorization accepted")
	}
	if _, err := ReadPrivateEgressBindingsFile(filepath.Dir(path)); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestPrivateEgressWarnings(t *testing.T) {
	target := testTarget(t)
	api := target.Spec.Services["api"]
	api.Env = map[string]string{
		"DATABASE_URL": "postgres://u:p@172.31.33.237:5432/app?sslmode=verify-full",
		"CACHE":        "redis.internal.example:6379",
		"PUBLIC":       "https://api.example.com:443",
		"PEER":         "worker:8080",
	}
	target.Spec.Services["api"] = api
	lookup := func(host string) []netip.Addr {
		switch host {
		case "redis.internal.example":
			return []netip.Addr{netip.MustParseAddr("10.20.40.7")}
		case "api.example.com":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}
		}
		return nil
	}
	got := privateEgressWarnings(nil, target.Project, target.Environment, target.Spec, lookup)
	if len(got) != 2 || !strings.Contains(got[0], "CACHE points at private address 10.20.40.7:6379 (redis.internal.example)") || !strings.Contains(got[1], "DATABASE_URL points at private address 172.31.33.237:5432") || !strings.Contains(got[1], `private_egress = ["<binding-name>"] to [services.api]`) {
		t.Fatalf("warnings = %q", got)
	}
	b := PrivateEgressBinding{Name: "orders-db", Project: target.Project, Environment: target.Environment, Application: target.Spec.Name, Services: []string{"api"}, CIDRs: []string{"172.31.32.0/20"}, Ports: []int32{5432}}
	// A binding that exists but is not referenced still warns.
	if got := privateEgressWarnings([]PrivateEgressBinding{b}, target.Project, target.Environment, target.Spec, lookup); len(got) != 2 {
		t.Fatalf("unreferenced binding: %q", got)
	}
	api.PrivateEgress = []string{"orders-db"}
	target.Spec.Services["api"] = api
	if got := privateEgressWarnings([]PrivateEgressBinding{b}, target.Project, target.Environment, target.Spec, lookup); len(got) != 1 || !strings.Contains(got[0], "CACHE") {
		t.Fatalf("covered destination still warned: %q", got)
	}
	if got := (&Client{options: Options{DeploymentMode: DeploymentManagedCloud}}).PrivateEgressWarnings(context.Background(), target.Project, target.Environment, target.Spec); got != nil {
		t.Fatalf("cloud warned: %q", got)
	}
}

func TestPrivateEgressDatabaseSourceWithoutRestart(t *testing.T) {
	ctx := context.Background()
	target := withPrivateRef(testTarget(t))
	stored := []json.RawMessage{}
	c := &Client{options: Options{PrivateEgressSource: func(context.Context) ([]json.RawMessage, error) { return stored, nil }}}
	if _, err := c.resolvePrivateEgress(ctx, target, false); err == nil {
		t.Fatal("unapproved destination resolved")
	}
	raw, _ := json.Marshal(privateBindingFixture(target))
	stored = []json.RawMessage{raw}
	got, err := c.resolvePrivateEgress(ctx, target, false)
	if err != nil || len(got["api"]) != 1 {
		t.Fatalf("database binding not picked up: %v %v", got, err)
	}
	stored = nil
	if got, err = c.resolvePrivateEgress(ctx, target, true); err != nil || len(got["api"]) != 0 {
		t.Fatalf("revoked binding not dropped on refresh: %v %v", got, err)
	}
	c.options.PrivateEgressBindings = []PrivateEgressBinding{privateBindingFixture(target)}
	stored = []json.RawMessage{raw}
	if _, err = c.resolvePrivateEgress(ctx, target, false); err == nil {
		t.Fatal("file and database name collision accepted")
	}
	if err = c.ValidatePrivateEgressCandidate(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = DecodePrivateEgressBindings([]json.RawMessage{json.RawMessage(`{"name":"x","extra":1}`)}); err == nil {
		t.Fatal("unknown stored field accepted")
	}
}
