package spec

import "errors"

// DefaultCustomHostnamesMax applies when custom_hostnames.max is omitted.
const DefaultCustomHostnamesMax = 1000

// CustomHostnamesCeiling bounds any one service whatever the installation allows.
const CustomHostnamesCeiling = 50000

// CustomHostnames opts a public HTTP service into tenant hostnames that are
// added through the API, without a new revision per hostname.
type CustomHostnames struct {
	Enabled bool `json:"enabled" toml:"enabled"`
	// Max caps this service's hostnames; 0 means DefaultCustomHostnamesMax.
	// The installation's max_per_service setting caps it again at acceptance.
	Max int `json:"max,omitempty" toml:"max,omitempty"`
	// Target is the CNAME customers point at. Empty means the service's
	// generated public hostname.
	Target string `json:"target,omitempty" toml:"target,omitempty"`
}

// Limit is the configured per-service maximum, with the default applied.
func (c *CustomHostnames) Limit() int {
	if c == nil || c.Max == 0 {
		return DefaultCustomHostnamesMax
	}
	return c.Max
}

// CustomHostnamesEnabled reports whether a service routes tenant hostnames.
func CustomHostnamesEnabled(svc Service) bool {
	return svc.CustomHostnames != nil && svc.CustomHostnames.Enabled
}

func validateCustomHostnames(service Service) error {
	c := service.CustomHostnames
	if c == nil {
		return nil
	}
	if c.Max < 0 || c.Max > CustomHostnamesCeiling {
		return errors.New("custom_hostnames.max: use 1 to 50000, or omit it for 1000")
	}
	if c.Target != "" && !ValidHostname(c.Target) {
		return errors.New("custom_hostnames.target: use a lowercase DNS hostname without a scheme, port, path or wildcard")
	}
	if service.Job != nil || service.Session != nil || service.Actions != nil || service.Serverless != nil {
		return errors.New("custom_hostnames: jobs, serverless services, actions runners and sandbox sessions cannot route tenant hostnames")
	}
	if !service.Public || service.Port == 0 {
		return errors.New("custom_hostnames: requires a public service with a port")
	}
	return nil
}
