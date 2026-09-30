package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientconfig "k8s.io/client-go/tools/clientcmd/api"
)

// This fixture serves synthetic ConfigMap observations and rejects every
// Kubernetes mutation. It cannot acknowledge a running HAProxy configuration.
type edgeAPIFixture struct {
	t         *testing.T
	db        *store.Store
	server    *Server
	handler   http.Handler
	token     string
	principal store.Principal
	mu        sync.Mutex
	config    corev1.ConfigMap
	writes    int
}

func newEdgeAPIFixture(t *testing.T) *edgeAPIFixture {
	t.Helper()
	f := &edgeAPIFixture{t: t, db: sourceDatabase(t)}
	var err error
	f.token, err = f.db.Bootstrap(context.Background(), "edge-api-fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.principal, err = f.db.Authenticate(context.Background(), f.token)
	if err != nil {
		t.Fatal(err)
	}
	f.config = corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "controller", Namespace: "ingress", ResourceVersion: "100",
			Labels: map[string]string{
				"app.kubernetes.io/instance": "hakopod",
				"app.kubernetes.io/name":     "kubernetes-ingress",
			},
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "hakopod",
				"meta.helm.sh/release-namespace": "ingress",
			},
		},
		Data: map[string]string{"timeout-client": "30s"},
	}
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != http.MethodGet {
			f.writes++
			http.Error(w, "synthetic fixture rejects Kubernetes mutations", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/api/v1/namespaces/ingress/configmaps/controller" {
			http.NotFound(w, r)
			return
		}
		write(w, http.StatusOK, f.config)
	}))
	t.Cleanup(kube.Close)
	file := filepath.Join(t.TempDir(), "kubeconfig")
	configuration := clientconfig.Config{
		Clusters:       map[string]*clientconfig.Cluster{"fixture": {Server: kube.URL}},
		Contexts:       map[string]*clientconfig.Context{"fixture": {Cluster: "fixture"}},
		CurrentContext: "fixture",
	}
	if err = clientcmd.WriteToFile(configuration, file); err != nil {
		t.Fatal(err)
	}
	c, err := cluster.New(file, cluster.Options{ProxyNamespace: "ingress", ProxyConfigMap: "controller", ProxyRelease: "hakopod"})
	if err != nil {
		t.Fatal(err)
	}
	f.server = &Server{Store: f.db, Cluster: c, Auth: AuthConfig{DeploymentMode: cluster.DeploymentSelfHosted}}
	f.handler = f.server.Handler()
	return f
}

func (f *edgeAPIFixture) request(method, token string, input any, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, "http://localhost/api/v1/settings/haproxy", bytes.NewReader(store.JSON(input)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, r)
	if out.Code != want {
		f.t.Fatalf("%s HAProxy settings returned %d, want %d: %s", method, out.Code, want, out.Body.String())
	}
	return out
}

func (f *edgeAPIFixture) change() (store.RuntimeResource, proxyChange) {
	f.t.Helper()
	row, err := f.db.RuntimeResource(context.Background(), "proxy", "", "", "haproxy")
	if err != nil {
		f.t.Fatal(err)
	}
	var change proxyChange
	if err = json.Unmarshal(row.Metadata, &change); err != nil {
		f.t.Fatal(err)
	}
	return row, change
}

func (f *edgeAPIFixture) noWrites() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 0 {
		f.t.Fatalf("attempted %d Kubernetes mutations without authorized reconciliation", f.writes)
	}
}

func (f *edgeAPIFixture) noSavedChange() {
	f.t.Helper()
	_, err := f.db.RuntimeResource(context.Background(), "proxy", "", "", "haproxy")
	if !errors.Is(err, pgx.ErrNoRows) {
		f.t.Fatalf("rejected policy created a durable change: %v", err)
	}
	var history, audits int
	err = f.db.Pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM runtime_resource_history WHERE kind='proxy'),
		(SELECT count(*) FROM audit_events WHERE action='proxy.configured')`).Scan(&history, &audits)
	if err != nil || history != 0 || audits != 0 {
		f.t.Fatalf("rejected policy created history/audit records: %d/%d, %v", history, audits, err)
	}
	f.noWrites()
}

func edgeAPIPolicy() cluster.EdgePolicy {
	return cluster.EdgePolicy{
		Enabled: true, ClientIPSource: "connection",
		TrustedProxyCIDRs: []string{},
		Rules: []cluster.EdgeRule{{
			ID: "api", Host: "api.example.test", PathPrefix: "/",
			AllowCIDRs:     []string{"198.51.100.0/24", "2001:db8::/32"},
			DenyCIDRs:      []string{"198.51.100.2/32"},
			AllowCountries: []string{}, DenyCountries: []string{}, RequestsPerSecond: 10,
		}},
	}
}

func edgeAPIInput(policy cluster.EdgePolicy) map[string]any {
	return map[string]any{"edge": policy, "expected_revision": 0, "expected_resource_version": "100"}
}

type edgeAPIResponse struct {
	Observed struct {
		Edge            cluster.EdgePolicy `json:"edge"`
		Settings        map[string]string  `json:"settings"`
		ResourceVersion string             `json:"resource_version"`
	} `json:"observed"`
	Revision int64       `json:"revision"`
	Change   proxyChange `json:"change"`
	Drift    bool        `json:"drift"`
}

func (f *edgeAPIFixture) observed() edgeAPIResponse {
	f.t.Helper()
	out := f.request(http.MethodGet, f.token, nil, http.StatusOK)
	if strings.Contains(out.Body.String(), f.token) || strings.Contains(out.Body.String(), f.principal.KeyID) || strings.Contains(out.Body.String(), `"key_id"`) {
		f.t.Fatal("settings response exposed the accepting credential")
	}
	var response edgeAPIResponse
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		f.t.Fatal(err)
	}
	return response
}

func TestEdgePolicyAcceptancePersistsIntentWithoutApplyingInfrastructure(t *testing.T) {
	f := newEdgeAPIFixture(t)
	initial := f.observed()
	if initial.Revision != 0 || initial.Observed.Edge.Enabled || initial.Change.Edge != nil || initial.Drift {
		t.Fatal("unconfigured edge returned an invented saved or active policy")
	}
	policy := edgeAPIPolicy()
	out := f.request(http.MethodPatch, f.token, edgeAPIInput(policy), http.StatusAccepted)
	var accepted struct {
		Revision int64  `json:"revision"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &accepted); err != nil || accepted.Revision != 1 || accepted.Status != "queued" {
		t.Fatalf("edge-only request was not queued: %s", out.Body.String())
	}
	row, change := f.change()
	if row.Revision != 1 || change.Status != "queued" || change.ResourceVersion != "100" || change.KeyID != f.principal.KeyID || len(change.Settings) != 0 || !reflect.DeepEqual(change.Edge, &policy) {
		t.Fatalf("accepted edge policy was not durably retained: %s", row.Metadata)
	}
	observed := f.observed()
	if observed.Revision != 1 || !reflect.DeepEqual(observed.Observed.Edge, initial.Observed.Edge) || observed.Change.Status != "queued" || !reflect.DeepEqual(observed.Change.Edge, &policy) || observed.Drift {
		t.Fatal("GET confused queued policy with observed infrastructure")
	}
	var history json.RawMessage
	var identity string
	if err := f.db.Pool.QueryRow(context.Background(), "SELECT identity_id,metadata FROM runtime_resource_history WHERE kind='proxy' AND revision=1").Scan(&identity, &history); err != nil || identity != f.principal.ID {
		t.Fatal("accepted policy has no attributed immutable history", err)
	}
	var historical proxyChange
	if err := json.Unmarshal(history, &historical); err != nil || !reflect.DeepEqual(historical.Edge, &policy) || historical.KeyID != f.principal.KeyID {
		t.Fatal("history lost the reviewed edge policy or its authority", err)
	}
	var audits int
	if err := f.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='proxy.configured' AND identity_id=$1 AND key_id=$2", f.principal.ID, f.principal.KeyID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("accepted edge policy needs exactly one attributed audit event", err)
	}
	f.request(http.MethodPatch, f.token, map[string]any{"edge": policy, "expected_revision": 1, "expected_resource_version": "100"}, http.StatusConflict)
	still, _ := f.change()
	if still.Revision != 1 {
		t.Fatal("a pending change was replaced")
	}
	f.noWrites()
}

func TestEdgePolicyRejectsInvalidInputBeforeAcceptance(t *testing.T) {
	f := newEdgeAPIFixture(t)
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *cluster.EdgePolicy)
	}{
		{"missing database revision", func(in map[string]any, _ *cluster.EdgePolicy) { delete(in, "expected_revision") }},
		{"missing resource version", func(in map[string]any, _ *cluster.EdgePolicy) { delete(in, "expected_resource_version") }},
		{"empty resource version", func(in map[string]any, _ *cluster.EdgePolicy) { in["expected_resource_version"] = "" }},
		{"no mutation", func(in map[string]any, _ *cluster.EdgePolicy) { delete(in, "edge") }},
		{"empty settings and null edge", func(in map[string]any, _ *cluster.EdgePolicy) { in["settings"] = map[string]string{}; in["edge"] = nil }},
		{"invalid IPv4 CIDR", func(_ map[string]any, p *cluster.EdgePolicy) { p.Rules[0].AllowCIDRs = []string{"198.51.100.0/33"} }},
		{"invalid IPv6 CIDR", func(_ map[string]any, p *cluster.EdgePolicy) { p.Rules[0].DenyCIDRs = []string{"2001:db8::/129"} }},
		{"country without trusted source", func(_ map[string]any, p *cluster.EdgePolicy) { p.Rules[0].DenyCountries = []string{"US"} }},
		{"country header with direct source", func(_ map[string]any, p *cluster.EdgePolicy) {
			p.CountryHeader = "CF-IPCountry"
			p.Rules[0].AllowCountries = []string{"US"}
		}},
		{"trusted source without peers", func(_ map[string]any, p *cluster.EdgePolicy) {
			p.ClientIPSource = "trusted_proxy"
			p.ClientIPHeader = "CF-Connecting-IP"
		}},
		{"invalid trusted peer", func(_ map[string]any, p *cluster.EdgePolicy) {
			p.ClientIPSource = "trusted_proxy"
			p.ClientIPHeader = "X-Real-IP"
			p.TrustedProxyCIDRs = []string{"not-a-network"}
		}},
		{"unbounded proxy trust", func(_ map[string]any, p *cluster.EdgePolicy) {
			p.ClientIPSource = "trusted_proxy"
			p.ClientIPHeader = "X-Real-IP"
			p.TrustedProxyCIDRs = []string{"0.0.0.0/0"}
		}},
		{"unsupported client header", func(_ map[string]any, p *cluster.EdgePolicy) {
			p.ClientIPSource = "trusted_proxy"
			p.ClientIPHeader = "X-Forwarded-For"
			p.TrustedProxyCIDRs = []string{"192.0.2.0/24"}
		}},
		{"negative rate", func(_ map[string]any, p *cluster.EdgePolicy) { p.Rules[0].RequestsPerSecond = -1 }},
		{"host directive injection", func(_ map[string]any, p *cluster.EdgePolicy) {
			p.Rules[0].Host = "api.example.test\nhttp-request allow"
		}},
		{"invalid companion setting", func(in map[string]any, _ *cluster.EdgePolicy) {
			in["settings"] = map[string]string{"timeout-client": "0s"}
		}},
		{"unknown input field", func(in map[string]any, _ *cluster.EdgePolicy) { in["frontend-config-snippet"] = "http-request allow" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := f.t
			f.t = t
			defer func() { f.t = parent }()
			policy := edgeAPIPolicy()
			in := edgeAPIInput(policy)
			in["edge"] = &policy
			tc.change(in, &policy)
			f.request(http.MethodPatch, f.token, in, http.StatusBadRequest)
			f.noSavedChange()
		})
	}
}

func TestEdgePolicyRequiresInstallationAdministrator(t *testing.T) {
	f := newEdgeAPIFixture(t)
	for _, permissions := range [][]string{{"deployments:read"}, {"deployments:write"}, {"admin"}} {
		_, token, err := f.db.CreateKey(context.Background(), f.principal, store.KeyInput{
			Name: strings.Join(permissions, ","), Project: "demo", Environment: "development",
			Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		f.request(http.MethodGet, token, nil, http.StatusForbidden)
		f.request(http.MethodPatch, token, edgeAPIInput(edgeAPIPolicy()), http.StatusForbidden)
	}
	f.request(http.MethodPatch, "", edgeAPIInput(edgeAPIPolicy()), http.StatusUnauthorized)
	f.server.Auth.DeploymentMode = cluster.DeploymentManagedCloud
	f.request(http.MethodGet, f.token, nil, http.StatusForbidden)
	f.request(http.MethodPatch, f.token, edgeAPIInput(edgeAPIPolicy()), http.StatusForbidden)
	f.noSavedChange()
}

func TestEdgePolicyAndSettingsShareNormalizedAtomicRevision(t *testing.T) {
	f := newEdgeAPIFixture(t)
	policy := edgeAPIPolicy()
	policy.ClientIPSource = "trusted_proxy"
	policy.ClientIPHeader = "CF-Connecting-IP"
	policy.CountryHeader = "CF-IPCountry"
	policy.TrustedProxyCIDRs = []string{"192.0.2.42/24"}
	policy.Rules[0].Host = "API.Example.Test"
	policy.Rules[0].AllowCIDRs = []string{"198.51.100.42/24", "2001:db8::1234/32"}
	policy.Rules[0].AllowCountries = []string{"us"}
	settings := map[string]string{"timeout-client": "45s", "load-balance": "leastconn"}
	in := edgeAPIInput(policy)
	in["settings"] = settings
	f.request(http.MethodPatch, f.token, in, http.StatusAccepted)
	want := edgeAPIPolicy()
	want.ClientIPSource, want.ClientIPHeader, want.CountryHeader = "trusted_proxy", "CF-Connecting-IP", "CF-IPCountry"
	want.TrustedProxyCIDRs = []string{"192.0.2.0/24"}
	want.Rules[0].AllowCountries = []string{"US"}
	row, change := f.change()
	if row.Revision != 1 || !reflect.DeepEqual(change.Settings, settings) || !reflect.DeepEqual(change.Edge, &want) {
		t.Fatalf("settings and normalized edge policy did not share one revision: %s", row.Metadata)
	}
	var historyCount int
	var history json.RawMessage
	if err := f.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM runtime_resource_history WHERE kind='proxy'").Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatal("combined update did not produce exactly one history revision", err)
	}
	if err := f.db.Pool.QueryRow(context.Background(), "SELECT metadata FROM runtime_resource_history WHERE kind='proxy' AND revision=1").Scan(&history); err != nil {
		t.Fatal(err)
	}
	var saved proxyChange
	if err := json.Unmarshal(history, &saved); err != nil || !reflect.DeepEqual(saved.Edge, change.Edge) || !reflect.DeepEqual(saved.Settings, settings) {
		t.Fatal("combined history lost part of the accepted intent", err)
	}
	f.noWrites()
}

func TestEdgePolicyReviewConflictsDoNotAdvanceRevision(t *testing.T) {
	f := newEdgeAPIFixture(t)
	policy := edgeAPIPolicy()
	in := edgeAPIInput(policy)
	in["expected_resource_version"] = "99"
	f.request(http.MethodPatch, f.token, in, http.StatusConflict)
	f.noSavedChange()
	in["expected_resource_version"] = "100"
	in["expected_revision"] = 1
	f.request(http.MethodPatch, f.token, in, http.StatusConflict)
	f.noSavedChange()
	in["expected_revision"] = 0
	f.request(http.MethodPatch, f.token, in, http.StatusAccepted)
	// An explicit failed fixture frees the queue without claiming runtime success.
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE runtime_resources SET metadata=jsonb_set(metadata,'{status}','"failed"') WHERE kind='proxy'`); err != nil {
		t.Fatal(err)
	}
	f.request(http.MethodPatch, f.token, in, http.StatusConflict)
	row, _ := f.change()
	if row.Revision != 1 {
		t.Fatal("stale reviewed revision replaced accepted intent")
	}
	f.mu.Lock()
	f.config.ResourceVersion = "101"
	f.mu.Unlock()
	in["expected_revision"] = 1
	f.request(http.MethodPatch, f.token, in, http.StatusConflict)
	in["expected_resource_version"] = "101"
	f.request(http.MethodPatch, f.token, in, http.StatusAccepted)
	row, change := f.change()
	if row.Revision != 2 || change.ResourceVersion != "101" || change.Status != "queued" {
		t.Fatal("refreshed review did not accept the next durable revision")
	}
	f.noWrites()
}

func TestEdgePolicyWorkerRechecksAuthorityBeforeKubernetes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(*edgeAPIFixture) error
	}{
		{"revoked key", func(f *edgeAPIFixture) error {
			_, err := f.db.Pool.Exec(context.Background(), "UPDATE api_keys SET revoked_at=now() WHERE id=$1", f.principal.KeyID)
			return err
		}},
		{"removed administrator role", func(f *edgeAPIFixture) error {
			_, err := f.db.Pool.Exec(context.Background(), "UPDATE identities SET admin=false WHERE id=$1", f.principal.ID)
			return err
		}},
		{"installation becomes managed cloud", func(f *edgeAPIFixture) error {
			f.server.Auth.DeploymentMode = cluster.DeploymentManagedCloud
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEdgeAPIFixture(t)
			f.request(http.MethodPatch, f.token, edgeAPIInput(edgeAPIPolicy()), http.StatusAccepted)
			if err := tc.revoke(f); err != nil {
				t.Fatal(err)
			}
			f.server.reconcileProxy(context.Background())
			row, change := f.change()
			if row.Revision != 1 || change.Status != "failed" || change.Error == "" || change.Attempts != 1 {
				t.Fatalf("queued edge change retained revoked authority: %s", row.Metadata)
			}
			f.noWrites()
		})
	}
}

func TestEdgePolicyGetReportsDriftWithoutInventingRuntimeSuccess(t *testing.T) {
	f := newEdgeAPIFixture(t)
	initial := f.observed()
	// Seed an applied database record solely to exercise GET's comparison. No
	// worker or Kubernetes fixture acknowledges this synthetic applied state.
	change := proxyChange{
		Settings: map[string]string{"timeout-client": "30s"}, Edge: &initial.Observed.Edge,
		ResourceVersion: "100", KeyID: f.principal.KeyID, Status: "applied",
	}
	if _, err := f.db.PutRuntimeResource(context.Background(), f.principal, "proxy", "", "", "haproxy", 0, change); err != nil {
		t.Fatal(err)
	}
	if observed := f.observed(); observed.Drift || observed.Revision != 1 || !reflect.DeepEqual(observed.Change.Edge, &initial.Observed.Edge) {
		t.Fatal("matching observed and saved policies were reported as drifted")
	}
	policy := edgeAPIPolicy()
	change.Edge = &policy
	if _, err := f.db.Pool.Exec(context.Background(), "UPDATE runtime_resources SET metadata=$1 WHERE kind='proxy'", store.JSON(change)); err != nil {
		t.Fatal(err)
	}
	observed := f.observed()
	if !observed.Drift || observed.Observed.Edge.Enabled || !reflect.DeepEqual(observed.Change.Edge, &policy) {
		t.Fatal("GET concealed a saved edge policy that differs from infrastructure")
	}
	// Queued intent is presented separately until the worker completes it.
	change.Status = "queued"
	if _, err := f.db.Pool.Exec(context.Background(), "UPDATE runtime_resources SET metadata=$1 WHERE kind='proxy'", store.JSON(change)); err != nil {
		t.Fatal(err)
	}
	if observed := f.observed(); observed.Drift || observed.Change.Status != "queued" || observed.Observed.Edge.Enabled {
		t.Fatal("queued intent was presented as active infrastructure drift")
	}
	f.noWrites()
}
