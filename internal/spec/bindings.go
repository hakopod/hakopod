package spec

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Binding derives a private service URL at deployment time. Only references
// enter revisions; resolved credentials remain in application-scoped Secrets.
type Binding struct {
	Service  string     `json:"service" toml:"service"`
	Protocol string     `json:"protocol" toml:"protocol"`
	Database string     `json:"database,omitempty" toml:"database"`
	Username string     `json:"username,omitempty" toml:"username"`
	Password *SecretRef `json:"password,omitempty" toml:"password"`
}

func BindingSecretKey(name string) string { return "__binding_" + name }

func validateBindings(app Application) error {
	for name, s := range app.Services {
		if len(s.Bindings) > 16 {
			return fmt.Errorf("services.%s.bindings: at most 16 bindings", name)
		}
		for key, b := range s.Bindings {
			if !envPattern.MatchString(key) || len(key) > 128 {
				return fmt.Errorf("services.%s.bindings: use valid environment names", name)
			}
			if _, ok := s.Env[key]; ok {
				return fmt.Errorf("services.%s.bindings.%s: environment name is already defined", name, key)
			}
			if _, ok := s.Secrets[key]; ok {
				return fmt.Errorf("services.%s.bindings.%s: secret environment name is already defined", name, key)
			}
			if _, ok := s.Secrets[BindingSecretKey(key)]; ok {
				return fmt.Errorf("services.%s.bindings: reserved secret key collision", name)
			}
			target, ok := app.Services[b.Service]
			if !ok || target.Job != nil || target.Port == 0 || !AllowsPeer(app, name, b.Service) {
				return fmt.Errorf("services.%s.bindings.%s: target must be a reachable private service with a primary port", name, key)
			}
			if len(b.Database) > 64 || strings.ContainsAny(b.Database, "/\\?#\x00\r\n") || len(b.Username) > 64 || strings.ContainsAny(b.Username, "\x00\r\n") {
				return fmt.Errorf("services.%s.bindings.%s: invalid database or username", name, key)
			}
			switch b.Protocol {
			case "http":
				if b.Password != nil || b.Username != "" || b.Database != "" {
					return fmt.Errorf("http bindings cannot contain database credentials")
				}
			case "postgres", "mysql", "redis":
				if b.Password == nil || !b.Password.Valid() {
					return fmt.Errorf("services.%s.bindings.%s: a scoped password reference is required", name, key)
				}
				if b.Protocol != "redis" && (b.Database == "" || b.Username == "") {
					return fmt.Errorf("database bindings require database and username")
				}
				if b.Protocol == "redis" && b.Database != "" {
					n, err := strconv.Atoi(b.Database)
					if err != nil || n < 0 || n > 15 {
						return fmt.Errorf("redis binding database must be 0–15")
					}
				}
			default:
				return fmt.Errorf("services.%s.bindings.%s.protocol: choose http, postgres, mysql or redis", name, key)
			}
		}
	}
	return nil
}

func BindingURL(b Binding, target Service, password []byte) string {
	u := url.URL{Scheme: b.Protocol, Host: net.JoinHostPort(b.Service, strconv.Itoa(int(target.Port)))}
	if b.Database != "" {
		u.Path = "/" + b.Database
	}
	if b.Password != nil {
		u.User = url.UserPassword(b.Username, string(password))
	}
	return u.String()
}
