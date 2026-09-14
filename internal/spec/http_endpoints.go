package spec

import "fmt"

// HTTPEndpoint exposes an additional declared TCP port through HTTP ingress.
// Domain, when supplied, must also be assigned to this service in domains.
type HTTPEndpoint struct {
	Port   int32  `json:"port" toml:"port"`
	Domain string `json:"domain,omitempty" toml:"domain"`
}

func validateHTTPEndpoints(app Application) error {
	generated := map[string]bool{}
	for name := range app.Services {
		generated[name] = true
	}
	for name, s := range app.Services {
		if len(s.HTTP) > 4 {
			return fmt.Errorf("services.%s.http: at most four additional HTTP endpoints", name)
		}
		seen := map[string]bool{}
		for key, e := range s.HTTP {
			if !s.Public || s.Job != nil || !namePattern.MatchString(key) || len(key) > 8 {
				return fmt.Errorf("services.%s.http: endpoint names must be 1–8 lowercase letters, digits or hyphens on a public HTTP service", name)
			}
			if generated[name+"-"+key] {
				return fmt.Errorf("HTTP endpoint hostname conflicts with another service or endpoint")
			}
			generated[name+"-"+key] = true
			found := false
			for _, p := range ServicePorts(s) {
				if p.Port == e.Port && p.Protocol == "TCP" {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("services.%s.http.%s.port: select a declared TCP service port", name, key)
			}
			if e.Domain != "" {
				if !ValidHostname(e.Domain) || app.Domains[e.Domain] != name || seen[e.Domain] {
					return fmt.Errorf("services.%s.http.%s.domain: use a unique custom domain assigned to this service", name, key)
				}
				seen[e.Domain] = true
			}
		}
	}
	return nil
}
