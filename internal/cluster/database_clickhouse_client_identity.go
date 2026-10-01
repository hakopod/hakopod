package cluster

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const clickhouseClientTLSSecret = "database-client-tls"

func clickhouseClientIdentityNames(d database.Resource) ([]string, error) {
	if d.Spec.Engine != "clickhouse" || d.Spec.Shards < 1 || d.Spec.Shards > 8 || d.Spec.Replicas < 0 || d.Spec.Replicas > 5 {
		return nil, fmt.Errorf("ClickHouse client identity topology is invalid")
	}
	public, err := database.NormalizePublicEndpointNames(d.PublicEndpointNames)
	if err != nil {
		return nil, err
	}
	names := append(databaseIdentityNames(d), public...)
	if len(names) > database.MaxTLSVerificationNames {
		return nil, fmt.Errorf("ClickHouse client identity names exceed their bound")
	}
	return names, nil
}

// Client SAN changes use a separate leaf so Keeper's identity and rollout
// fingerprint remain stable. Shared issuer renewal belongs to maintenance;
// this helper consumes its same-key renewal without changing either source.
func (c *Client) prepareClickHouseClientIdentity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "clickhouse" || !d.Spec.TLSRequired() {
		return fmt.Errorf("ClickHouse client identity requires enforced TLS")
	}
	names, err := clickhouseClientIdentityNames(d)
	if err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("ClickHouse client identity namespace ownership changed")
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	root, err := api.Get(ctx, "database-ca", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(root, d, ns.UID); err != nil {
		return err
	}
	if !boundedDatabaseKeyPair(root.Data["ca.crt"], root.Data["ca.key"]) {
		return fmt.Errorf("ClickHouse issuer material exceeds its size limit")
	}
	now := time.Now()
	_, ca, err := database.ParsePublicTrust(root.Data["ca.crt"], now)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(root.Data["ca.crt"], root.Data["ca.key"])
	if err != nil || len(pair.Certificate) != 1 {
		return fmt.Errorf("ClickHouse issuer key pair is invalid")
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return fmt.Errorf("ClickHouse issuer signer is invalid")
	}
	original, err := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(original, d, ns.UID); err != nil {
		return err
	}
	if !boundedDatabaseKeyPair(original.Data["tls.crt"], original.Data["tls.key"]) {
		return fmt.Errorf("ClickHouse internal identity exceeds its size limit")
	}
	pair, err = tls.X509KeyPair(original.Data["tls.crt"], original.Data["tls.key"])
	if err != nil || len(pair.Certificate) != 1 {
		return fmt.Errorf("ClickHouse internal key pair is invalid")
	}
	internal, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("ClickHouse internal certificate is invalid")
	}
	if _, err = database.VerifyServerCertificate(original.Data["tls.crt"], ca, databaseIdentityNames(d), now); err != nil {
		return err
	}
	if len(internal.IPAddresses) > 16 {
		return fmt.Errorf("ClickHouse internal IP identities exceed their bound")
	}
	for _, ip := range internal.IPAddresses {
		if ip.To16() == nil || ip.IsUnspecified() || ip.IsLoopback() {
			return fmt.Errorf("ClickHouse internal IP identity is invalid")
		}
	}
	leaf, err := api.Get(ctx, clickhouseClientTLSSecret, metav1.GetOptions{})
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return err
	}
	if !create {
		if err = databaseIdentityOwned(leaf, d, ns.UID); err != nil {
			return err
		}
		if !boundedDatabaseKeyPair(leaf.Data["tls.crt"], leaf.Data["tls.key"]) {
			return fmt.Errorf("ClickHouse client identity exceeds its size limit")
		}
		pair, err = tls.X509KeyPair(leaf.Data["tls.crt"], leaf.Data["tls.key"])
		if err != nil || len(pair.Certificate) != 1 {
			return fmt.Errorf("ClickHouse client key pair is invalid")
		}
		cert, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return fmt.Errorf("ClickHouse client certificate is invalid")
		}
		_, verifyErr := database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, names, now)
		if verifyErr == nil && !cert.NotAfter.Before(now.Add(7*24*time.Hour)) && reflect.DeepEqual(cert.DNSNames, names) && sameCertificateIPs(cert.IPAddresses, internal.IPAddresses) && reflect.DeepEqual(leaf.Data["ca.crt"], root.Data["ca.crt"]) {
			return nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	expires := now.Add(30 * 24 * time.Hour)
	if ca.NotAfter.Before(expires) {
		expires = ca.NotAfter
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "database"}, DNSNames: names, IPAddresses: internal.IPAddresses, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, NotBefore: now.Add(-5 * time.Minute), NotAfter: expires}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), signer)
	if err != nil {
		return err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if create {
		leaf = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, clickhouseClientTLSSecret), Type: corev1.SecretTypeTLS}
	}
	leaf.Data = map[string][]byte{"ca.crt": root.Data["ca.crt"], "tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}
	if _, err = database.VerifyServerCertificate(leaf.Data["tls.crt"], ca, names, now); err != nil {
		return err
	}
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
