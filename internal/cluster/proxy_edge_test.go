package cluster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func edgeTestPolicy() EdgePolicy {
	return EdgePolicy{Enabled: true, Rules: []EdgeRule{{ID: "api", Host: "api.example.test", PathPrefix: "/api", AllowCIDRs: []string{"192.0.2.0/24"}, DenyCIDRs: []string{"192.0.2.5"}, RequestsPerSecond: 10}}}
}

func edgeTestClient() *Client {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "ingress", ResourceVersion: "100", Labels: map[string]string{"app.kubernetes.io/instance": "hakopod", "app.kubernetes.io/name": "kubernetes-ingress"}, Annotations: map[string]string{"meta.helm.sh/release-name": "hakopod", "meta.helm.sh/release-namespace": "ingress", "operator.example/managed": "preserved"}}, Data: map[string]string{"timeout-client": "30s", "backend-config-snippet": "# operator backend setting", "frontend-config-snippet": "# operator frontend setting", "global-config-snippet": "# operator global setting", "other-controller-setting": "preserved"}}
	return &Client{kube: fake.NewClientset(cm), options: Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"}, edgeAck: func(context.Context, EdgePolicy) error { return nil }}
}

func TestEdgePolicyNormalizationAndBounds(t *testing.T) {
	input := edgeTestPolicy()
	input.ClientIPSource = "trusted_proxy"
	input.TrustedProxyCIDRs = []string{" 10.0.0.8/24 ", "2001:db8::1"}
	input.ClientIPHeader = "CF-Connecting-IP"
	input.CountryHeader = "CF-IPCountry"
	input.Rules[0].Host = " API.Example.Test "
	input.Rules[0].AllowCountries = []string{"us", " CA "}
	input.Rules[0].PathPrefix = ""
	policy, err := NormalizeEdgePolicy(input)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Rules[0].Host != "api.example.test" || policy.Rules[0].PathPrefix != "/" || policy.TrustedProxyCIDRs[0] != "10.0.0.0/24" || !reflect.DeepEqual(policy.Rules[0].AllowCountries, []string{"CA", "US"}) {
		t.Fatal("policy was not canonicalized")
	}
	policy.Rules[0].AllowCIDRs[0] = "changed"
	policy.TrustedProxyCIDRs[0] = "changed"
	if input.Rules[0].AllowCIDRs[0] != "192.0.2.0/24" || input.TrustedProxyCIDRs[0] != " 10.0.0.8/24 " || input.Rules[0].Host != " API.Example.Test " {
		t.Fatal("normalization mutated the caller's draft")
	}
	defaultPolicy, err := NormalizeEdgePolicy(EdgePolicy{})
	if err != nil || defaultPolicy.Enabled || defaultPolicy.ClientIPSource != "connection" || defaultPolicy.Rules == nil || defaultPolicy.TrustedProxyCIDRs == nil {
		t.Fatal("default edge policy is not explicit and disabled")
	}
	for _, tc := range []struct {
		name string
		edit func(*EdgePolicy)
	}{
		{"unknown source", func(p *EdgePolicy) { p.ClientIPSource = "xff" }},
		{"connection with header", func(p *EdgePolicy) { p.ClientIPHeader = "X-Real-IP" }},
		{"trusted source without peers", func(p *EdgePolicy) { p.ClientIPSource = "trusted_proxy"; p.ClientIPHeader = "X-Real-IP" }},
		{"country without trust", func(p *EdgePolicy) { p.Rules[0].AllowCountries = []string{"US"} }},
		{"reserved country", func(p *EdgePolicy) { p.Rules[0].AllowCountries = []string{"XX"} }},
		{"negative rate", func(p *EdgePolicy) { p.Rules[0].RequestsPerSecond = -1 }},
		{"unbounded rate", func(p *EdgePolicy) { p.Rules[0].RequestsPerSecond = 100001 }},
		{"ID injection", func(p *EdgePolicy) { p.Rules[0].ID = "api\nhttp-request allow" }},
		{"wildcard host", func(p *EdgePolicy) { p.Rules[0].Host = "*.example.test" }},
		{"host port", func(p *EdgePolicy) { p.Rules[0].Host += ":443" }},
		{"host injection", func(p *EdgePolicy) { p.Rules[0].Host += "\nbackend foreign" }},
		{"relative path", func(p *EdgePolicy) { p.Rules[0].PathPrefix = "api" }},
		{"ambiguous path", func(p *EdgePolicy) { p.Rules[0].PathPrefix = "/x/../api" }},
		{"encoded path", func(p *EdgePolicy) { p.Rules[0].PathPrefix = "/%61pi" }},
		{"empty network", func(p *EdgePolicy) { p.Rules[0].AllowCIDRs = []string{""} }},
		{"network injection", func(p *EdgePolicy) { p.Rules[0].AllowCIDRs = []string{"192.0.2.1\nhttp-request allow"} }},
		{"mapped address", func(p *EdgePolicy) { p.Rules[0].AllowCIDRs = []string{"::ffff:192.0.2.1"} }},
		{"zone address", func(p *EdgePolicy) { p.Rules[0].AllowCIDRs = []string{"fe80::1%eth0"} }},
		{"duplicate network", func(p *EdgePolicy) { p.Rules[0].AllowCIDRs = []string{"192.0.2.1/24", "192.0.2.2/24"} }},
		{"duplicate rule", func(p *EdgePolicy) { p.Rules = append(p.Rules, p.Rules[0]) }},
		{"duplicate selector", func(p *EdgePolicy) { other := p.Rules[0]; other.ID = "other"; p.Rules = append(p.Rules, other) }},
		{"long path", func(p *EdgePolicy) { p.Rules[0].PathPrefix = "/" + strings.Repeat("x", 128) }},
		{"disabled invalid policy", func(p *EdgePolicy) { p.Enabled = false; p.Rules[0].RequestsPerSecond = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := edgeTestPolicy()
			tc.edit(&policy)
			if err := ValidateEdgePolicy(policy); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	for _, cidr := range []string{"0.0.0.0/0", "::/0"} {
		input.TrustedProxyCIDRs = []string{cidr}
		if err := ValidateEdgePolicy(input); err == nil {
			t.Fatal("trusted proxy accepted the entire Internet")
		}
	}
	for _, size := range []int{edgeMaximumList + 1, edgeMaximumCIDRs + 1} {
		input := edgeTestPolicy()
		input.Rules[0].AllowCIDRs = make([]string, size)
		for i := range input.Rules[0].AllowCIDRs {
			input.Rules[0].AllowCIDRs[i] = fmt.Sprintf("2001:db8::%x", i+1)
		}
		if err := ValidateEdgePolicy(input); err == nil {
			t.Fatal("unbounded network list accepted")
		}
	}
	policy = edgeTestPolicy()
	policy.Rules = nil
	for i := 0; i < edgeMaximumRules; i++ {
		rule := EdgeRule{ID: fmt.Sprintf("rule-%d", i), Host: fmt.Sprintf("%d.example.test", i)}
		for j := 0; j < 17; j++ {
			rule.AllowCIDRs = append(rule.AllowCIDRs, fmt.Sprintf("2001:db8::%x", j+1))
		}
		policy.Rules = append(policy.Rules, rule)
	}
	if err := ValidateEdgePolicy(policy); err == nil {
		t.Fatal("total CIDR bound not enforced across individual lists")
	}
}

func TestEdgeApplyIsAtomicOwnedAndResumable(t *testing.T) {
	c := edgeTestClient()
	ctx := context.Background()
	policy := edgeTestPolicy()
	acknowledged := false
	ackCalls := 0
	c.edgeAck = func(context.Context, EdgePolicy) error {
		ackCalls++
		if !acknowledged {
			return ErrEdgeNotAcknowledged
		}
		return nil
	}
	settings := map[string]string{"timeout-client": "31s", "max-content-length": "1024"}
	if _, err := c.ApplyProxyConfigurationWithEdge(ctx, settings, &policy, "100", 7); !errors.Is(err, ErrEdgeNotAcknowledged) {
		t.Fatal("ConfigMap acceptance was confused with an active reload", err)
	}
	current, err := c.proxyConfigMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Data["timeout-client"] != "31s" || current.Annotations[bodyLimitAnnotation] != "1024" || current.Annotations[edgePolicyAnnotation] == "" || current.Annotations["hakopod.io/proxy-revision"] != "7" {
		t.Fatal("combined controller and edge intent was not stored together")
	}
	for _, key := range []string{"global-config-snippet", "frontend-config-snippet", "backend-config-snippet"} {
		if !strings.HasPrefix(current.Data[key], "# operator ") || strings.Count(current.Data[key], edgeBlockStart) != 1 {
			t.Fatal("operator snippet was replaced or owned block duplicated")
		}
	}
	if current.Data["other-controller-setting"] != "preserved" || current.Annotations["operator.example/managed"] != "preserved" || !bodyLimitBlockMatches(current) {
		t.Fatal("unrelated controller state was lost")
	}
	if !strings.Contains(current.Data["global-config-snippet"], "\nexpose-experimental-directives\n") {
		t.Fatal("active path guards omitted the HAProxy 3.2 parser prerequisite")
	}
	// A failed observation can resume after the first mutation received a newer
	// Kubernetes version, without submitting that mutation again.
	current.ResourceVersion = "101"
	if _, err := c.kube.CoreV1().ConfigMaps(current.Namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	acknowledged = true
	kube := c.kube.(*fake.Clientset)
	kube.ClearActions()
	if version, err := c.ApplyProxyConfigurationWithEdge(ctx, settings, &policy, "100", 7); err != nil || version != "101" {
		t.Fatal("accepted mutation did not resume", version, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "update" {
			t.Fatal("runtime acknowledgement retry repeated the ConfigMap mutation")
		}
	}
	if ackCalls != 2 {
		t.Fatal("replay did not recheck active workers")
	}
	if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &policy, "100", 8); !errors.Is(err, ErrProxyConflict) {
		t.Fatal("stale reviewed version changed edge policy")
	}
	observed, err := c.ProxyConfiguration(ctx)
	normalized, _ := NormalizeEdgePolicy(policy)
	if err != nil || !reflect.DeepEqual(observed.Edge, normalized) {
		t.Fatal("stored edge policy was not observed", err)
	}
	disabled := normalized
	disabled.Enabled = false
	if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &disabled, "101", 8); err != nil {
		t.Fatal(err)
	}
	reset, err := c.proxyConfigMap(ctx)
	if err != nil || strings.Contains(reset.Data["frontend-config-snippet"], "http-request") || strings.Contains(reset.Data["frontend-config-snippet"], "stick-table") || strings.Contains(reset.Data["global-config-snippet"], "expose-experimental-directives") || !strings.Contains(reset.Data["backend-config-snippet"], "req.hdr(content-length)") {
		t.Fatal("disabling edge retained enforcement or removed another owned guard", err)
	}
}

func TestEdgePreservesOperatorExperimentalDirective(t *testing.T) {
	c := edgeTestClient()
	ctx := context.Background()
	cm, _ := c.proxyConfigMap(ctx)
	const operator = "# operator global setting\nexpose-experimental-directives\n"
	cm.Data["global-config-snippet"] = operator
	if _, err := c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	policy := edgeTestPolicy()
	for i, enabled := range []bool{true, false} {
		policy.Enabled = enabled
		if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &policy, cm.ResourceVersion, int64(i+1)); err != nil {
			t.Fatal(err)
		}
		var err error
		cm, err = c.proxyConfigMap(ctx)
		if err != nil {
			t.Fatal(err)
		}
		count := 1
		if enabled {
			count++
		}
		if !strings.HasPrefix(cm.Data["global-config-snippet"], operator) || strings.Count(cm.Data["global-config-snippet"], "expose-experimental-directives") != count {
			t.Fatal("edge changed the operator's existing experimental-directive setting")
		}
	}
}

func TestEdgeRejectsDriftAndOperatorConflicts(t *testing.T) {
	for _, key := range []string{"global-config-snippet", "frontend-config-snippet", "backend-config-snippet"} {
		t.Run(key, func(t *testing.T) {
			c := edgeTestClient()
			policy := edgeTestPolicy()
			ctx := context.Background()
			if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &policy, "100", 1); err != nil {
				t.Fatal(err)
			}
			cm, _ := c.proxyConfigMap(ctx)
			cm.Data[key] = strings.Replace(cm.Data[key], edgeBlockStart, "# edited edge block", 1)
			_, _ = c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
			before := cm.DeepCopy()
			if _, err := c.ProxyConfiguration(ctx); !errors.Is(err, ErrProxyConflict) {
				t.Fatal("external edge edit appeared configured", err)
			}
			if _, err := c.ApplyProxyConfiguration(ctx, map[string]string{"maxconn": "2048"}, "100", 2); !errors.Is(err, ErrProxyConflict) {
				t.Fatal("unrelated settings bypassed the ownership conflict", err)
			}
			after, _ := c.proxyConfigMap(ctx)
			if !maps.Equal(before.Data, after.Data) || !maps.Equal(before.Annotations, after.Annotations) {
				t.Fatal("conflicting operator state was overwritten")
			}
		})
	}
	for _, entry := range []struct{ key, value string }{
		{"src-ip-header", "X-Forwarded-For"}, {"proxy-protocol", "0.0.0.0/0"},
		{"frontend-config-snippet", "stick-table type ip size 100 expire 1s store http_req_rate(1s)"},
		{"frontend-config-snippet", "http-request track-sc2 src"},
		{"global-config-snippet", "tune.stick-counters 2"},
		{"cr-frontend-http", "operator-frontend"},
		{"cr-frontend-ssl", "operator-tls-frontend"},
		{"ssl-passthrough", "true"},
		{"ssl-passthrough", "invalid"},
		{"frontend-config-snippet", "http-request allow"},
		{"frontend-config-snippet", "http-request return status 200"},
		{"frontend-config-snippet", "http-request use-service lua.operator"},
		{"frontend-config-snippet", "http-request cache-use operator-cache"},
		{"frontend-config-snippet", "http-request lua.operator"},
		{"frontend-config-snippet", "http-request strict-mode off"},
		{"frontend-config-snippet", "http-request set-header Host public.example.test"},
		{"frontend-config-snippet", "http-request add-header HOST public.example.test"},
		{"frontend-config-snippet", "http-request del-header Host"},
		{"frontend-config-snippet", "http-request replace-header Host .* public.example.test"},
		{"frontend-config-snippet", "http-request replace-value Host .* public.example.test"},
		{"frontend-config-snippet", "http-request set-path /public"},
		{"frontend-config-snippet", "http-request set-pathq /public"},
		{"frontend-config-snippet", "http-request replace-path .* /public"},
		{"frontend-config-snippet", "http-request replace-pathq .* /public"},
		{"frontend-config-snippet", "http-request set-uri /public"},
		{"frontend-config-snippet", "http-request replace-uri .* /public"},
		{"frontend-config-snippet", "http-request normalize-uri path-merge-slashes"},
		{"frontend-config-snippet", "tcp-request connection set-src src"},
	} {
		c := edgeTestClient()
		ctx := context.Background()
		cm, _ := c.proxyConfigMap(ctx)
		cm.Data[entry.key] = entry.value
		_, _ = c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
		policy := edgeTestPolicy()
		if _, err := c.ApplyProxyConfigurationWithEdge(ctx, map[string]string{"maxconn": "2048"}, &policy, "100", 1); !errors.Is(err, ErrEdgeUnsupported) {
			t.Fatalf("accepted incompatible operator configuration %s: %v", entry.key, err)
		}
		after, _ := c.proxyConfigMap(ctx)
		if after.Data["maxconn"] != "" || after.Annotations[edgePolicyAnnotation] != "" {
			t.Fatal("rejected combined change partially mutated the ConfigMap")
		}
	}
}

func TestEdgeExplicitlyDisabledTLSPassthroughRemainsCompatible(t *testing.T) {
	c := edgeTestClient()
	ctx := context.Background()
	cm, _ := c.proxyConfigMap(ctx)
	cm.Data["ssl-passthrough"] = "false"
	if _, err := c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	policy := edgeTestPolicy()
	if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &policy, "100", 1); err != nil {
		t.Fatal("explicitly disabled TLS passthrough prevented protection", err)
	}
	current, _ := c.proxyConfigMap(ctx)
	current.Data["ssl-passthrough"] = "true"
	if _, err := c.kube.CoreV1().ConfigMaps(current.Namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{1, 2} {
		if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &policy, "100", revision); !errors.Is(err, ErrEdgeUnsupported) {
			t.Fatal("a replay or retained-policy edit acknowledged TLS passthrough", err)
		}
	}
	if _, err := c.ApplyProxyConfiguration(ctx, map[string]string{"maxconn": "2048"}, "100", 3); !errors.Is(err, ErrEdgeUnsupported) {
		t.Fatal("a controller-only edit concealed TLS passthrough", err)
	}
}

func TestEdgeGeneratedPreflightRejectsPolicyBypasses(t *testing.T) {
	awk, err := exec.LookPath("awk")
	if err != nil {
		t.Skip("requires awk, also present in the supported ingress container")
	}
	check := func(config string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, awk, "-v", "client_header=CF-Connecting-IP", "-v", "country_header=CF-IPCountry", edgeGeneratedCompatibilityAWK)
		command.Stdin = strings.NewReader(config)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatal("generated frontend inspection failed", err, string(output))
		}
		return strings.TrimSpace(string(output))
	}
	for _, action := range []string{
		"http-request allow", "http-request return status 200", "http-request use-service lua.operator", "http-request cache-use operator-cache", "http-request lua.operator", "http-request strict-mode off",
		"http-request set-header Host public.example.test", "http-request add-header HOST public.example.test", "http-request del-header Host", "http-request replace-header Host .* public.example.test", "http-request replace-value Host .* public.example.test",
		"http-request set-path /public", "http-request set-pathq /public", "http-request replace-path .* /public", "http-request replace-pathq .* /public", "http-request set-uri /public", "http-request replace-uri .* /public", "http-request normalize-uri path-strip-dotdot full",
		"http-request set-header CF-Connecting-IP 192.0.2.1", "http-request replace-value CF-IPCountry .* US", "http-request set-src src", "tcp-request connection expect-proxy layer4", "http-request track-sc2 src", "stick-table type ip size 100",
	} {
		for _, frontend := range []string{"http", "https"} {
			if got := check("global\nfrontend " + frontend + "\n  " + action + "\nbackend api\n"); got != "INCOMPATIBLE" {
				t.Fatalf("frontend %s accepted %s: %s", frontend, action, got)
			}
		}
	}
	if got := check("frontend http\n  bind :80\nfrontend ssl\n  mode tcp\n  bind :443\n"); got != "INCOMPATIBLE" {
		t.Fatal("TLS passthrough bypassed generated preflight", got)
	}
	for _, config := range []string{
		"frontend http\n  http-request set-var(txn.path) path\n  http-request redirect scheme https if { ssl_fc }\n",
		"frontend https\n  http-request set-header X-Request-ID %[unique-id]\nbackend api\n  http-request set-path /private\n",
		"frontend http\n" + edgeFrontendBlock(edgeTestPolicy()) + "\nfrontend https\n" + edgeFrontendBlock(edgeTestPolicy()) + "\n",
	} {
		if got := check(config); got != "COMPATIBLE" {
			t.Fatal("compatible controller or owned policy was rejected", got)
		}
	}
}

func TestEdgePrerequisitesFailBeforeMutation(t *testing.T) {
	c := edgeTestClient()
	c.edgeAck = nil
	policy := edgeTestPolicy()
	kube := c.kube.(*fake.Clientset)
	if _, err := c.ApplyProxyConfigurationWithEdge(context.Background(), map[string]string{"maxconn": "2048"}, &policy, "100", 1); !errors.Is(err, ErrEdgeUnsupported) {
		t.Fatal("missing runtime access was not rejected during preflight", err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "update" || action.GetVerb() == "patch" {
			t.Fatal("unsupported edge configuration was written before prerequisites were checked")
		}
	}
}

func TestControllerOnlyEditCannotHideInactiveEdgePolicy(t *testing.T) {
	c := edgeTestClient()
	ctx := context.Background()
	checks := 0
	c.edgeAck = func(context.Context, EdgePolicy) error {
		checks++
		return ErrEdgeNotAcknowledged
	}
	policy := edgeTestPolicy()
	if _, err := c.ApplyProxyConfigurationWithEdge(ctx, nil, &policy, "100", 1); !errors.Is(err, ErrEdgeNotAcknowledged) {
		t.Fatal(err)
	}
	if _, err := c.ApplyProxyConfiguration(ctx, map[string]string{"maxconn": "2048"}, "100", 2); !errors.Is(err, ErrEdgeNotAcknowledged) || checks != 2 {
		t.Fatal("a controller-only change concealed an inactive retained edge policy", err)
	}
}

func TestEdgeACMEExemptionRejectsTraversal(t *testing.T) {
	challenge := regexp.MustCompile(edgeACMETokenPattern)
	for _, path := range []string{"/.well-known/acme-challenge/token_123-abc", "/.well-known/acme-challenge/" + strings.Repeat("a", 43)} {
		if !challenge.MatchString(path) {
			t.Fatal("canonical HTTP-01 token was not exempted")
		}
	}
	for _, path := range []string{"/.well-known/acme-challenge/", "/.well-known/acme-challenge/../../private", "/.well-known/acme-challenge/%2e%2e/%2e%2e/private", "/.well-known/acme-challenge/token/../../../private", "/.well-known/acme-challenge/token%2f..", "/.well-known/acme-challenge/token\\..", "/.well-known/acme-challenge/" + strings.Repeat("a", 257)} {
		if challenge.MatchString(path) {
			t.Fatalf("non-token path bypassed edge policy: %s", path)
		}
	}
}

func TestEdgeCompiledACLsRespectHAProxyArgumentLimit(t *testing.T) {
	policy := EdgePolicy{Enabled: true, ClientIPSource: "trusted_proxy", TrustedProxyCIDRs: []string{"192.0.2.0/24"}, ClientIPHeader: "CF-Connecting-IP", CountryHeader: "CF-IPCountry"}
	for i := 0; i < edgeMaximumRules; i++ {
		policy.Rules = append(policy.Rules, EdgeRule{ID: fmt.Sprintf("rule-%d", i), Host: fmt.Sprintf("%d.example.test", i), AllowCountries: strings.Fields(edgeCountryCodes)[:64], RequestsPerSecond: 100000})
	}
	policy, err := NormalizeEdgePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(edgeFrontendBlock(policy), "\n") {
		if len(strings.Fields(line)) > 64 {
			t.Fatal("bounded public input exceeded HAProxy's 64-argument configuration limit")
		}
	}
}

func TestEdgeKubernetesWriteConflictIsNotRetriedAsAcknowledged(t *testing.T) {
	c := edgeTestClient()
	c.kube.(*fake.Clientset).PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "controller", errors.New("operator changed configuration"))
	})
	policy := edgeTestPolicy()
	if _, err := c.ApplyProxyConfigurationWithEdge(context.Background(), nil, &policy, "100", 1); !errors.Is(err, ErrProxyConflict) {
		t.Fatal("Kubernetes conflict was not exposed as a review conflict", err)
	}
}

func TestEdgeRuntimeAcknowledgesProcessAndEveryActiveListener(t *testing.T) {
	policy, _ := NormalizeEdgePolicy(edgeTestPolicy())
	reply := "REVISION\n" + edgeRuntimeVariable + ": type=str value=<" + edgePolicyHash(policy) + ">\n\nCONFIG\nFRONTEND http\n" + edgeFrontendBlock(policy) + "\nFRONTEND https\n" + edgeFrontendBlock(policy) + "\nACTIVE\nhttp\nhttps\nEND\n"
	state, err := parseEdgeRuntime(reply)
	if err != nil || validateEdgeRuntime(state, policy) != nil {
		t.Fatal("valid runtime acknowledgement rejected", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*edgeRuntime)
	}{
		{"old process", func(s *edgeRuntime) { s.revision = strings.Repeat("0", 64) }},
		{"old https rules", func(s *edgeRuntime) { s.frontends["https"] = edgeOwnedBlock("# old rules") }},
		{"missing https rules", func(s *edgeRuntime) { delete(s.frontends, "https") }},
		{"no active listeners", func(s *edgeRuntime) { s.active = nil }},
		{"duplicate listener", func(s *edgeRuntime) { s.active = []string{"http", "http"} }},
		{"unknown listener", func(s *edgeRuntime) { s.active = []string{"unreviewed"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := parseEdgeRuntime(reply)
			tc.edit(&state)
			if err := validateEdgeRuntime(state, policy); err == nil {
				t.Fatal("incomplete or stale runtime was acknowledged")
			}
		})
	}
	for _, invalid := range []string{strings.TrimSuffix(reply, "END\n"), strings.Replace(reply, "type=str", "type=int", 1), strings.Replace(reply, "FRONTEND https", "FRONTEND http", 1), "REVISION\nVariable not found.\nCONFIG\nACTIVE\nhttp\nEND\n"} {
		if _, err := parseEdgeRuntime(invalid); err == nil {
			t.Fatal("malformed runtime response accepted")
		}
	}
}
