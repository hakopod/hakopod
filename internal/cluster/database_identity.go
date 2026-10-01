package cluster

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Each database has a separate issuer. Private issuer and server keys never
// leave its namespace. CA renewal keeps the signing key so existing client
// trust remains valid throughout the 30-day overlap; key replacement requires
// a separately provisioned database and reviewed connection migration.
func (c *Client) prepareDatabaseIdentity(ctx context.Context, d database.Resource, before func() error) error {
	return c.prepareDatabaseIdentityForTransition(ctx, d, "", "", before)
}

func databaseIdentityIssuerRenewalRequired(create bool, ca *x509.Certificate, now time.Time) bool {
	return create || ca == nil || ca.NotAfter.Before(now.Add(30*24*time.Hour))
}

func (c *Client) prepareDatabaseIdentityForTransition(ctx context.Context, d database.Resource, transitionIntent, expectedIssuerFingerprint string, before func() error) error {
	if !d.Spec.TLSRequired() || d.Spec.Engine == "postgresql" {
		return nil
	}
	if transitionIntent != "" && (d.Spec.Engine != "oracle" || oracleEnterprise(d.Spec) || len(transitionIntent) != 64) {
		return fmt.Errorf("database identity transition is invalid")
	}
	if transitionIntent != "" && len(expectedIssuerFingerprint) != 64 {
		return fmt.Errorf("database identity transition issuer is invalid")
	}
	if d.Spec.Engine != "redis" && d.Spec.Engine != "mysql" && d.Spec.Engine != "mongodb" && d.Spec.Engine != "clickhouse" && d.Spec.Engine != "oracle" && d.Spec.Engine != "vitess" {
		return fmt.Errorf("database identity engine is unsupported")
	}
	if d.Spec.Engine == "mysql" || d.Spec.Engine == "mongodb" || d.Spec.Engine == "oracle" && !oracleEnterprise(d.Spec) {
		names, err := database.NormalizePublicEndpointNames(d.PublicEndpointNames)
		if err != nil {
			return err
		}
		d.PublicEndpointNames = names
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" || ns.UID == "" {
		return fmt.Errorf("database identity namespace ownership changed")
	}
	var addresses []net.IP
	if d.Spec.Engine == "clickhouse" {
		addresses, err = c.prepareClickHousePeerServices(ctx, d, before)
		if err != nil {
			return err
		}
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	root, err := api.Get(ctx, "database-ca", metav1.GetOptions{})
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return err
	}
	now := time.Now()
	var signer crypto.Signer
	var ca *x509.Certificate
	if create {
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		key, err := x509.MarshalPKCS8PrivateKey(signer)
		if err != nil {
			return err
		}
		root = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-ca"), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"ca.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})}}
	} else {
		if err = databaseIdentityOwned(root, d, ns.UID); err != nil {
			return err
		}
		if !boundedDatabaseKeyPair(root.Data["ca.crt"], root.Data["ca.key"]) {
			return fmt.Errorf("database issuer material exceeds its size limit")
		}
		pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
		if err != nil || len(pair.Certificate) != 1 {
			return fmt.Errorf("database issuer key pair is invalid")
		}
		ca, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil {
			return fmt.Errorf("database issuer identity is invalid")
		}
		var ok bool
		signer, ok = pair.PrivateKey.(crypto.Signer)
		if !ok {
			return fmt.Errorf("database issuer signer is invalid")
		}
	}
	issuerRenewalRequired := databaseIdentityIssuerRenewalRequired(create, ca, now)
	if transitionIntent != "" && issuerRenewalRequired {
		return fmt.Errorf("database identity transition requires an issuer outside its renewal window")
	}
	if transitionIntent != "" && fmt.Sprintf("%x", sha256.Sum256(ca.Raw)) != expectedIssuerFingerprint {
		return fmt.Errorf("database identity transition issuer changed")
	}
	if issuerRenewalRequired {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return err
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Hakopod database " + d.ID, OrganizationalUnit: []string{ns.Name}}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(365 * 24 * time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
		if err != nil {
			return err
		}
		ca, err = x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		root.Data["ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if err = before(); err != nil {
			return err
		}
		if create {
			root, err = api.Create(ctx, root, metav1.CreateOptions{})
		} else {
			root, err = api.Update(ctx, root, metav1.UpdateOptions{})
		}
		if err != nil {
			return err
		}
	}
	leaf, err := api.Get(ctx, "database-tls", metav1.GetOptions{})
	create = apierrors.IsNotFound(err)
	if err != nil && !create {
		return err
	}
	names := databaseIdentityNames(d)
	renew := create
	if !create {
		if err = databaseIdentityOwned(leaf, d, ns.UID); err != nil {
			return err
		}
		if !boundedDatabaseKeyPair(leaf.Data["tls.crt"], leaf.Data["tls.key"]) {
			return fmt.Errorf("database server material exceeds its size limit")
		}
		pair, err := tls.X509KeyPair(leaf.Data["tls.crt"], leaf.Data["tls.key"])
		if err != nil || len(pair.Certificate) != 1 {
			return fmt.Errorf("database server key pair is invalid")
		}
		cert, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return fmt.Errorf("database server certificate is invalid")
		}
		renew = cert.NotAfter.Before(now.Add(7*24*time.Hour)) || cert.CheckSignatureFrom(ca) != nil || !reflect.DeepEqual(cert.DNSNames, names) || !sameCertificateIPs(cert.IPAddresses, addresses) || !reflect.DeepEqual(leaf.Data["ca.crt"], root.Data["ca.crt"])
		if transitionIntent != "" && leaf.Annotations["hakopod.io/public-endpoint-identity-intent"] != transitionIntent {
			renew = true
		}
	}
	if !renew {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "database"}, DNSNames: names, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(30 * 24 * time.Hour)}
	template.IPAddresses = addresses
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), signer)
	if err != nil {
		return err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if create {
		leaf = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-tls"), Type: corev1.SecretTypeTLS}
	}
	if transitionIntent != "" {
		if leaf.Annotations == nil {
			leaf.Annotations = map[string]string{}
		}
		leaf.Annotations["hakopod.io/public-endpoint-identity-intent"] = transitionIntent
	}
	leaf.Data = map[string][]byte{"ca.crt": root.Data["ca.crt"], "tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}
	if err = before(); err != nil {
		return err
	}
	if create {
		_, err = api.Create(ctx, leaf, metav1.CreateOptions{})
	} else {
		_, err = api.Update(ctx, leaf, metav1.UpdateOptions{})
	}
	return err
}

func sameCertificateIPs(a, b []net.IP) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

func boundedDatabaseKeyPair(cert, key []byte) bool {
	return len(cert) > 0 && len(cert) <= 32<<10 && len(key) > 0 && len(key) <= 32<<10
}

func databaseIdentityMeta(d database.Resource, uid types.UID, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: DatabaseNamespace(d.ID), Labels: databaseLabels(d), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: DatabaseNamespace(d.ID), UID: uid}}}
}

func databaseIdentityOwned(secret *corev1.Secret, d database.Resource, uid types.UID) error {
	if secret.DeletionTimestamp != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" || secret.Namespace != DatabaseNamespace(d.ID) {
		return fmt.Errorf("database certificate ownership changed")
	}
	for _, owner := range secret.OwnerReferences {
		if owner.UID == uid && owner.Kind == "Namespace" && owner.APIVersion == "v1" && owner.Name == secret.Namespace {
			return nil
		}
	}
	return fmt.Errorf("database certificate namespace identity changed")
}

func databaseIdentityNames(d database.Resource) []string {
	if oracleEnterprise(d.Spec) {
		return oracleEnterpriseNames(d)
	}
	if d.Spec.Engine == "vitess" {
		return vitessIdentityNames(d)
	}
	if d.Spec.Engine == "oracle" {
		ns := DatabaseNamespace(d.ID)
		return append([]string{"database", "database." + ns, "database." + ns + ".svc", "database." + ns + ".svc.cluster.local"}, d.PublicEndpointNames...)
	}
	if d.Spec.Engine == "clickhouse" {
		ns := DatabaseNamespace(d.ID)
		names := []string{"database", "database." + ns, "database." + ns + ".svc", "database." + ns + ".svc.cluster.local"}
		for shard := 0; shard < d.Spec.Shards; shard++ {
			for replica := 0; replica <= d.Spec.Replicas; replica++ {
				host := fmt.Sprintf("chi-database-managed-%d-%d", shard, replica)
				names = append(names, host, host+"."+ns+".svc", host+"."+ns+".svc.cluster.local")
				names = append(names, host+"-0."+host+"."+ns+".svc.cluster.local")
			}
		}
		for i := 0; i < d.Spec.KeeperInstances(); i++ {
			names = append(names, clickhouseKeeperHost(d, i))
		}
		return names
	}
	if d.Spec.Engine == "mongodb" {
		ns := DatabaseNamespace(d.ID)
		return append([]string{"database-svc", "database-svc." + ns, "database-svc." + ns + ".svc", "database-svc." + ns + ".svc.cluster.local", "*.database-svc." + ns + ".svc", "*.database-svc." + ns + ".svc.cluster.local"}, d.PublicEndpointNames...)
	}
	if d.Spec.Engine == "mysql" {
		ns := DatabaseNamespace(d.ID)
		return append([]string{"database", "database." + ns, "database." + ns + ".svc", "database." + ns + ".svc.cluster.local", "database-instances." + ns + ".svc", "database-instances." + ns + ".svc.cluster.local", "*.database-instances." + ns + ".svc", "*.database-instances." + ns + ".svc.cluster.local"}, d.PublicEndpointNames...)
	}
	names := []string{}
	for _, service := range []string{"database", "database-headless", "database-leader", "database-follower", "database-leader-headless", "database-follower-headless"} {
		for _, suffix := range []string{"", "." + DatabaseNamespace(d.ID), "." + DatabaseNamespace(d.ID) + ".svc"} {
			names = append(names, service+suffix)
		}
	}
	for _, service := range []string{"database-headless", "database-leader-headless", "database-follower-headless"} {
		names = append(names, "*."+service+"."+DatabaseNamespace(d.ID)+".svc")
	}
	return names
}
