package cluster

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func supabaseGatewayCertificateFixture(t *testing.T, names []string, notBefore, notAfter time.Time) map[string][]byte {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Hakopod Supabase fixture CA"}, NotBefore: notBefore.Add(-time.Hour), NotAfter: notAfter.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)}),
		"ca.crt":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
	}
}

func TestValidateSupabaseGatewayTLSBindsPublicAndInternalNames(t *testing.T) {
	now := time.Now()
	valid := supabaseGatewayCertificateFixture(t, []string{"data.example.test", "api-gw"}, now.Add(-time.Minute), now.Add(time.Hour))
	if err := validateSupabaseGatewayTLS(valid, "https://data.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := validateSupabaseGatewayTLS(valid, "https://other.example.test"); err == nil {
		t.Fatal("certificate without the public hostname was accepted")
	}
	publicOnly := supabaseGatewayCertificateFixture(t, []string{"data.example.test"}, now.Add(-time.Minute), now.Add(time.Hour))
	if err := validateSupabaseGatewayTLS(publicOnly, "https://data.example.test"); err == nil {
		t.Fatal("certificate without the internal api-gw hostname was accepted")
	}
	expired := supabaseGatewayCertificateFixture(t, []string{"data.example.test", "api-gw"}, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err := validateSupabaseGatewayTLS(expired, "https://data.example.test"); err == nil {
		t.Fatal("expired certificate was accepted")
	}
	other := supabaseGatewayCertificateFixture(t, []string{"data.example.test", "api-gw"}, now.Add(-time.Minute), now.Add(time.Hour))
	untrusted := map[string][]byte{"tls.crt": valid["tls.crt"], "tls.key": valid["tls.key"], "ca.crt": other["ca.crt"]}
	if err := validateSupabaseGatewayTLS(untrusted, "https://data.example.test"); err == nil {
		t.Fatal("certificate with the wrong trust root was accepted")
	}
}

const validSupabaseGatewayLDS = `resources:
  - '@type': type.googleapis.com/envoy.config.listener.v3.Listener
    name: supabase
    address:
      socket_address:
        address: 0.0.0.0
        port_value: 8443
    filter_chains:
      - transport_socket:
          name: envoy.transport_sockets.tls
          typed_config:
            '@type': type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
            common_tls_context:
              tls_params:
                tls_minimum_protocol_version: TLSv1_2
              tls_certificates:
                - certificate_chain:
                    filename: /etc/envoy/tls/tls.crt
                  private_key:
                    filename: /etc/envoy/tls/tls.key
        filters:
          - name: envoy.filters.network.http_connection_manager
            typed_config:
              '@type': type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
`

func TestValidateSupabaseGatewayLDSRejectsAmbiguousOrPlaintextConfiguration(t *testing.T) {
	if err := validateSupabaseGatewayLDS([]byte(validSupabaseGatewayLDS)); err != nil {
		t.Fatal(err)
	}
	invalid := map[string]string{
		"malformed":            "resources: [",
		"duplicate port":       replaceSupabaseTestString(validSupabaseGatewayLDS, "        port_value: 8443", "        port_value: 8443\n        port_value: 8000"),
		"alias":                "listener: &listener {name: supabase}\ncopy: *listener\n",
		"second listener":      validSupabaseGatewayLDS + "  - '@type': type.googleapis.com/envoy.config.listener.v3.Listener\n    name: plaintext\n",
		"plaintext":            replaceSupabaseTestString(validSupabaseGatewayLDS, "port_value: 8443", "port_value: 8000"),
		"weak TLS":             replaceSupabaseTestString(validSupabaseGatewayLDS, "TLSv1_2", "TLSv1_0"),
		"wrong key path":       replaceSupabaseTestString(validSupabaseGatewayLDS, "/etc/envoy/tls/tls.key", "/tmp/tls.key"),
		"default plaintext":    replaceSupabaseTestString(validSupabaseGatewayLDS, "    filter_chains:", "    default_filter_chain: {}\n    filter_chains:"),
		"additional address":   replaceSupabaseTestString(validSupabaseGatewayLDS, "    filter_chains:", "    additional_addresses: [{address: {socket_address: {address: 0.0.0.0, port_value: 8000}}}]\n    filter_chains:"),
		"extra network filter": replaceSupabaseTestString(validSupabaseGatewayLDS, "          - name: envoy.filters.network.http_connection_manager", "          - name: envoy.filters.network.echo\n          - name: envoy.filters.network.http_connection_manager"),
		"wrong network filter": replaceSupabaseTestString(validSupabaseGatewayLDS, "envoy.filters.network.http_connection_manager", "envoy.filters.network.echo"),
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := validateSupabaseGatewayLDS([]byte(value)); err == nil {
				t.Fatal("invalid gateway LDS was accepted")
			}
		})
	}
}

func TestValidateSupabaseTLSSecretSnapshotsRequiresExactSecretShapes(t *testing.T) {
	now := time.Now()
	values := map[string]map[string][]byte{
		"tls-r1":     supabaseGatewayCertificateFixture(t, []string{"data.example.test", "api-gw"}, now.Add(-time.Minute), now.Add(time.Hour)),
		"runtime-r1": {"value": []byte(validSupabaseGatewayLDS)},
	}
	if err := validateSupabaseTLSSecretSnapshots(values, []string{"tls-r1", "runtime-r1"}, "tls-r1", "runtime-r1", "https://data.example.test"); err != nil {
		t.Fatal(err)
	}
	values["tls-r1"]["unexpected"] = []byte("secret")
	if err := validateSupabaseTLSSecretSnapshots(values, []string{"tls-r1", "runtime-r1"}, "tls-r1", "runtime-r1", "https://data.example.test"); err == nil {
		t.Fatal("TLS snapshot with an unexpected key was accepted")
	}
	delete(values["tls-r1"], "unexpected")
	values["runtime-r1"]["unexpected"] = []byte("secret")
	if err := validateSupabaseTLSSecretSnapshots(values, []string{"tls-r1", "runtime-r1"}, "tls-r1", "runtime-r1", "https://data.example.test"); err == nil {
		t.Fatal("runtime snapshot with an unexpected key was accepted")
	}
}

func replaceSupabaseTestString(value, old, replacement string) string {
	for index := 0; index+len(old) <= len(value); index++ {
		if value[index:index+len(old)] == old {
			return value[:index] + replacement + value[index+len(old):]
		}
	}
	return value
}
