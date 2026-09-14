package spec

import (
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type TemplateSecretField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Format      string `json:"format"`
	Generate    bool   `json:"generate"`
	Optional    bool   `json:"optional"`
}

var databaseIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func TemplateSecretFields(id string) []TemplateSecretField {
	for _, t := range Templates() {
		if t.ID == id {
			return t.SecretFields
		}
	}
	return []TemplateSecretField{}
}

func TemplateSecretFieldByName(id, name string) (TemplateSecretField, bool) {
	for _, field := range TemplateSecretFields(id) {
		if field.Name == name {
			return field, true
		}
	}
	return TemplateSecretField{}, false
}

// Validation errors name the reference and requirement, never its contents.
func ValidateTemplateSecret(id, name, value string) error {
	field, ok := TemplateSecretFieldByName(id, name)
	if !ok {
		return fmt.Errorf("unsupported template secret reference")
	}
	if len(value) == 0 || len(value) > 64<<10 || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be nonempty and at most 64 KiB without NUL", name)
	}
	switch field.Format {
	case "password", "token", "token64", "provider-key":
		minimum := 16
		if field.Format == "token" {
			minimum = 32
		}
		if field.Format == "token64" {
			minimum = 64
		}
		if field.Format == "provider-key" {
			minimum = 1
		}
		if len(value) < minimum || len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must have %d–4096 characters on one line without surrounding whitespace", name, minimum)
		}
		if id == "open-webui" && name == "provider-key" && strings.Contains(value, ";") {
			return fmt.Errorf("provider-key must contain one provider credential")
		}
	case "hex32":
		if _, err := hex.DecodeString(value); err != nil || len(value) != 32 {
			return fmt.Errorf("%s must contain exactly 32 hexadecimal characters", name)
		}
	case "base64-32":
		decoded, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || len(decoded) != 32 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must be 32 bytes encoded as standard Base64", name)
		}
	case "postgres-url", "redis-url":
		u, err := url.Parse(value)
		if err != nil || u.User == nil || u.Fragment != "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must be a valid private service connection URL", name)
		}
		password, present := u.User.Password()
		if !present || len(password) < 16 {
			return fmt.Errorf("%s must include the matching password", name)
		}
		if field.Format == "postgres-url" {
			if (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host != "db:5432" || u.Path != "/app" || u.User.Username() != "hakopod" || (u.RawQuery != "" && u.RawQuery != "sslmode=disable") {
				return fmt.Errorf("database-url must target hakopod at db:5432/app, with no options except sslmode=disable")
			}
		} else if u.Scheme != "redis" || u.Host != "redis:6379" || (u.Path != "" && u.Path != "/0") || (u.User.Username() != "" && u.User.Username() != "default") || u.RawQuery != "" {
			return fmt.Errorf("redis-url must target redis:6379 database 0 with password authentication")
		}
	case "certificate":
		block, rest := pem.Decode([]byte(value))
		if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
			return fmt.Errorf("%s must contain one PEM certificate", name)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("%s must contain a valid X.509 certificate", name)
		}
	case "private-key":
		block, rest := pem.Decode([]byte(value))
		if block == nil || strings.TrimSpace(string(rest)) != "" {
			return fmt.Errorf("%s must contain one unencrypted PEM private key", name)
		}
		var err error
		switch block.Type {
		case "RSA PRIVATE KEY":
			_, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		case "EC PRIVATE KEY":
			_, err = x509.ParseECPrivateKey(block.Bytes)
		case "PRIVATE KEY":
			_, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		default:
			err = fmt.Errorf("unsupported key")
		}
		if err != nil {
			return fmt.Errorf("%s must contain a supported unencrypted PEM private key", name)
		}
	}
	return nil
}

func ValidateTemplateSecretSet(id string, required []string, values map[string]string) error {
	for _, field := range TemplateSecretFields(id) {
		if !field.Optional && !slices.Contains(required, field.Name) {
			return fmt.Errorf("template configuration is missing secret reference %s", field.Name)
		}
	}
	for _, name := range required {
		value, ok := values[name]
		if !ok {
			return fmt.Errorf("save required secret %s before deploying", name)
		}
		if err := ValidateTemplateSecret(id, name, value); err != nil {
			return err
		}
	}
	if id == "mysql" && subtle.ConstantTimeCompare([]byte(values["database-password"]), []byte(values["database-root-password"])) == 1 {
		return fmt.Errorf("MySQL root and application passwords must differ")
	}
	if id == "infisical" {
		for reference, password := range map[string]string{"database-url": "database-password", "redis-url": "redis-password"} {
			u, err := url.Parse(values[reference])
			if err != nil || u.User == nil {
				return fmt.Errorf("%s must contain a valid connection URL", reference)
			}
			actual, _ := u.User.Password()
			if subtle.ConstantTimeCompare([]byte(actual), []byte(values[password])) != 1 {
				return fmt.Errorf("%s does not match %s; regenerate the connection URL", reference, password)
			}
		}
	}
	if id == "cockroachdb" {
		caBlock, _ := pem.Decode([]byte(values["database-ca"]))
		ca, err := x509.ParseCertificate(caBlock.Bytes)
		if err != nil || !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
			return fmt.Errorf("database-ca must be a signing CA certificate")
		}
		pair, err := tls.X509KeyPair([]byte(values["database-node-cert"]), []byte(values["database-node-key"]))
		if err != nil {
			return fmt.Errorf("database-node-cert and database-node-key must match")
		}
		node, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || node.Subject.CommonName != "node" || node.IsCA || node.VerifyHostname("main") != nil || node.VerifyHostname("localhost") != nil || !slices.Contains(node.ExtKeyUsage, x509.ExtKeyUsageServerAuth) || !slices.Contains(node.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
			return fmt.Errorf("node certificate must identify node, cover main and localhost, and allow server and client authentication")
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		if _, err := node.Verify(x509.VerifyOptions{Roots: roots, DNSName: "main", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return fmt.Errorf("node certificate must be current and signed by database-ca")
		}
	}
	return nil
}
