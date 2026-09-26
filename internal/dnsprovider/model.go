// Package dnsprovider creates the ownership and routing DNS records that a user
// would otherwise enter by hand at their DNS provider. A credential is a named
// row, installation-wide or scoped to one project and environment, carrying a
// revision, an enabled flag and encrypted credentials, following the git
// connection model in internal/store/023_git_connections.sql and
// internal/store/031_scoped_git.sql.
package dnsprovider

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

var ErrInput = errors.New("invalid DNS provider")
var ErrUnavailable = errors.New("the DNS provider is unavailable; check provider access, permissions and configuration")
var ErrExists = errors.New("a DNS record already exists at that name and type")

// KindCloudflare is the only kind. A second kind adds itself when it exists.
const KindCloudflare = "cloudflare"

const maxZoneFilter = 32 // a credential permitted everywhere is not a boundary

type Provider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	// ZoneFilter lists the DNS zones this credential may write to. hakopod
	// cannot see what a provider token actually reaches, so this list, not the
	// token, is the enforceable boundary.
	ZoneFilter           []string  `json:"zone_filter"`
	Revision             int64     `json:"revision"`
	Enabled              bool      `json:"enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	EncryptedCredentials []byte    `json:"-"`
}

type Credentials struct {
	Token string `json:"token"`
}

type Input struct {
	Provider
	ExpectedRevision int64        `json:"expected_revision"`
	Credentials      *Credentials `json:"credentials,omitempty"`
}

func (p Provider) Validate() error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInput, message) }
	name := strings.TrimSpace(p.Name)
	if name != p.Name || name == "" || len(name) > 80 || strings.ContainsAny(name, "\x00\r\n") {
		return invalid("name must be between 1 and 80 printable characters")
	}
	if p.Kind != KindCloudflare {
		return invalid("kind must be cloudflare")
	}
	// A scope is both a project and an environment, or neither, exactly as the
	// git_connection_scope constraint requires.
	if (p.Project == "") != (p.Environment == "") {
		return invalid("scope a provider to both a project and an environment, or to neither")
	}
	if len(p.Project) > 63 || len(p.Environment) > 63 {
		return invalid("project and environment names exceed bounds")
	}
	if p.Revision < 0 {
		return invalid("revision must not be negative")
	}
	if len(p.ZoneFilter) == 0 || len(p.ZoneFilter) > maxZoneFilter {
		return invalid("list between 1 and 32 permitted DNS zones")
	}
	seen := map[string]bool{}
	for _, zone := range p.ZoneFilter {
		if !spec.ValidHostname(zone) || seen[zone] {
			return invalid("permitted zones must be unique lowercase DNS names without a scheme, port or wildcard")
		}
		seen[zone] = true
	}
	return nil
}

// Allows reports whether this provider may be used for a project and
// environment. An unscoped provider is installation-wide.
func (p Provider) Allows(project, environment string) bool {
	if !p.Enabled {
		return false
	}
	return p.Project == "" || (p.Project == project && p.Environment == environment)
}

// validDNSName accepts an underscore label such as _acme-challenge or a TXT
// ownership name, which spec.ValidHostname refuses because a Kubernetes name
// cannot carry one. Everything else about the name is checked as usual.
func validDNSName(name string) bool {
	labels := strings.Split(name, ".")
	for i, label := range labels {
		labels[i] = strings.TrimPrefix(label, "_")
	}
	return spec.ValidHostname(strings.Join(labels, "."))
}

// AllowsHostname reports whether a hostname falls inside the zone filter. A
// hostname outside it is refused whatever the token itself can reach.
func (p Provider) AllowsHostname(host string) bool {
	if !validDNSName(host) {
		return false
	}
	for _, zone := range p.ZoneFilter {
		if host == zone || strings.HasSuffix(host, "."+zone) {
			return true
		}
	}
	return false
}

// CredentialsTransferable says whether a stored token may carry forward to an
// edited provider. Name and kind are bound into the credential's additional
// authenticated data, so carrying bytes across a change to either would store
// something that can never be opened again. The zone filter is the boundary
// this feature actually enforces, since the platform cannot see what a token
// reaches, so widening it has to mean re-entering the token. Project and
// environment are neither, which is what lets a scope edit keep the token.
func (p Provider) CredentialsTransferable(next Provider) bool {
	return p.Name == next.Name && p.Kind == next.Kind && slices.Equal(p.ZoneFilter, next.ZoneFilter)
}

func (c Credentials) Validate() error {
	// The token becomes an HTTP header, so a NUL, carriage return or newline
	// would be header injection.
	if c.Token == "" || len(c.Token) > 8192 || strings.ContainsAny(c.Token, "\x00\r\n") {
		return fmt.Errorf("%w: provide a Cloudflare API token", ErrInput)
	}
	return nil
}
