package cluster

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const platformTLSRenewBefore = 7 * 24 * time.Hour

// Each platform owns a separate issuer. Only its public certificate is copied
// into application snapshots. The issuer key never enters a runtime request,
// pod template, environment variable, operation observation or response.
func (c *Client) prepareManagedPlatformIssuer(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, prior, current map[string]store.PlatformResourceClaim, before func() error, now time.Time) (*corev1.Secret, *x509.Certificate, crypto.Signer, error) {
	api := c.kube.CoreV1().Secrets(ns.Name)
	root, err := api.Get(ctx, managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return nil, nil, nil, err
	}
	var signer crypto.Signer
	var ca *x509.Certificate
	if create {
		key := supabaseClaimKey("secret", managedplatform.ManagedTLSIssuerSecret)
		if prior[key].ResourceID != "" || current[key].ResourceID != "" {
			return nil, nil, nil, fmt.Errorf("claimed managed platform issuer disappeared")
		}
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, nil, err
		}
		private, err := x509.MarshalPKCS8PrivateKey(signer)
		if err != nil {
			return nil, nil, nil, err
		}
		root = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: managedplatform.ManagedTLSIssuerSecret, Namespace: ns.Name, Labels: supabaseLabels(op), OwnerReferences: []metav1.OwnerReference{supabaseNamespaceOwner(ns)}}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"ca.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}}
	} else {
		if err = verifySupabaseOwned(root, op.PlatformID, ns.UID); err != nil {
			return nil, nil, nil, err
		}
		if root.Immutable != nil && *root.Immutable || len(root.Data) != 2 || !boundedDatabaseKeyPair(root.Data["ca.crt"], root.Data["ca.key"]) {
			return nil, nil, nil, fmt.Errorf("managed platform issuer shape is invalid")
		}
		if err = claimOrAdvanceSupabaseObject(ctx, state, op, "secret", root, prior, current, false); err != nil {
			return nil, nil, nil, err
		}
		pair, pairErr := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
		if pairErr != nil || len(pair.Certificate) != 1 {
			return nil, nil, nil, fmt.Errorf("managed platform issuer key pair is invalid")
		}
		ca, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || ca.Subject.CommonName != "Hakopod platform "+op.PlatformID || !reflect.DeepEqual(ca.Subject.OrganizationalUnit, []string{ns.Name}) {
			return nil, nil, nil, fmt.Errorf("managed platform issuer identity is invalid")
		}
		var ok bool
		signer, ok = pair.PrivateKey.(crypto.Signer)
		if !ok {
			return nil, nil, nil, fmt.Errorf("managed platform issuer signer is invalid")
		}
	}
	if databaseIdentityIssuerRenewalRequired(create, ca, now) {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, nil, nil, err
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Hakopod platform " + op.PlatformID, OrganizationalUnit: []string{ns.Name}}, IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(365 * 24 * time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
		if err != nil {
			return nil, nil, nil, err
		}
		ca, err = x509.ParseCertificate(der)
		if err != nil {
			return nil, nil, nil, err
		}
		root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if create {
			if _, err = reserveSupabaseCreate(ctx, state, op, "secret", root); err != nil {
				return nil, nil, nil, err
			}
		}
		if err = before(); err != nil {
			return nil, nil, nil, err
		}
		if create {
			root, err = api.Create(ctx, root, metav1.CreateOptions{})
		} else {
			root, err = api.Update(ctx, root, metav1.UpdateOptions{})
		}
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, "secret", root, prior, current, create); err != nil {
		return nil, nil, nil, err
	}
	return root, ca, signer, nil
}

func platformTLSNames(spec managedplatform.Spec, platformID, logical string) ([]string, []net.IP, error) {
	namespace := "managed-platform-" + platformID
	names := []string{}
	var ips []net.IP
	add := func(service string) {
		names = append(names, service, service+"."+namespace+".svc", service+"."+namespace+".svc.cluster.local")
	}
	switch logical {
	case "database-tls-certificate":
		add("db")
	case "gateway-tls-certificate":
		add("api-gw")
		if spec.Supabase == nil {
			return nil, nil, fmt.Errorf("Supabase gateway configuration is missing")
		}
		u, err := url.Parse(spec.Supabase.PublicURL)
		if err != nil || u.Hostname() == "" {
			return nil, nil, fmt.Errorf("Supabase gateway hostname is invalid")
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil {
			ips = append(ips, ip)
		} else {
			names = append(names, u.Hostname())
		}
	case "broker-auth":
		add("neon-broker")
	case "controller-auth":
		add("neon-storage-controller")
	case "controller-database-password":
		add("neon-controller-database")
	case "proxy-auth":
		add("neon-proxy")
	case "pageserver-auth", "safekeeper-auth", "compute-auth":
		if spec.Neon == nil {
			return nil, nil, fmt.Errorf("Neon TLS configuration is missing")
		}
		count, component, suffix := spec.Neon.Pageservers, "pageserver", ""
		if logical == "safekeeper-auth" {
			count, component = spec.Neon.Safekeepers, "safekeeper"
		}
		if logical == "compute-auth" {
			count, component, suffix = spec.Neon.ComputeReplicas, "compute", "-control"
		}
		if count < 1 || count > 8 {
			return nil, nil, fmt.Errorf("managed platform TLS member count is invalid")
		}
		for i := 0; i < count; i++ {
			add("neon-" + component + "-" + strconv.Itoa(i) + suffix)
		}
	default:
		return nil, nil, fmt.Errorf("managed platform TLS component is unsupported")
	}
	sort.Strings(names)
	names = uniquePlatformTLSNames(names)
	return names, ips, nil
}

func uniquePlatformTLSNames(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func platformTLSSecretName(logical string, data map[string][]byte) string {
	encoded, _ := json.Marshal(data)
	digest := sha256.Sum256(encoded)
	return "platform-tls-" + managedplatform.ManagedTLSName(logical) + "-" + hex.EncodeToString(digest[:8]) + "-r1"
}

func validPlatformTLSLeaf(data, credentials map[string][]byte, ca *x509.Certificate, caPEM []byte, names []string, ips []net.IP, now time.Time, proxy bool) bool {
	expected := len(credentials) + 3
	if proxy {
		expected--
	}
	if len(data) != expected || !boundedDatabaseKeyPair(data["tls.crt"], data["tls.key"]) {
		return false
	}
	for key, value := range credentials {
		if !bytes.Equal(data[key], value) {
			return false
		}
	}
	if !proxy && !bytes.Equal(data["ca.crt"], caPEM) {
		return false
	}
	pair, err := tls.X509KeyPair(data["tls.crt"], data["tls.key"])
	if err != nil || len(pair.Certificate) != 1 {
		return false
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	return err == nil && !cert.IsCA && !now.Before(cert.NotBefore) && cert.NotAfter.After(now.Add(platformTLSRenewBefore)) && cert.CheckSignatureFrom(ca) == nil && reflect.DeepEqual(cert.DNSNames, names) && sameCertificateIPs(cert.IPAddresses, ips) && reflect.DeepEqual(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
}

func issuePlatformTLSLeaf(credentials map[string][]byte, ca *x509.Certificate, signer crypto.Signer, caPEM []byte, names []string, ips []net.IP, now time.Time, proxy bool) (map[string][]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, IPAddresses: ips, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(30 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), signer)
	if err != nil {
		return nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	data := copySecretData(credentials)
	data["tls.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	data["tls.key"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
	if !proxy {
		data["ca.crt"] = append([]byte(nil), caPEM...)
	}
	return data, nil
}

// Certificate snapshots use their content hash as identity. Retries reuse the
// same owned material, and new references force an ordinary workload rollout.
// The reviewed immutable specification and credential bytes remain unchanged.
func (c *Client) prepareManagedPlatformTLS(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, spec *managedplatform.Spec, previous **managedplatform.Spec, snapshots *map[string]map[string][]byte, prior, current map[string]store.PlatformResourceClaim, before func() error) error {
	if spec.TLSMode != "managed" {
		return nil
	}
	if previous != nil && *previous != nil {
		old := **previous
		if old.TLSMode != "managed" {
			return fmt.Errorf("TLS ownership mode cannot change in place")
		}
		if spec.Kind == "neon" {
			for key, ref := range spec.Secrets {
				if old.Secrets[key] != ref {
					return fmt.Errorf("Neon secret rotation requires a separately reviewed rolling protocol")
				}
			}
		}
	}
	now := time.Now()
	root, ca, signer, err := c.prepareManagedPlatformIssuer(ctx, state, op, ns, prior, current, before, now)
	if err != nil {
		return err
	}
	found, err := c.kube.CoreV1().Secrets(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + op.PlatformID, Limit: maxSupabaseRuntimeObjects + 1})
	if err != nil {
		return err
	}
	if found.Continue != "" || len(found.Items) > maxSupabaseRuntimeObjects {
		return fmt.Errorf("managed platform TLS inventory exceeds its bound")
	}
	sort.Slice(found.Items, func(i, j int) bool { return found.Items[i].Name < found.Items[j].Name })
	refs := make(map[string]managedplatform.SecretReference, len(spec.Secrets)+2)
	values := make(map[string]map[string][]byte, len(*snapshots)+2)
	for key, ref := range spec.Secrets {
		refs[key] = ref
	}
	for key, data := range *snapshots {
		values[key] = data
	}
	for _, logical := range managedplatform.ManagedTLSLogicalNames(spec.Kind) {
		credentials := map[string][]byte{}
		if ref, ok := spec.Secrets[logical]; ok {
			credentials = copySecretData((*snapshots)[secretSnapshotNameForCluster(ref)])
			for _, key := range []string{"ca.key", "tls.key", "tls.crt", "ca.crt"} {
				if _, present := credentials[key]; present {
					return fmt.Errorf("managed TLS credential snapshot must not contain certificate material")
				}
			}
			delete(values, secretSnapshotNameForCluster(ref))
		}
		names, ips, err := platformTLSNames(*spec, op.PlatformID, logical)
		if err != nil {
			return err
		}
		proxy := spec.Kind == "neon" && logical == "proxy-auth"
		var data map[string][]byte
		for i := range found.Items {
			candidate := &found.Items[i]
			if !strings.HasPrefix(candidate.Name, "platform-tls-"+managedplatform.ManagedTLSName(logical)+"-") {
				continue
			}
			if err = verifySupabaseOwned(candidate, op.PlatformID, ns.UID); err != nil {
				return err
			}
			if candidate.Immutable == nil || !*candidate.Immutable || candidate.Name != platformTLSSecretName(logical, candidate.Data) {
				return fmt.Errorf("managed platform TLS snapshot content changed")
			}
			if validPlatformTLSLeaf(candidate.Data, credentials, ca, root.Data["ca.crt"], names, ips, now, proxy) {
				if err = claimOrAdvanceSupabaseObject(ctx, state, op, "secret", candidate, prior, current, false); err != nil {
					return err
				}
				data = copySecretData(candidate.Data)
				break
			}
		}
		if data == nil {
			data, err = issuePlatformTLSLeaf(credentials, ca, signer, root.Data["ca.crt"], names, ips, now, proxy)
			if err != nil {
				return err
			}
		}
		name := platformTLSSecretName(logical, data)
		if err = c.applySupabaseSecret(ctx, state, op, ns, name, data, prior, current, before); err != nil {
			return err
		}
		refs[logical] = managedplatform.SecretReference{Name: strings.TrimSuffix(name, "-r1"), Revision: 1}
		values[name] = data
	}
	spec.Secrets, spec.TLSMode = refs, "operator"
	if previous != nil && *previous != nil {
		p := **previous
		p.Secrets = make(map[string]managedplatform.SecretReference, len(p.Secrets)+2)
		for key, ref := range (**previous).Secrets {
			p.Secrets[key] = ref
		}
		// TLS is controller-owned. Database credential compatibility remains
		// checked against the caller's reviewed previous credential references.
		if p.TLSMode == "managed" {
			p.TLSMode = "operator"
			for _, key := range managedplatform.ManagedTLSLogicalNames(spec.Kind) {
				p.Secrets[key] = refs[key]
			}
		}
		*previous = &p
	}
	*snapshots = values
	return nil
}

// Recovery consumes the target's existing issuer and snapshots. It never copies
// the source's TLS key or creates a replacement identity outside a lifecycle or
// certificate-maintenance lease.
func (c *Client) readManagedPlatformTLS(ctx context.Context, platformID string, spec *managedplatform.Spec, previous **managedplatform.Spec, snapshots *map[string]map[string][]byte, claims map[string]store.PlatformResourceClaim) error {
	if spec.TLSMode != "managed" {
		return nil
	}
	namespace := "managed-platform-" + platformID
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil || ns.DeletionTimestamp != nil || claims["namespace."+namespace].ResourceID != string(ns.UID) || ns.Labels["hakopod.io/managed-platform-id"] != platformID {
		return fmt.Errorf("managed platform recovery TLS namespace identity changed")
	}
	root, err := c.kube.CoreV1().Secrets(namespace).Get(ctx, managedplatform.ManagedTLSIssuerSecret, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = verifySupabaseOwned(root, platformID, ns.UID); err != nil {
		return err
	}
	if claims["secret."+root.Name].ResourceID != string(root.UID) {
		return fmt.Errorf("managed platform recovery issuer claim changed")
	}
	block, _ := pem.Decode(root.Data["ca.crt"])
	if block == nil {
		return fmt.Errorf("managed platform recovery issuer is invalid")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || ca.Subject.CommonName != "Hakopod platform "+platformID {
		return fmt.Errorf("managed platform recovery issuer is invalid")
	}
	inventory, err := c.kube.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + platformID, Limit: maxSupabaseRuntimeObjects + 1})
	if err != nil {
		return err
	}
	if inventory.Continue != "" || len(inventory.Items) > maxSupabaseRuntimeObjects {
		return fmt.Errorf("managed platform recovery TLS inventory exceeds its bound")
	}
	refs := make(map[string]managedplatform.SecretReference, len(spec.Secrets)+2)
	values := make(map[string]map[string][]byte, len(*snapshots)+2)
	for key, ref := range spec.Secrets {
		refs[key] = ref
	}
	for name, data := range *snapshots {
		values[name] = data
	}
	sort.Slice(inventory.Items, func(i, j int) bool { return inventory.Items[i].Name < inventory.Items[j].Name })
	for _, logical := range managedplatform.ManagedTLSLogicalNames(spec.Kind) {
		credentials := map[string][]byte{}
		if ref, ok := spec.Secrets[logical]; ok {
			credentials = copySecretData((*snapshots)[secretSnapshotNameForCluster(ref)])
			delete(values, secretSnapshotNameForCluster(ref))
		}
		names, ips, err := platformTLSNames(*spec, platformID, logical)
		if err != nil {
			return err
		}
		selected := ""
		for i := range inventory.Items {
			candidate := &inventory.Items[i]
			if !strings.HasPrefix(candidate.Name, "platform-tls-"+managedplatform.ManagedTLSName(logical)+"-") {
				continue
			}
			if err = verifySupabaseOwned(candidate, platformID, ns.UID); err != nil {
				return err
			}
			if candidate.Immutable == nil || !*candidate.Immutable || candidate.Name != platformTLSSecretName(logical, candidate.Data) || claims["secret."+candidate.Name].ResourceID != string(candidate.UID) {
				return fmt.Errorf("managed platform recovery TLS snapshot ownership changed")
			}
			if !validPlatformTLSLeaf(candidate.Data, credentials, ca, root.Data["ca.crt"], names, ips, time.Now(), logical == "proxy-auth") {
				continue
			}
			selected = candidate.Name
			values[selected] = copySecretData(candidate.Data)
			refs[logical] = managedplatform.SecretReference{Name: strings.TrimSuffix(selected, "-r1"), Revision: 1}
			break
		}
		if selected == "" {
			return fmt.Errorf("managed platform recovery requires its current maintained TLS identity")
		}
	}
	spec.Secrets, spec.TLSMode = refs, "operator"
	if previous != nil && *previous != nil {
		p := **previous
		p.Secrets = make(map[string]managedplatform.SecretReference, len(p.Secrets)+2)
		for key, ref := range (**previous).Secrets {
			p.Secrets[key] = ref
		}
		p.TLSMode = "operator"
		for _, key := range managedplatform.ManagedTLSLogicalNames(spec.Kind) {
			p.Secrets[key] = refs[key]
		}
		*previous = &p
	}
	*snapshots = values
	return nil
}
