package cluster

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"go.yaml.in/yaml/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const maxSupabaseRuntimeObjects = managedplatform.MaxComponents * 5

var errSupabasePrunePending = errors.New("Supabase snapshot deletion is still in progress")

type ManagedPlatformOperationStore interface {
	CheckManagedPlatformOperation(context.Context, store.ManagedPlatformOperation) error
	HeartbeatManagedPlatformOperation(context.Context, store.ManagedPlatformOperation) error
	ClaimPlatformResource(context.Context, store.ManagedPlatformOperation, store.PlatformResourceClaim) error
	ReservePlatformResourceIntent(context.Context, store.ManagedPlatformOperation, store.PlatformResourceIntent) (store.PlatformResourceIntent, error)
	PlatformResourceIntents(context.Context, store.ManagedPlatformOperation, int64) ([]store.PlatformResourceIntent, error)
	ConfirmPlatformResourceIntent(context.Context, store.ManagedPlatformOperation, store.PlatformResourceIntent, store.PlatformResourceClaim) error
	CancelPlatformResourceIntent(context.Context, store.ManagedPlatformOperation, store.PlatformResourceIntent) error
	AdvancePlatformResourceClaim(context.Context, store.ManagedPlatformOperation, store.PlatformResourceClaim) error
	VerifyPlatformResourceClaim(context.Context, store.ManagedPlatformOperation, store.PlatformResourceClaim) error
	ReleasePlatformResourceClaim(context.Context, store.ManagedPlatformOperation, store.PlatformResourceClaim) error
	PlatformResourceClaims(context.Context, store.ManagedPlatformOperation, int64) ([]store.PlatformResourceClaim, error)
	RecordManagedPlatformStep(context.Context, store.ManagedPlatformOperation, string, string, string, map[string]any) error
}

type SupabaseRuntimeRequest struct {
	Operation       store.ManagedPlatformOperation
	Render          managedplatform.SupabaseRenderInput
	SecretSnapshots map[string]map[string][]byte
}

type SupabaseRuntimeObservation struct {
	Status             string   `json:"status"`
	Revision           int64    `json:"revision"`
	NamespaceUID       string   `json:"namespace_uid,omitempty"`
	ReadyComponents    int      `json:"ready_components"`
	ExpectedComponents int      `json:"expected_components"`
	Pending            []string `json:"pending,omitempty"`
}

// ReconcileSupabaseOperation performs one bounded durable operation attempt.
// It never waits for rollout. Not-ready state is requeued through PostgreSQL.
func (c *Client) ReconcileSupabaseOperation(ctx context.Context, state ManagedPlatformOperationStore, request SupabaseRuntimeRequest) error {
	op := request.Operation
	if op.ID == "" || op.Lease == "" || op.PlatformID == "" || op.Revision < 1 || request.Render.PlatformID != op.PlatformID || request.Render.Revision != op.Revision || request.Render.Spec.Kind != "supabase" {
		return fmt.Errorf("invalid Supabase operation contract")
	}
	before := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return state.HeartbeatManagedPlatformOperation(ctx, op)
	}
	if op.Kind == "delete" {
		return c.deleteSupabaseOperation(ctx, state, op, before)
	}
	if op.Kind != "create" && op.Kind != "update" {
		return fmt.Errorf("unsupported Supabase operation kind")
	}
	prior, current, err := loadSupabaseClaims(ctx, state, op)
	if err != nil {
		return err
	}
	ns, err := c.ensureSupabaseNamespace(ctx, state, op, prior, current, before)
	if err != nil {
		return err
	}
	request.Render.NamespaceUID = ns.UID
	claim, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, "supabase-database", metav1.GetOptions{})
	request.Render.DatabaseClaim = managedplatform.ObservedClaimState{Observed: true}
	if err == nil {
		request.Render.DatabaseClaim.UID = claim.UID
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	manifests, err := managedplatform.RenderSupabase(request.Render)
	if err != nil {
		return err
	}
	tlsName := secretSnapshotNameForCluster(request.Render.Spec.Secrets["gateway-tls-certificate"])
	runtimeName := secretSnapshotNameForCluster(request.Render.Spec.Secrets["envoy-runtime-config"])
	if err = validateSupabaseTLSSecretSnapshots(request.SecretSnapshots, manifests.RequiredSecrets, tlsName, runtimeName, request.Render.Spec.Supabase.PublicURL); err != nil {
		return err
	}
	databaseTLSName := secretSnapshotNameForCluster(request.Render.Spec.Secrets["database-tls-certificate"])
	if err = validateSupabaseDatabaseTLS(request.SecretSnapshots[databaseTLSName], ns.Name); err != nil {
		return err
	}
	if err = validateSupabaseDatabaseClientURLs(request.SecretSnapshots, request.Render.Spec); err != nil {
		return err
	}
	for _, name := range manifests.RequiredSecrets {
		if err = c.applySupabaseSecret(ctx, state, op, ns, name, request.SecretSnapshots[name], prior, current, before); err != nil {
			return err
		}
	}
	objects := append([]runtime.Object(nil), manifests.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return supabaseApplyRank(objects[i]) < supabaseApplyRank(objects[j]) })
	for _, object := range objects {
		if err = c.applySupabaseObject(ctx, op, ns, object, state, prior, current, before); err != nil {
			return err
		}
	}
	observation, err := c.ObserveSupabase(ctx, op, manifests, current)
	if err != nil {
		return err
	}
	if observation.Status != "ready" {
		return state.RecordManagedPlatformStep(ctx, op, "queued", "waiting-ready", "Supabase components are not ready.", supabaseObservationMap(observation))
	}
	if err = c.pruneSupabaseSnapshots(ctx, state, op, ns, manifests, prior, current, before); errors.Is(err, errSupabasePrunePending) {
		return state.RecordManagedPlatformStep(ctx, op, "queued", "waiting-prune", "Old Supabase snapshots are still being deleted.", supabaseObservationMap(observation))
	} else if err != nil {
		return err
	}
	return state.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "Supabase runtime is ready in the cluster.", supabaseObservationMap(observation))
}

func (c *Client) ensureSupabaseNamespace(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, prior, current map[string]store.PlatformResourceClaim, before func() error) (*corev1.Namespace, error) {
	name := "managed-platform-" + op.PlatformID
	api := c.kube.CoreV1().Namespaces()
	ns, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return nil, err
		}
		desired := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": op.PlatformID, "hakopod.io/owner-operation-id": op.ID}}}
		if _, err = reserveSupabaseCreate(ctx, state, op, "namespace", desired); err != nil {
			return nil, err
		}
		ns, createErr := api.Create(ctx, desired, metav1.CreateOptions{})
		if createErr != nil {
			return nil, createErr
		}
		if claimErr := claimOrAdvanceSupabaseObject(ctx, state, op, "namespace", ns, prior, current, true); claimErr != nil {
			return nil, claimErr
		}
		return ns, nil
	}
	if err != nil {
		return nil, err
	}
	if ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" || ns.Labels["hakopod.io/managed-platform-id"] != op.PlatformID {
		return nil, fmt.Errorf("Supabase namespace ownership changed")
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, "namespace", ns, prior, current, false); err != nil {
		return nil, err
	}
	return ns, nil
}

func validateSupabaseSecretSnapshot(values map[string]map[string][]byte, names []string) error {
	if len(values) != len(names) {
		return fmt.Errorf("resolved Supabase secret snapshot is incomplete")
	}
	for _, name := range names {
		data, ok := values[name]
		if !ok || len(data) == 0 || len(data) > 16 {
			return fmt.Errorf("resolved Supabase secret %s is unavailable", name)
		}
		total := 0
		for key, value := range data {
			if key == "" || len(value) == 0 {
				return fmt.Errorf("resolved Supabase secret %s is invalid", name)
			}
			total += len(key) + len(value)
		}
		if total > 65536 {
			return fmt.Errorf("resolved Supabase secret %s exceeds 64 KiB", name)
		}
	}
	return nil
}

func validateSupabaseTLSSecretSnapshots(values map[string]map[string][]byte, names []string, tlsName, runtimeName, publicURL string) error {
	if err := validateSupabaseSecretSnapshot(values, names); err != nil {
		return err
	}
	if tlsName == "" || runtimeName == "" || publicURL == "" {
		return fmt.Errorf("Supabase gateway TLS validation parameters are incomplete")
	}
	if err := validateSupabaseGatewayTLS(values[tlsName], publicURL); err != nil {
		return err
	}
	return validateSupabaseGatewayRuntimeSecret(values[runtimeName])
}

type supabaseGatewayLDS struct {
	Resources []struct {
		Type    string `yaml:"@type"`
		Name    string `yaml:"name"`
		Address struct {
			SocketAddress struct {
				Address   string `yaml:"address"`
				PortValue int32  `yaml:"port_value"`
				Protocol  string `yaml:"protocol"`
			} `yaml:"socket_address"`
		} `yaml:"address"`
		AdditionalAddresses []yaml.Node `yaml:"additional_addresses"`
		DefaultFilterChain  *yaml.Node  `yaml:"default_filter_chain"`
		FilterChains        []struct {
			Filters []struct {
				Name        string `yaml:"name"`
				TypedConfig struct {
					Type string `yaml:"@type"`
				} `yaml:"typed_config"`
			} `yaml:"filters"`
			TransportSocket struct {
				Name        string `yaml:"name"`
				TypedConfig struct {
					Type             string `yaml:"@type"`
					CommonTLSContext struct {
						TLSParams struct {
							MinimumProtocolVersion string `yaml:"tls_minimum_protocol_version"`
						} `yaml:"tls_params"`
						TLSCertificates []struct {
							CertificateChain struct {
								Filename string `yaml:"filename"`
							} `yaml:"certificate_chain"`
							PrivateKey struct {
								Filename string `yaml:"filename"`
							} `yaml:"private_key"`
						} `yaml:"tls_certificates"`
					} `yaml:"common_tls_context"`
				} `yaml:"typed_config"`
			} `yaml:"transport_socket"`
		} `yaml:"filter_chains"`
	} `yaml:"resources"`
}

func rejectSupabaseYAMLAliasesAndDuplicates(node *yaml.Node) error {
	if node == nil {
		return fmt.Errorf("Supabase gateway runtime config is malformed")
	}
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("Supabase gateway runtime config must not contain YAML aliases")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Value == "<<" || seen[key.Value] {
				return fmt.Errorf("Supabase gateway runtime config contains a duplicate or merged field")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := rejectSupabaseYAMLAliasesAndDuplicates(child); err != nil {
			return err
		}
	}
	return nil
}

func validateSupabaseGatewayRuntimeSecret(data map[string][]byte) error {
	if len(data) != 1 || len(data["value"]) == 0 {
		return fmt.Errorf("Supabase gateway runtime snapshot must contain only value")
	}
	return validateSupabaseGatewayLDS(data["value"])
}

func validateSupabaseGatewayLDS(value []byte) error {
	if len(value) == 0 || len(value) > 64<<10 {
		return fmt.Errorf("Supabase gateway runtime config is empty or exceeds 64 KiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(value))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 {
		return fmt.Errorf("Supabase gateway runtime config is malformed")
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("Supabase gateway runtime config must contain exactly one YAML document")
	}
	if err := rejectSupabaseYAMLAliasesAndDuplicates(&document); err != nil {
		return err
	}
	var config supabaseGatewayLDS
	if err := document.Decode(&config); err != nil || len(config.Resources) != 1 {
		return fmt.Errorf("Supabase gateway runtime config must contain exactly one listener")
	}
	listener := config.Resources[0]
	if listener.Type != "type.googleapis.com/envoy.config.listener.v3.Listener" || listener.Name != "supabase" || listener.Address.SocketAddress.Address != "0.0.0.0" || listener.Address.SocketAddress.PortValue != 8443 || listener.Address.SocketAddress.Protocol != "" && listener.Address.SocketAddress.Protocol != "TCP" || len(listener.AdditionalAddresses) != 0 || listener.DefaultFilterChain != nil || len(listener.FilterChains) != 1 {
		return fmt.Errorf("Supabase gateway runtime config must expose only the named TLS listener on port 8443")
	}
	tlsConfig := listener.FilterChains[0].TransportSocket
	if tlsConfig.Name != "envoy.transport_sockets.tls" || tlsConfig.TypedConfig.Type != "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext" || tlsConfig.TypedConfig.CommonTLSContext.TLSParams.MinimumProtocolVersion != "TLSv1_2" || len(tlsConfig.TypedConfig.CommonTLSContext.TLSCertificates) != 1 {
		return fmt.Errorf("Supabase gateway runtime config is missing its TLS 1.2-or-newer transport socket")
	}
	if len(listener.FilterChains[0].Filters) != 1 || listener.FilterChains[0].Filters[0].Name != "envoy.filters.network.http_connection_manager" || listener.FilterChains[0].Filters[0].TypedConfig.Type != "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager" {
		return fmt.Errorf("Supabase gateway runtime config is missing its HTTP connection manager")
	}
	certificate := tlsConfig.TypedConfig.CommonTLSContext.TLSCertificates[0]
	if certificate.CertificateChain.Filename != "/etc/envoy/tls/tls.crt" || certificate.PrivateKey.Filename != "/etc/envoy/tls/tls.key" {
		return fmt.Errorf("Supabase gateway runtime config uses an unowned TLS keypair path")
	}
	return nil
}

func secretSnapshotNameForCluster(ref managedplatform.SecretReference) string {
	return ref.Name + "-r" + strconv.FormatInt(ref.Revision, 10)
}

func validateSupabaseGatewayTLS(data map[string][]byte, publicURL string) error {
	if len(data) != 3 {
		return fmt.Errorf("Supabase gateway TLS snapshot must contain only tls.crt, tls.key and ca.crt")
	}
	certificate, key, caPEM := data[corev1.TLSCertKey], data[corev1.TLSPrivateKeyKey], data["ca.crt"]
	if len(certificate) == 0 || len(certificate) > 48<<10 || len(key) == 0 || len(key) > 16<<10 || len(caPEM) == 0 || len(caPEM) > 48<<10 {
		return fmt.Errorf("Supabase gateway TLS snapshot requires bounded tls.crt, tls.key and ca.crt")
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil || len(pair.Certificate) == 0 {
		return fmt.Errorf("Supabase gateway TLS certificate and key are invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("Supabase gateway TLS leaf is invalid")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("Supabase gateway public hostname is invalid")
	}
	for _, hostname := range []string{u.Hostname(), "api-gw"} {
		if err = leaf.VerifyHostname(hostname); err != nil {
			return fmt.Errorf("Supabase gateway TLS certificate does not cover required hostname %s", hostname)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("Supabase gateway TLS trust bundle is invalid")
	}
	intermediates := x509.NewCertPool()
	for rest := certificate; len(rest) > 0; {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		cert, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr == nil && !cert.Equal(leaf) {
			intermediates.AddCert(cert)
		}
	}
	for _, hostname := range []string{u.Hostname(), "api-gw"} {
		if _, err = leaf.Verify(x509.VerifyOptions{DNSName: hostname, Roots: roots, Intermediates: intermediates, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return fmt.Errorf("Supabase gateway TLS certificate is not trusted for required hostname %s", hostname)
		}
	}
	return nil
}

func validateSupabaseDatabaseTLS(data map[string][]byte, namespace string) error {
	if len(data) != 3 {
		return fmt.Errorf("Supabase database TLS snapshot must contain only tls.crt, tls.key and ca.crt")
	}
	certificate, key, caPEM := data[corev1.TLSCertKey], data[corev1.TLSPrivateKeyKey], data["ca.crt"]
	if len(certificate) == 0 || len(certificate) > 48<<10 || len(key) == 0 || len(key) > 16<<10 || len(caPEM) == 0 || len(caPEM) > 48<<10 {
		return fmt.Errorf("Supabase database TLS snapshot requires bounded tls.crt, tls.key and ca.crt")
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil || len(pair.Certificate) != 1 {
		return fmt.Errorf("Supabase database TLS certificate and key are invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || time.Until(leaf.NotAfter) < 24*time.Hour || time.Until(leaf.NotAfter) > 398*24*time.Hour {
		return fmt.Errorf("Supabase database TLS leaf identity or validity is invalid")
	}
	caBlock, rest := pem.Decode(caPEM)
	if caBlock == nil || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("Supabase database TLS trust bundle must contain exactly one CA")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.Equal(leaf) || ca.CheckSignatureFrom(ca) != nil {
		return fmt.Errorf("Supabase database TLS trust anchor is invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, hostname := range []string{"db", "db." + namespace + ".svc"} {
		if _, err = leaf.Verify(x509.VerifyOptions{DNSName: hostname, Roots: roots, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return fmt.Errorf("Supabase database TLS certificate is not trusted for required hostname %s", hostname)
		}
	}
	return nil
}

func validateSupabaseDatabaseClientURLs(values map[string]map[string][]byte, spec managedplatform.Spec) error {
	expectedRoles := map[string]string{
		"auth-database-url":    "supabase_auth_admin",
		"rest-database-url":    "authenticator",
		"storage-database-url": "supabase_storage_admin",
	}
	for key, expectedRole := range expectedRoles {
		ref := spec.Secrets[key]
		data := values[secretSnapshotNameForCluster(ref)]
		u, err := url.Parse(string(data["value"]))
		if err != nil || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() != "db" || u.Port() != "5432" || u.User == nil || u.User.Username() != expectedRole || u.Path != "/"+spec.Supabase.DatabaseName {
			return fmt.Errorf("Supabase %s must use its expected database role, database, and TLS hostname", key)
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query) != 2 || len(query["sslmode"]) != 1 || query["sslmode"][0] != "verify-full" || len(query["sslrootcert"]) != 1 || query["sslrootcert"][0] != "/etc/hakopod-database-ca/ca.crt" {
			return fmt.Errorf("Supabase %s must use database hostname verification and the mounted CA without connection overrides", key)
		}
	}
	return nil
}

func supabaseLabels(op store.ManagedPlatformOperation) map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": op.PlatformID, "hakopod.io/revision": strconv.FormatInt(op.Revision, 10), "hakopod.io/owner-operation-id": op.ID}
}
func supabaseNamespaceOwner(ns *corev1.Namespace) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}
}

func (c *Client) applySupabaseSecret(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, name string, data map[string][]byte, priorClaims, currentClaims map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.CoreV1().Secrets(ns.Name)
	secret, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, exists := priorClaims[supabaseClaimKey("secret", name)]; exists {
			return fmt.Errorf("claimed Supabase secret %s disappeared", name)
		}
		if _, exists := currentClaims[supabaseClaimKey("secret", name)]; exists {
			return fmt.Errorf("claimed Supabase secret %s disappeared", name)
		}
		desired := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name, Labels: supabaseLabels(op), OwnerReferences: []metav1.OwnerReference{supabaseNamespaceOwner(ns)}}, Immutable: boolPointer(true), Type: corev1.SecretTypeOpaque, Data: copySecretData(data)}
		if _, err = reserveSupabaseCreate(ctx, state, op, "secret", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, err := api.Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "secret", created, priorClaims, currentClaims, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(secret, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if secret.Immutable == nil || !*secret.Immutable || !equalSecretData(secret.Data, data) {
		return fmt.Errorf("immutable Supabase secret snapshot %s changed", name)
	}
	return claimOrAdvanceSupabaseObject(ctx, state, op, "secret", secret, priorClaims, currentClaims, false)
}

func copySecretData(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for key, value := range in {
		out[key] = append([]byte(nil), value...)
	}
	return out
}
func equalSecretData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || subtle.ConstantTimeCompare(value, other) != 1 {
			return false
		}
	}
	return true
}

func loadSupabaseClaims(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation) (map[string]store.PlatformResourceClaim, map[string]store.PlatformResourceClaim, error) {
	prior := map[string]store.PlatformResourceClaim{}
	current := map[string]store.PlatformResourceClaim{}
	if op.Revision > 1 {
		claims, err := state.PlatformResourceClaims(ctx, op, op.Revision-1)
		if err != nil {
			return nil, nil, err
		}
		if len(claims) > maxSupabaseRuntimeObjects {
			return nil, nil, fmt.Errorf("managed platform prior resource claim bound exceeded")
		}
		for _, claim := range claims {
			if claim.PlatformID != op.PlatformID || claim.PlatformRevision != op.Revision-1 || claim.ReleasedAt != nil {
				return nil, nil, fmt.Errorf("invalid managed platform prior resource claim")
			}
			if op.Spec.Kind == "neon" && neonLifecycleClaimComponent(claim.Component) {
				if !validNeonLifecycleClaimKind(claim.Component, claim.Kind) {
					return nil, nil, fmt.Errorf("invalid managed platform prior Neon lifecycle claim")
				}
				if op.Kind != "delete" {
					continue
				}
			} else if claim.Kind != "runtime_component" {
				return nil, nil, fmt.Errorf("invalid managed platform prior resource claim")
			}
			if _, exists := prior[claim.Component]; exists {
				return nil, nil, fmt.Errorf("duplicate managed platform prior resource claim")
			}
			prior[claim.Component] = claim
		}
	}
	claims, err := state.PlatformResourceClaims(ctx, op, op.Revision)
	if err != nil {
		return nil, nil, err
	}
	if len(claims) > maxSupabaseRuntimeObjects {
		return nil, nil, fmt.Errorf("managed platform current resource claim bound exceeded")
	}
	for _, claim := range claims {
		if claim.PlatformID != op.PlatformID || claim.PlatformRevision != op.Revision || claim.OwnerOperationID != op.ID || claim.ReleasedAt != nil {
			return nil, nil, fmt.Errorf("invalid managed platform current resource claim")
		}
		if op.Spec.Kind == "neon" && neonLifecycleClaimComponent(claim.Component) {
			if !validNeonLifecycleClaimKind(claim.Component, claim.Kind) {
				return nil, nil, fmt.Errorf("invalid managed platform current Neon lifecycle claim")
			}
			if op.Kind != "delete" {
				continue
			}
		} else if claim.Kind != "runtime_component" {
			return nil, nil, fmt.Errorf("invalid managed platform current resource claim")
		}
		if _, exists := current[claim.Component]; exists {
			return nil, nil, fmt.Errorf("duplicate managed platform current resource claim")
		}
		current[claim.Component] = claim
	}
	if len(prior)+len(current) > maxSupabaseRuntimeObjects {
		return nil, nil, fmt.Errorf("managed platform resource claim bound exceeded")
	}
	return prior, current, nil
}

func supabaseClaimKey(kind, name string) string { return kind + "." + name }

func supabaseExternalKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

func reserveSupabaseCreate(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, kind string, object metav1.Object) (store.PlatformResourceIntent, error) {
	key := supabaseClaimKey(kind, object.GetName())
	external := supabaseExternalKey(kind, object.GetNamespace(), object.GetName())
	intents, err := state.PlatformResourceIntents(ctx, op, op.Revision)
	if err != nil {
		return store.PlatformResourceIntent{}, err
	}
	if len(intents) > maxSupabaseRuntimeObjects {
		return store.PlatformResourceIntent{}, fmt.Errorf("Supabase resource intent bound exceeded")
	}
	var intent store.PlatformResourceIntent
	for _, candidate := range intents {
		if candidate.Component == key {
			if candidate.Kind != "runtime_component" || candidate.ExternalKey != external || candidate.OwnerOperationID != op.ID || candidate.ConfirmedAt != nil {
				return intent, fmt.Errorf("Supabase resource intent changed for %s", key)
			}
			intent = candidate
			break
		}
	}
	if intent.ID == "" {
		intent, err = state.ReservePlatformResourceIntent(ctx, op, store.PlatformResourceIntent{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: key, Kind: "runtime_component", ExternalKey: external, OwnerOperationID: op.ID})
		if err != nil {
			return intent, err
		}
	}
	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["hakopod.io/resource-intent-id"] = intent.ID
	object.SetLabels(labels)
	return intent, nil
}

func claimOrAdvanceSupabaseObject(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, kind string, object metav1.Object, priorClaims, currentClaims map[string]store.PlatformResourceClaim, allowUnclaimed bool) error {
	key := supabaseClaimKey(kind, object.GetName())
	if len(key) > 63 || object.GetUID() == "" {
		return fmt.Errorf("invalid Supabase runtime object identity")
	}
	if claim, ok := currentClaims[key]; ok {
		if claim.ResourceID != string(object.GetUID()) || claim.ImmutableGeneration != 1 {
			return fmt.Errorf("Supabase resource claim identity changed for %s", key)
		}
		return state.VerifyPlatformResourceClaim(ctx, op, claim)
	}
	if claim, ok := priorClaims[key]; ok {
		if claim.ResourceID != string(object.GetUID()) || claim.ImmutableGeneration != 1 {
			return fmt.Errorf("Supabase resource claim identity changed for %s", key)
		}
		if err := state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
		currentClaims[key] = claim
		delete(priorClaims, key)
		return state.VerifyPlatformResourceClaim(ctx, op, claim)
	}
	claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: key, Kind: "runtime_component", ResourceID: string(object.GetUID()), ImmutableGeneration: 1, OwnerOperationID: op.ID}
	intents, err := state.PlatformResourceIntents(ctx, op, op.Revision)
	if err != nil {
		return err
	}
	var intent store.PlatformResourceIntent
	for _, candidate := range intents {
		if candidate.Component == key {
			intent = candidate
			break
		}
	}
	if intent.ID == "" || intent.ConfirmedAt != nil || intent.ExternalKey != supabaseExternalKey(kind, object.GetNamespace(), object.GetName()) || intent.OwnerOperationID != op.ID || object.GetLabels()["hakopod.io/resource-intent-id"] != intent.ID || object.GetLabels()["hakopod.io/owner-operation-id"] != op.ID {
		return fmt.Errorf("existing Supabase object %s has no matching durable creation intent", key)
	}
	if err := state.ConfirmPlatformResourceIntent(ctx, op, intent, claim); err != nil {
		return err
	}
	currentClaims[key] = claim
	return nil
}

func verifySupabaseOwned(object metav1.Object, platformID string, namespaceUID types.UID) error {
	return verifySupabaseIdentity(object, platformID, namespaceUID, false)
}

func verifySupabaseIdentity(object metav1.Object, platformID string, namespaceUID types.UID, allowDeleting bool) error {
	if object.GetUID() == "" || !allowDeleting && object.GetDeletionTimestamp() != nil || object.GetLabels()["app.kubernetes.io/managed-by"] != "hakopod" || object.GetLabels()["hakopod.io/managed-platform-id"] != platformID {
		return fmt.Errorf("Supabase object %s ownership changed", object.GetName())
	}
	owners := object.GetOwnerReferences()
	if len(owners) != 1 || owners[0].APIVersion != "v1" || owners[0].Kind != "Namespace" || owners[0].UID != namespaceUID {
		return fmt.Errorf("Supabase object %s namespace ownership changed", object.GetName())
	}
	return nil
}

func (c *Client) applySupabaseObject(ctx context.Context, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired runtime.Object, state ManagedPlatformOperationStore, priorClaims, currentClaims map[string]store.PlatformResourceClaim, before func() error) error {
	metadata, ok := desired.(metav1.Object)
	if !ok {
		return fmt.Errorf("Supabase runtime object has no metadata")
	}
	labels := metadata.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["hakopod.io/owner-operation-id"] = op.ID
	metadata.SetLabels(labels)
	switch object := desired.(type) {
	case *corev1.ConfigMap:
		return c.applySupabaseConfigMap(ctx, state, op, ns, object, priorClaims, currentClaims, before)
	case *corev1.PersistentVolumeClaim:
		return c.applySupabasePVC(ctx, state, op, ns, object, priorClaims, currentClaims, before)
	case *corev1.Service:
		return c.applySupabaseService(ctx, state, op, ns, object, priorClaims, currentClaims, before)
	case *appsv1.Deployment:
		return c.applySupabaseDeployment(ctx, state, op, ns, object, priorClaims, currentClaims, before)
	case *appsv1.StatefulSet:
		return c.applySupabaseStatefulSet(ctx, state, op, ns, object, priorClaims, currentClaims, before)
	case *networkingv1.NetworkPolicy:
		return c.applySupabaseNetworkPolicy(ctx, state, op, ns, object, priorClaims, currentClaims, before)
	default:
		return fmt.Errorf("unsupported Supabase runtime object %T", desired)
	}
}

func (c *Client) applySupabaseConfigMap(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired *corev1.ConfigMap, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.CoreV1().ConfigMaps(ns.Name)
	existing, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if prior[supabaseClaimKey("configmap", desired.Name)].ResourceID != "" || current[supabaseClaimKey("configmap", desired.Name)].ResourceID != "" {
			return fmt.Errorf("claimed Supabase ConfigMap %s disappeared", desired.Name)
		}
		if _, err = reserveSupabaseCreate(ctx, state, op, "configmap", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, e := api.Create(ctx, desired, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "configmap", created, prior, current, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(existing, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if existing.Immutable == nil || !*existing.Immutable || !reflect.DeepEqual(existing.Data, desired.Data) || !reflect.DeepEqual(existing.BinaryData, desired.BinaryData) {
		return fmt.Errorf("immutable Supabase ConfigMap %s changed", desired.Name)
	}
	return claimOrAdvanceSupabaseObject(ctx, state, op, "configmap", existing, prior, current, false)
}

func (c *Client) applySupabasePVC(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired *corev1.PersistentVolumeClaim, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.CoreV1().PersistentVolumeClaims(ns.Name)
	existing, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if prior[supabaseClaimKey("pvc", desired.Name)].ResourceID != "" || current[supabaseClaimKey("pvc", desired.Name)].ResourceID != "" {
			return fmt.Errorf("claimed Supabase PVC %s disappeared", desired.Name)
		}
		if _, err = reserveSupabaseCreate(ctx, state, op, "pvc", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, e := api.Create(ctx, desired, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "pvc", created, prior, current, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(existing, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if !reflect.DeepEqual(existing.Spec.StorageClassName, desired.Spec.StorageClassName) || !reflect.DeepEqual(existing.Spec.AccessModes, desired.Spec.AccessModes) || existing.Spec.Resources.Requests.Storage().Cmp(*desired.Spec.Resources.Requests.Storage()) != 0 {
		return fmt.Errorf("Supabase PVC %s storage contract changed", desired.Name)
	}
	return claimOrAdvanceSupabaseObject(ctx, state, op, "pvc", existing, prior, current, false)
}

func (c *Client) applySupabaseService(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired *corev1.Service, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.CoreV1().Services(ns.Name)
	existing, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if prior[supabaseClaimKey("service", desired.Name)].ResourceID != "" || current[supabaseClaimKey("service", desired.Name)].ResourceID != "" {
			return fmt.Errorf("claimed Supabase Service %s disappeared", desired.Name)
		}
		if _, err = reserveSupabaseCreate(ctx, state, op, "service", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, e := api.Create(ctx, desired, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "service", created, prior, current, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(existing, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, "service", existing, prior, current, false); err != nil {
		return err
	}
	desired = desired.DeepCopy()
	desired.ResourceVersion = existing.ResourceVersion
	desired.Spec.ClusterIP = existing.Spec.ClusterIP
	desired.Spec.ClusterIPs = append([]string(nil), existing.Spec.ClusterIPs...)
	desired.Spec.IPFamilies = append([]corev1.IPFamily(nil), existing.Spec.IPFamilies...)
	desired.Spec.IPFamilyPolicy = existing.Spec.IPFamilyPolicy
	desired.Spec.HealthCheckNodePort = existing.Spec.HealthCheckNodePort
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *Client) applySupabaseDeployment(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired *appsv1.Deployment, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.AppsV1().Deployments(ns.Name)
	existing, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if prior[supabaseClaimKey("deployment", desired.Name)].ResourceID != "" || current[supabaseClaimKey("deployment", desired.Name)].ResourceID != "" {
			return fmt.Errorf("claimed Supabase Deployment %s disappeared", desired.Name)
		}
		if _, err = reserveSupabaseCreate(ctx, state, op, "deployment", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, e := api.Create(ctx, desired, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "deployment", created, prior, current, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(existing, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, "deployment", existing, prior, current, false); err != nil {
		return err
	}
	desired = desired.DeepCopy()
	desired.ResourceVersion = existing.ResourceVersion
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *Client) applySupabaseStatefulSet(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired *appsv1.StatefulSet, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.AppsV1().StatefulSets(ns.Name)
	existing, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if prior[supabaseClaimKey("statefulset", desired.Name)].ResourceID != "" || current[supabaseClaimKey("statefulset", desired.Name)].ResourceID != "" {
			return fmt.Errorf("claimed Supabase StatefulSet %s disappeared", desired.Name)
		}
		if _, err = reserveSupabaseCreate(ctx, state, op, "statefulset", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, e := api.Create(ctx, desired, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "statefulset", created, prior, current, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(existing, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, "statefulset", existing, prior, current, false); err != nil {
		return err
	}
	desired = desired.DeepCopy()
	desired.ResourceVersion = existing.ResourceVersion
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *Client) applySupabaseNetworkPolicy(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, desired *networkingv1.NetworkPolicy, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	api := c.kube.NetworkingV1().NetworkPolicies(ns.Name)
	existing, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if prior[supabaseClaimKey("networkpolicy", desired.Name)].ResourceID != "" || current[supabaseClaimKey("networkpolicy", desired.Name)].ResourceID != "" {
			return fmt.Errorf("claimed Supabase NetworkPolicy %s disappeared", desired.Name)
		}
		if _, err = reserveSupabaseCreate(ctx, state, op, "networkpolicy", desired); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		created, e := api.Create(ctx, desired, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		return claimOrAdvanceSupabaseObject(ctx, state, op, "networkpolicy", created, prior, current, true)
	}
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(existing, op.PlatformID, ns.UID); err != nil {
		return err
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, "networkpolicy", existing, prior, current, false); err != nil {
		return err
	}
	desired = desired.DeepCopy()
	desired.ResourceVersion = existing.ResourceVersion
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func supabaseApplyRank(object runtime.Object) int {
	switch object.(type) {
	case *corev1.ConfigMap:
		return 0
	case *corev1.PersistentVolumeClaim:
		return 1
	case *corev1.Service:
		return 2
	case *networkingv1.NetworkPolicy:
		return 3
	case *appsv1.StatefulSet:
		return 4
	case *appsv1.Deployment:
		return 5
	default:
		return 99
	}
}

func (c *Client) ObserveSupabase(ctx context.Context, op store.ManagedPlatformOperation, manifests managedplatform.SupabaseManifests, current map[string]store.PlatformResourceClaim) (SupabaseRuntimeObservation, error) {
	result := SupabaseRuntimeObservation{Status: "ready", Revision: op.Revision}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, manifests.Namespace.Name, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	if ns.UID != manifests.ExpectedUID || ns.Labels["hakopod.io/managed-platform-id"] != op.PlatformID {
		return result, fmt.Errorf("Supabase namespace identity changed")
	}
	if err = verifySupabaseClaimedUID("namespace", ns, current); err != nil {
		return result, err
	}
	result.NamespaceUID = string(ns.UID)
	for _, object := range manifests.Objects {
		switch desired := object.(type) {
		case *corev1.PersistentVolumeClaim:
			result.ExpectedComponents++
			item, e := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{})
			if e != nil && !apierrors.IsNotFound(e) {
				return result, e
			}
			if e == nil {
				if e = verifySupabaseOwned(item, op.PlatformID, ns.UID); e != nil {
					return result, e
				}
				if e = verifySupabaseClaimedUID("pvc", item, current); e != nil {
					return result, e
				}
				claimRevision, parseErr := strconv.ParseInt(item.Labels["hakopod.io/revision"], 10, 64)
				if parseErr != nil || claimRevision < 1 || claimRevision > op.Revision {
					return result, fmt.Errorf("Supabase PVC revision identity changed")
				}
			}
			if e != nil || item.Status.Phase != corev1.ClaimBound {
				result.Pending = append(result.Pending, "pvc/"+desired.Name)
			} else {
				result.ReadyComponents++
			}
		case *appsv1.Deployment:
			result.ExpectedComponents++
			item, e := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{})
			if e != nil && !apierrors.IsNotFound(e) {
				return result, e
			}
			if e == nil {
				if e = verifySupabaseOwned(item, op.PlatformID, ns.UID); e != nil {
					return result, e
				}
				if e = verifySupabaseClaimedUID("deployment", item, current); e != nil {
					return result, e
				}
				if item.Labels["hakopod.io/revision"] != strconv.FormatInt(op.Revision, 10) {
					return result, fmt.Errorf("Supabase Deployment revision changed")
				}
			}
			if e != nil || item.Generation != item.Status.ObservedGeneration || item.Spec.Replicas == nil || item.Status.Replicas != *item.Spec.Replicas || item.Status.UpdatedReplicas != *item.Spec.Replicas || item.Status.AvailableReplicas != *item.Spec.Replicas {
				result.Pending = append(result.Pending, "deployment/"+desired.Name)
			} else {
				result.ReadyComponents++
			}
		case *appsv1.StatefulSet:
			result.ExpectedComponents++
			item, e := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{})
			if e != nil && !apierrors.IsNotFound(e) {
				return result, e
			}
			if e == nil {
				if e = verifySupabaseOwned(item, op.PlatformID, ns.UID); e != nil {
					return result, e
				}
				if e = verifySupabaseClaimedUID("statefulset", item, current); e != nil {
					return result, e
				}
				if item.Labels["hakopod.io/revision"] != strconv.FormatInt(op.Revision, 10) {
					return result, fmt.Errorf("Supabase StatefulSet revision changed")
				}
			}
			if e != nil || item.Generation != item.Status.ObservedGeneration || item.Spec.Replicas == nil || item.Status.Replicas != *item.Spec.Replicas || item.Status.UpdatedReplicas != *item.Spec.Replicas || item.Status.ReadyReplicas != *item.Spec.Replicas || item.Status.CurrentRevision != item.Status.UpdateRevision {
				result.Pending = append(result.Pending, "statefulset/"+desired.Name)
			} else {
				result.ReadyComponents++
			}
		}
	}
	sort.Strings(result.Pending)
	if len(result.Pending) > 0 {
		result.Status = "pending"
	}
	return result, nil
}

func verifySupabaseClaimedUID(kind string, object metav1.Object, current map[string]store.PlatformResourceClaim) error {
	claim, ok := current[supabaseClaimKey(kind, object.GetName())]
	if !ok || claim.ResourceID != string(object.GetUID()) || claim.ImmutableGeneration != 1 {
		return fmt.Errorf("Supabase readiness claim identity changed for %s", object.GetName())
	}
	return nil
}

func supabaseObservationMap(value SupabaseRuntimeObservation) map[string]any {
	return map[string]any{"status": value.Status, "revision": value.Revision, "namespace_uid": value.NamespaceUID, "ready_components": value.ReadyComponents, "expected_components": value.ExpectedComponents, "pending": append([]string(nil), value.Pending...)}
}

func (c *Client) pruneSupabaseSnapshots(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, manifests managedplatform.SupabaseManifests, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	retain := map[string]bool{}
	for _, name := range manifests.RetainSecretSnapshots {
		retain[name] = true
	}
	cms, err := c.kube.CoreV1().ConfigMaps(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + op.PlatformID, Limit: maxSupabaseRuntimeObjects + 1})
	if err != nil {
		return err
	}
	if cms.Continue != "" || len(cms.Items) > maxSupabaseRuntimeObjects {
		return fmt.Errorf("Supabase ConfigMap prune bound exceeded")
	}
	for i := range cms.Items {
		item := &cms.Items[i]
		revision, e := strconv.ParseInt(item.Labels["hakopod.io/revision"], 10, 64)
		if e != nil || revision > manifests.PruneConfigMapsBeforeRevision {
			continue
		}
		if e = c.deleteSupabaseSnapshot(ctx, state, op, ns, "configmap", item, prior, current, before); e != nil {
			return e
		}
	}
	secrets, err := c.kube.CoreV1().Secrets(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + op.PlatformID, Limit: maxSupabaseRuntimeObjects + 1})
	if err != nil {
		return err
	}
	if secrets.Continue != "" || len(secrets.Items) > maxSupabaseRuntimeObjects {
		return fmt.Errorf("Supabase Secret prune bound exceeded")
	}
	for i := range secrets.Items {
		item := &secrets.Items[i]
		if retain[item.Name] {
			continue
		}
		if err = c.deleteSupabaseSnapshot(ctx, state, op, ns, "secret", item, prior, current, before); err != nil {
			return err
		}
	}
	for key, claim := range prior {
		kind, _, ok := strings.Cut(key, ".")
		if !ok || kind != "secret" && kind != "configmap" {
			return fmt.Errorf("obsolete Supabase resource claim %s is not pruneable", key)
		}
		if _, findErr := c.supabaseClaimedObject(ctx, ns.Name, key); !apierrors.IsNotFound(findErr) {
			if findErr != nil {
				return findErr
			}
			return errSupabasePrunePending
		}
		if err = state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
		if err = state.VerifyPlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		if err = state.ReleasePlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		delete(prior, key)
	}
	return nil
}

func (c *Client) releaseSupabaseSnapshotClaim(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, kind string, object metav1.Object, prior, current map[string]store.PlatformResourceClaim) error {
	key := supabaseClaimKey(kind, object.GetName())
	claim, ok := current[key]
	if !ok {
		claim, ok = prior[key]
		if !ok {
			return fmt.Errorf("Supabase snapshot %s has no durable resource claim", key)
		}
		if claim.ResourceID != string(object.GetUID()) {
			return fmt.Errorf("Supabase snapshot claim identity changed for %s", key)
		}
		if err := state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
		delete(prior, key)
	}
	if claim.ResourceID != string(object.GetUID()) {
		return fmt.Errorf("Supabase snapshot claim identity changed for %s", key)
	}
	if err := state.VerifyPlatformResourceClaim(ctx, op, claim); err != nil {
		return err
	}
	if err := state.ReleasePlatformResourceClaim(ctx, op, claim); err != nil {
		return err
	}
	delete(current, key)
	return nil
}

func (c *Client) deleteSupabaseSnapshot(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, kind string, object metav1.Object, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	if err := verifySupabaseIdentity(object, op.PlatformID, ns.UID, true); err != nil {
		return err
	}
	key := supabaseClaimKey(kind, object.GetName())
	claim, ok := current[key]
	if !ok {
		claim, ok = prior[key]
		if !ok {
			return fmt.Errorf("Supabase snapshot %s has no durable resource claim", key)
		}
		if claim.ResourceID != string(object.GetUID()) {
			return fmt.Errorf("Supabase snapshot claim identity changed for %s", key)
		}
		if err := state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
		current[key] = claim
		delete(prior, key)
	}
	if claim.ResourceID != string(object.GetUID()) {
		return fmt.Errorf("Supabase snapshot claim identity changed for %s", key)
	}
	if err := state.VerifyPlatformResourceClaim(ctx, op, claim); err != nil {
		return err
	}
	if object.GetDeletionTimestamp() == nil {
		if err := before(); err != nil {
			return err
		}
		uid := object.GetUID()
		options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
		var err error
		if kind == "configmap" {
			err = c.kube.CoreV1().ConfigMaps(ns.Name).Delete(ctx, object.GetName(), options)
		} else {
			err = c.kube.CoreV1().Secrets(ns.Name).Delete(ctx, object.GetName(), options)
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if _, err := c.supabaseClaimedObject(ctx, ns.Name, key); !apierrors.IsNotFound(err) {
		if err != nil {
			return err
		}
		return errSupabasePrunePending
	}
	return c.releaseSupabaseSnapshotClaim(ctx, state, op, kind, object, prior, current)
}

func (c *Client) deleteSupabaseOperation(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, before func() error) error {
	prior, current, err := loadSupabaseClaims(ctx, state, op)
	if err != nil {
		return err
	}
	priorIntents, currentIntents, err := loadSupabaseIntents(ctx, state, op)
	if err != nil {
		return err
	}
	nsName := "managed-platform-" + op.PlatformID
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		for _, intent := range append(priorIntents, currentIntents...) {
			if intent.ConfirmedAt == nil {
				if err = state.CancelPlatformResourceIntent(ctx, op, intent); err != nil {
					return err
				}
			}
		}
		for key, claim := range prior {
			if err = state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
				return err
			}
			claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
			current[key] = claim
		}
		for _, claim := range current {
			if err = state.VerifyPlatformResourceClaim(ctx, op, claim); err != nil {
				return err
			}
			if err = state.ReleasePlatformResourceClaim(ctx, op, claim); err != nil {
				return err
			}
		}
		return state.RecordManagedPlatformStep(ctx, op, "succeeded", "deleted", "Supabase runtime was deleted.", map[string]any{"status": "deleted", "revision": op.Revision})
	}
	if err != nil {
		return err
	}
	if ns.UID == "" || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" || ns.Labels["hakopod.io/managed-platform-id"] != op.PlatformID {
		return fmt.Errorf("Supabase namespace ownership changed")
	}
	pendingNamespace := false
	for _, intent := range append(priorIntents, currentIntents...) {
		if intent.ConfirmedAt != nil {
			continue
		}
		kind, name, ok := strings.Cut(intent.Component, ".")
		if !ok || intent.ExternalKey != supabaseExternalKey(kind, func() string {
			if kind == "namespace" {
				return ""
			}
			return ns.Name
		}(), name) {
			return fmt.Errorf("Supabase pending resource intent changed for %s", intent.Component)
		}
		object, findErr := c.supabaseClaimedObject(ctx, ns.Name, intent.Component)
		if apierrors.IsNotFound(findErr) {
			if err = state.CancelPlatformResourceIntent(ctx, op, intent); err != nil {
				return err
			}
			continue
		}
		if findErr != nil {
			return findErr
		}
		if object.GetLabels()["hakopod.io/resource-intent-id"] != intent.ID || object.GetLabels()["hakopod.io/owner-operation-id"] != intent.OwnerOperationID || verifySupabaseDeletionObject(intent.Component, object, op.PlatformID, ns.UID) != nil {
			return fmt.Errorf("Supabase pending resource intent ownership changed for %s", intent.Component)
		}
		if kind == "namespace" {
			pendingNamespace = true
		}
	}
	namespaceKey := supabaseClaimKey("namespace", ns.Name)
	namespaceClaim, ok := current[namespaceKey]
	if !ok {
		namespaceClaim, ok = prior[namespaceKey]
		if !ok && !pendingNamespace {
			return fmt.Errorf("Supabase namespace has no durable resource claim")
		}
		if ok {
			if namespaceClaim.ResourceID != string(ns.UID) {
				return fmt.Errorf("Supabase namespace claim identity changed")
			}
			if err = state.AdvancePlatformResourceClaim(ctx, op, namespaceClaim); err != nil {
				return err
			}
			namespaceClaim.PlatformRevision, namespaceClaim.OwnerOperationID = op.Revision, op.ID
			current[namespaceKey] = namespaceClaim
			delete(prior, namespaceKey)
		}
	}
	if ok {
		if namespaceClaim.ResourceID != string(ns.UID) {
			return fmt.Errorf("Supabase namespace claim identity changed")
		}
		if err = state.VerifyPlatformResourceClaim(ctx, op, namespaceClaim); err != nil {
			return err
		}
	}
	for key, claim := range prior {
		if op.Spec.Kind == "neon" && neonLifecycleClaimComponent(key) {
			continue
		}
		object, findErr := c.supabaseClaimedObject(ctx, ns.Name, key)
		if apierrors.IsNotFound(findErr) && ns.DeletionTimestamp != nil {
			if err = state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
				return err
			}
			claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
			current[key] = claim
			delete(prior, key)
			continue
		}
		if findErr != nil {
			return findErr
		}
		if string(object.GetUID()) != claim.ResourceID || verifySupabaseDeletionObject(key, object, op.PlatformID, ns.UID) != nil {
			return fmt.Errorf("Supabase deletion claim identity changed for %s", key)
		}
		if err = state.AdvancePlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
		claim.PlatformRevision, claim.OwnerOperationID = op.Revision, op.ID
		current[key] = claim
		delete(prior, key)
	}
	for key, claim := range current {
		if key == namespaceKey {
			continue
		}
		if op.Spec.Kind == "neon" && neonLifecycleClaimComponent(key) {
			continue
		}
		object, findErr := c.supabaseClaimedObject(ctx, ns.Name, key)
		if apierrors.IsNotFound(findErr) && ns.DeletionTimestamp != nil {
			if err = state.VerifyPlatformResourceClaim(ctx, op, claim); err != nil {
				return err
			}
			continue
		}
		if findErr != nil {
			return findErr
		}
		if string(object.GetUID()) != claim.ResourceID {
			return fmt.Errorf("Supabase deletion claim identity changed for %s", key)
		}
		if err = verifySupabaseDeletionObject(key, object, op.PlatformID, ns.UID); err != nil {
			return err
		}
		if err = state.VerifyPlatformResourceClaim(ctx, op, claim); err != nil {
			return err
		}
	}
	if ns.DeletionTimestamp == nil {
		if err = before(); err != nil {
			return err
		}
		policy := metav1.DeletePropagationForeground
		if err = c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{PropagationPolicy: &policy, Preconditions: &metav1.Preconditions{UID: &ns.UID}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return state.RecordManagedPlatformStep(ctx, op, "queued", "waiting-delete", "Supabase namespace deletion is still in progress.", map[string]any{"status": "deleting", "revision": op.Revision, "namespace_uid": string(ns.UID)})
}

func neonLifecycleClaimComponent(component string) bool {
	return component == "tenant" || component == "timeline" || strings.HasPrefix(component, "compute-") || strings.HasPrefix(component, "pageserver-registration-") || strings.HasPrefix(component, "safekeeper-registration-")
}

func validNeonLifecycleClaimKind(component, kind string) bool {
	if component == "tenant" {
		return kind == "neon_tenant"
	}
	if component == "timeline" {
		return kind == "neon_timeline"
	}
	return kind == "runtime_component" && (strings.HasPrefix(component, "compute-") || strings.HasPrefix(component, "pageserver-registration-") || strings.HasPrefix(component, "safekeeper-registration-"))
}

func loadSupabaseIntents(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation) ([]store.PlatformResourceIntent, []store.PlatformResourceIntent, error) {
	prior := []store.PlatformResourceIntent{}
	if op.Revision > 1 {
		items, err := state.PlatformResourceIntents(ctx, op, op.Revision-1)
		if err != nil {
			return nil, nil, err
		}
		prior = items
	}
	current, err := state.PlatformResourceIntents(ctx, op, op.Revision)
	if err != nil {
		return nil, nil, err
	}
	if len(prior)+len(current) > maxSupabaseRuntimeObjects {
		return nil, nil, fmt.Errorf("Supabase resource intent bound exceeded")
	}
	return prior, current, nil
}

func verifySupabaseDeletionObject(key string, object metav1.Object, platformID string, namespaceUID types.UID) error {
	kind, _, _ := strings.Cut(key, ".")
	if kind == "namespace" {
		if object.GetUID() == "" || object.GetLabels()["app.kubernetes.io/managed-by"] != "hakopod" || object.GetLabels()["hakopod.io/managed-platform-id"] != platformID {
			return fmt.Errorf("Supabase namespace ownership changed")
		}
		return nil
	}
	return verifySupabaseIdentity(object, platformID, namespaceUID, true)
}

func (c *Client) supabaseClaimedObject(ctx context.Context, namespace, key string) (metav1.Object, error) {
	kind, name, ok := strings.Cut(key, ".")
	if !ok || name == "" {
		return nil, fmt.Errorf("invalid Supabase resource claim key")
	}
	switch kind {
	case "namespace":
		return c.kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	case "secret":
		return c.kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	case "configmap":
		return c.kube.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	case "pvc":
		return c.kube.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	case "service":
		return c.kube.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	case "deployment":
		return c.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	case "statefulset":
		return c.kube.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	case "networkpolicy":
		return c.kube.NetworkingV1().NetworkPolicies(namespace).Get(ctx, name, metav1.GetOptions{})
	default:
		return nil, fmt.Errorf("unsupported Supabase claim kind")
	}
}

func boolPointer(value bool) *bool { return &value }
