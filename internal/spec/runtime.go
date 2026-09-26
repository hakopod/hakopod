package spec

import (
	"errors"
	"regexp"
)

// TLS references managed Kubernetes resources, never certificate private keys.
type TLSConfig struct {
	Certificate string `json:"certificate,omitempty" toml:"certificate"`
	Issuer      string `json:"issuer,omitempty" toml:"issuer"`
}

var runtimeName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var noncePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateRuntimeService(service Service) error {
	if service.RestartNonce != "" && !noncePattern.MatchString(service.RestartNonce) {
		return errors.New("restart_nonce: use at most 64 letters, numbers, underscores or hyphens")
	}
	if service.RegistryCredential != "" && !runtimeName.MatchString(service.RegistryCredential) {
		return errors.New("registry_credential: use a managed credential name of at most 63 lowercase letters, digits or hyphens")
	}
	if tls := service.TLS; tls != nil {
		if !service.Public {
			return errors.New("tls: requires a public service")
		}
		if (tls.Certificate == "") == (tls.Issuer == "") {
			return errors.New("tls: choose exactly one managed uploaded certificate or cert-manager issuer")
		}
		if tls.Certificate != "" && !runtimeName.MatchString(tls.Certificate) || tls.Issuer != "" && !runtimeName.MatchString(tls.Issuer) {
			return errors.New("tls: resource names must use at most 63 lowercase letters, digits or hyphens")
		}
	}
	if service.BackendHTTP2 {
		if !service.Public || service.Port == 0 {
			return errors.New("backend_http2: requires a public service with a port")
		}
		if service.Serverless != nil {
			return errors.New("backend_http2: serverless services are reached through an HTTP/1.1 gateway")
		}
		if len(service.HTTP) > 0 {
			return errors.New("backend_http2: applies to the whole service, so it cannot be combined with named http endpoints")
		}
	}
	return nil
}
