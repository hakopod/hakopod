// Package externaldatabase connects to provider-owned databases. Hakopod owns
// connection configuration and application grants, not the provider's data.
package externaldatabase

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const MaxDatabases = 64
const ObservationMaxAge = 2 * time.Minute
const ProbeTimeout = 10 * time.Second

var ErrUnavailable = errors.New("external database connection could not be verified; check the endpoint, credentials, provider access rules and TLS trust")
var ErrNewChangesUnavailable = errors.New("PlanetScale connections are unavailable for creation, endpoint changes or new bindings; existing connections can be inspected, have credentials rotated, disconnected and removed")
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Spec struct {
	SchemaVersion int    `json:"schema_version" toml:"schema_version"`
	Name          string `json:"name" toml:"name"`
	Provider      string `json:"provider" toml:"provider"`
	Engine        string `json:"engine" toml:"engine"`
	Host          string `json:"host" toml:"host"`
	Port          int    `json:"port" toml:"port"`
	Database      string `json:"database" toml:"database"`
}

func (s Spec) Validate() error {
	if s.SchemaVersion != 1 || !namePattern.MatchString(s.Name) {
		return fmt.Errorf("use schema_version 1 and a name containing 1–40 lowercase letters, digits or hyphens")
	}
	if s.Provider != "planetscale" || s.Engine != "mysql" && s.Engine != "postgresql" {
		return fmt.Errorf("choose a PlanetScale MySQL or PostgreSQL connection")
	}
	if len(s.Host) > 253 || s.Host != strings.ToLower(s.Host) || strings.HasSuffix(s.Host, ".") || !(strings.HasSuffix(s.Host, ".psdb.cloud") || strings.HasSuffix(s.Host, ".psdb.io")) {
		return fmt.Errorf("use the provider-issued psdb.cloud or psdb.io hostname")
	}
	for _, label := range strings.Split(s.Host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("provider hostname is invalid")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("provider hostname is invalid")
			}
		}
	}
	if s.Engine == "mysql" && s.Port != 3306 || s.Engine == "postgresql" && s.Port != 5432 && s.Port != 6432 {
		return fmt.Errorf("use MySQL port 3306 or PostgreSQL port 5432 or 6432")
	}
	if len(s.Database) == 0 || len(s.Database) > 64 || strings.ContainsAny(s.Database, "/\\?#\x00\r\n") {
		return fmt.Errorf("provide a database name containing 1–64 characters without URL separators")
	}
	return nil
}

func (s Spec) SameEndpoint(other Spec) bool {
	return s.Provider == other.Provider && s.Engine == other.Engine && s.Host == other.Host && s.Port == other.Port && s.Database == other.Database
}

func Parse(data []byte) (Spec, error) {
	var s Spec
	if len(data) == 0 || len(data) > 16<<10 {
		return s, fmt.Errorf("external database TOML must be between 1 byte and 16 KiB")
	}
	if toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&s) != nil {
		return s, fmt.Errorf("external database TOML contains invalid or unknown fields")
	}
	return s, s.Validate()
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c Credentials) Validate() error {
	if len(c.Username) == 0 || len(c.Username) > 256 || strings.ContainsAny(c.Username, "\x00\r\n") || len(c.Password) == 0 || len(c.Password) > 4096 || strings.ContainsAny(c.Password, "\x00\r\n") {
		return fmt.Errorf("provide a username of at most 256 characters and a password of at most 4096 characters")
	}
	return nil
}

// CredentialAAD prevents moving an encrypted secret to another connection or
// forwarding it to a changed endpoint without an explicit credential update.
func credentialAAD(id string, s Spec) []byte {
	return []byte("hakopod-external-database-v1:" + id + ":" + s.Provider + ":" + s.Engine + ":" + net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) + ":" + s.Database)
}

func SealCredentials(key []byte, id string, s Spec, c Credentials) ([]byte, error) {
	if len(key) != 32 || !idPattern.MatchString(id) || s.Validate() != nil || c.Validate() != nil {
		return nil, ErrUnavailable
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	gcm, err := cipher.NewGCM(b)
	if err != nil {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, ErrUnavailable
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return nil, ErrUnavailable
	}
	return gcm.Seal(nonce, nonce, plain, credentialAAD(id, s)), nil
}

func OpenCredentials(key []byte, r Resource) (Credentials, error) {
	var c Credentials
	if len(key) != 32 || !idPattern.MatchString(r.ID) {
		return c, ErrUnavailable
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return c, ErrUnavailable
	}
	gcm, err := cipher.NewGCM(b)
	if err != nil || len(r.EncryptedCredentials) < gcm.NonceSize()+gcm.Overhead() || len(r.EncryptedCredentials) > 8192 {
		return c, ErrUnavailable
	}
	plain, err := gcm.Open(nil, r.EncryptedCredentials[:gcm.NonceSize()], r.EncryptedCredentials[gcm.NonceSize():], credentialAAD(r.ID, r.Spec))
	if err != nil || json.Unmarshal(plain, &c) != nil || c.Validate() != nil {
		return Credentials{}, ErrUnavailable
	}
	return c, nil
}

// ConnectionURL uses the system CA bundle. MySQL clients must interpret the
// explicit VERIFY_IDENTITY option; drivers without URL TLS support need the
// equivalent native TLS options described in the connection guide.
func ConnectionURL(s Spec, c Credentials) (string, error) {
	if s.Validate() != nil || c.Validate() != nil {
		return "", ErrUnavailable
	}
	scheme := "mysql"
	if s.Engine == "postgresql" {
		scheme = "postgresql"
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), Path: "/" + s.Database, User: url.UserPassword(c.Username, c.Password)}
	q := u.Query()
	if s.Engine == "postgresql" {
		q.Set("sslmode", "verify-full")
		q.Set("sslrootcert", "system")
	} else {
		q.Set("ssl-mode", "VERIFY_IDENTITY")
		q.Set("tls", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type Observation struct {
	VerifiedIPs   []string  `json:"verified_ips,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
	Revision      int64     `json:"revision"`
	Status        string    `json:"status"`
	Message       string    `json:"message"`
	TLSVerified   bool      `json:"tls_verified"`
	QueryVerified bool      `json:"query_verified"`
	LatencyMS     *int64    `json:"latency_ms,omitempty"`
}

type Resource struct {
	ID                   string      `json:"id"`
	Project              string      `json:"project"`
	Environment          string      `json:"environment"`
	Revision             int64       `json:"revision"`
	CredentialRevision   int64       `json:"credential_revision"`
	Spec                 Spec        `json:"spec"`
	Status               string      `json:"status"`
	Observation          Observation `json:"observation"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
	DeletedAt            *time.Time  `json:"deleted_at,omitempty"`
	EncryptedCredentials []byte      `json:"-"`
	CredentialDigest     []byte      `json:"-"`
}

func (r Resource) Verified(now time.Time) bool {
	o := r.Observation
	return r.DeletedAt == nil && r.Status == "ready" && o.Status == "ready" && o.Revision == r.Revision && o.TLSVerified && o.QueryVerified && !o.ObservedAt.After(now.Add(5*time.Second)) && now.Sub(o.ObservedAt) <= ObservationMaxAge
}

type Operation struct {
	ID         string     `json:"id"`
	DatabaseID string     `json:"database_id"`
	Revision   int64      `json:"revision"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Message    string     `json:"message"`
	Spec       Spec       `json:"spec"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Lease      string     `json:"-"`
}
