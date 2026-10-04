package cluster

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func validateNeonTLSSecretSnapshots(values map[string]map[string][]byte, spec managedplatform.Spec, platformID string, now time.Time) error {
	if len(platformID) != 32 || spec.Neon == nil {
		return fmt.Errorf("Neon TLS namespace identity is invalid")
	}
	namespace := "managed-platform-" + platformID
	snapshot := func(logical string) (map[string][]byte, error) {
		ref, ok := spec.Secrets[logical]
		if !ok {
			return nil, fmt.Errorf("Neon TLS secret reference %s is unavailable", logical)
		}
		data, ok := values[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]
		if !ok {
			return nil, fmt.Errorf("Neon TLS secret snapshot %s is unavailable", logical)
		}
		return data, nil
	}
	hosts := func(service string) []string {
		return []string{service, service + "." + namespace + ".svc"}
	}
	identities := []struct {
		logical string
		hosts   []string
	}{
		{"controller-auth", hosts("neon-storage-controller")},
		{"controller-database-password", hosts("neon-controller-database")},
	}
	for i := 0; i < spec.Neon.Pageservers; i++ {
		service := "neon-pageserver-" + strconv.Itoa(i)
		if i == 0 {
			identities = append(identities, struct {
				logical string
				hosts   []string
			}{"pageserver-auth", hosts(service)})
		} else {
			identities[len(identities)-1].hosts = append(identities[len(identities)-1].hosts, hosts(service)...)
		}
	}
	for i := 0; i < spec.Neon.Safekeepers; i++ {
		service := "neon-safekeeper-" + strconv.Itoa(i)
		if i == 0 {
			identities = append(identities, struct {
				logical string
				hosts   []string
			}{"safekeeper-auth", hosts(service)})
		} else {
			identities[len(identities)-1].hosts = append(identities[len(identities)-1].hosts, hosts(service)...)
		}
	}
	for i := 0; i < spec.Neon.ComputeReplicas; i++ {
		service := "neon-compute-" + strconv.Itoa(i) + "-control"
		if i == 0 {
			identities = append(identities, struct {
				logical string
				hosts   []string
			}{"compute-auth", hosts(service)})
		} else {
			identities[len(identities)-1].hosts = append(identities[len(identities)-1].hosts, hosts(service)...)
		}
	}
	for _, identity := range identities {
		data, err := snapshot(identity.logical)
		if err != nil {
			return err
		}
		if err = validateNeonServerIdentity(data, [][]byte{data["ca.crt"]}, identity.hosts, now); err != nil {
			return fmt.Errorf("Neon TLS identity %s is invalid: %w", identity.logical, err)
		}
	}
	proxy, err := snapshot("proxy-auth")
	if err != nil {
		return err
	}
	for _, host := range hosts("neon-proxy") {
		if _, err = validateTLSCertificate(proxy["tls.crt"], proxy["tls.key"], host, now); err != nil {
			return fmt.Errorf("Neon TLS identity proxy-auth is invalid: %w", err)
		}
	}
	broker, err := snapshot("broker-auth")
	if err != nil {
		return err
	}
	pageserver, err := snapshot("pageserver-auth")
	if err != nil {
		return err
	}
	safekeeper, err := snapshot("safekeeper-auth")
	if err != nil {
		return err
	}
	if err = validateNeonServerIdentity(broker, [][]byte{broker["ca.crt"], pageserver["ca.crt"], safekeeper["ca.crt"]}, hosts("neon-broker"), now); err != nil {
		return fmt.Errorf("Neon TLS identity broker-auth is invalid: %w", err)
	}
	return nil
}

func validateNeonServerIdentity(data map[string][]byte, trusts [][]byte, hosts []string, now time.Time) error {
	pair, err := tls.X509KeyPair(data["tls.crt"], data["tls.key"])
	if err != nil || len(pair.Certificate) != 1 {
		return fmt.Errorf("certificate and private key do not match")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return fmt.Errorf("server certificate is invalid or outside its validity period")
	}
	for _, trust := range trusts {
		roots := x509.NewCertPool()
		rest, count := trust, 0
		for len(bytes.TrimSpace(rest)) != 0 {
			block, next := pem.Decode(rest)
			if block == nil || block.Type != "CERTIFICATE" || count == 8 {
				return fmt.Errorf("CA bundle is malformed or exceeds eight certificates")
			}
			certificate, parseErr := x509.ParseCertificate(block.Bytes)
			if parseErr != nil || !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
				return fmt.Errorf("CA certificate is invalid or outside its validity period")
			}
			roots.AddCert(certificate)
			count++
			rest = next
		}
		if count == 0 {
			return fmt.Errorf("CA bundle is empty")
		}
		for _, host := range hosts {
			if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
				return fmt.Errorf("server certificate does not verify for its owned endpoint")
			}
		}
		if len(hosts) == 0 {
			return fmt.Errorf("server certificate has no owned endpoint")
		}
		if len(trust) > 64<<10 {
			return fmt.Errorf("CA bundle exceeds 64 KiB")
		}
		if len(data["tls.crt"]) > 32<<10 || len(data["tls.key"]) > 32<<10 {
			return fmt.Errorf("server certificate material exceeds its size limit")
		}
		if len(leaf.ExtKeyUsage) > 0 && !containsNeonExtKeyUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) && !containsNeonExtKeyUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageAny) {
			return fmt.Errorf("certificate does not allow TLS server authentication")
		}
		if leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
			return fmt.Errorf("certificate does not allow a TLS signature")
		}
	}
	return nil
}

func containsNeonExtKeyUsage(values []x509.ExtKeyUsage, wanted x509.ExtKeyUsage) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
