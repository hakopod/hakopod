// Package secretprovider reads bounded snapshots from explicitly configured
// external providers. It never exposes resolved values through the API.
package secretprovider

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

var ErrInput = errors.New("invalid secret provider")
var ErrUnavailable = errors.New("external secret is unavailable; check provider access and configuration")

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)

type Scope struct {
	Project      string   `json:"project"`
	Environments []string `json:"environments"`
}

type Provider struct {
	Name                 string    `json:"name"`
	Kind                 string    `json:"kind"`
	Endpoint             string    `json:"endpoint"`
	Mount                string    `json:"mount,omitempty"`
	RootPath             string    `json:"root_path"`
	ProjectID            string    `json:"project_id,omitempty"`
	Environment          string    `json:"environment,omitempty"`
	Namespace            string    `json:"namespace,omitempty"`
	CACert               string    `json:"ca_cert,omitempty"`
	PrivateCIDRs         []string  `json:"private_cidrs"`
	Scopes               []Scope   `json:"scopes"`
	Revision             int64     `json:"revision"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	EncryptedCredentials []byte    `json:"-"`
}

type Credentials struct {
	Token        string `json:"token,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

type Input struct {
	Provider
	ExpectedRevision int64        `json:"expected_revision"`
	Credentials      *Credentials `json:"credentials,omitempty"`
}

func (p Provider) Validate() error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInput, message) }
	if !namePattern.MatchString(p.Name) || (p.Kind != "vault" && p.Kind != "infisical") {
		return invalid("use a valid name and kind vault or infisical")
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || len(p.Endpoint) > 512 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return invalid("endpoint must be an HTTPS origin without credentials, path or query")
	}
	if len(p.CACert) > 32<<10 || !spec.ValidSecretPath(p.RootPath) || len(p.PrivateCIDRs) > 16 {
		return invalid("root path, CA certificate or private network list exceeds bounds")
	}
	if p.CACert != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(p.CACert)) {
		return invalid("ca_cert must contain a PEM certificate authority")
	}
	for _, raw := range p.PrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 || prefix != prefix.Masked() || !prefix.Masked().Addr().IsPrivate() {
			return invalid("private_cidrs accepts explicit private network prefixes only")
		}
		// A CIDR may not widen an IPv6 ULA allowance into public addresses.
		if prefix.Addr().Is6() && prefix.Bits() < 7 {
			return invalid("invalid private network prefix")
		}
	}
	if p.Kind == "vault" {
		if p.Mount == "" || !spec.ValidSecretPath(p.Mount) || p.ProjectID != "" || p.Environment != "" || !spec.ValidSecretPath(p.Namespace) {
			return invalid("Vault requires a KV v2 mount and optional namespace")
		}
	} else if p.ProjectID == "" || len(p.ProjectID) > 128 || !spec.ValidSecretPath(p.ProjectID) || strings.Contains(p.ProjectID, "/") || !namePattern.MatchString(p.Environment) || p.Mount != "" || p.Namespace != "" {
		return invalid("Infisical requires project_id and environment")
	}
	if len(p.Scopes) == 0 || len(p.Scopes) > 32 {
		return invalid("select between 1 and 32 project access scopes")
	}
	projects := map[string]bool{}
	for _, scope := range p.Scopes {
		if !namePattern.MatchString(scope.Project) || projects[scope.Project] || len(scope.Environments) > 32 {
			return invalid("project scopes must be valid and unique")
		}
		projects[scope.Project] = true
		environments := map[string]bool{}
		for _, environment := range scope.Environments {
			if !namePattern.MatchString(environment) || environments[environment] {
				return invalid("environment scopes must be valid and unique")
			}
			environments[environment] = true
		}
	}
	return nil
}

func (c Credentials) Validate(kind string) error {
	valid := func(value string) bool {
		return value != "" && len(value) <= 8192 && !strings.ContainsAny(value, "\x00\r\n")
	}
	if kind == "vault" && valid(c.Token) && c.ClientID == "" && c.ClientSecret == "" || kind == "infisical" && valid(c.ClientID) && valid(c.ClientSecret) && c.Token == "" {
		return nil
	}
	return fmt.Errorf("%w: provide a Vault token or an Infisical client ID and client secret", ErrInput)
}

func (p Provider) Allows(project, environment string) bool {
	for _, scope := range p.Scopes {
		if scope.Project != project {
			continue
		}
		if len(scope.Environments) == 0 {
			return true
		}
		for _, name := range scope.Environments {
			if name == environment {
				return true
			}
		}
	}
	return false
}

// SameSource makes destination changes explicit: stored credentials are never
// silently forwarded to a replacement endpoint, path or trust policy.
func (p Provider) SameSource(other Provider) bool {
	return p.Name == other.Name && p.Kind == other.Kind && p.Endpoint == other.Endpoint && p.Mount == other.Mount && p.RootPath == other.RootPath && p.ProjectID == other.ProjectID && p.Environment == other.Environment && p.Namespace == other.Namespace && p.CACert == other.CACert && strings.Join(p.PrivateCIDRs, "\x00") == strings.Join(other.PrivateCIDRs, "\x00")
}
