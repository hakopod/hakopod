package spec

import (
	"fmt"
	"net"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

func ValidHostname(host string) bool {
	if len(host) > 244 || len(host) == 0 || host != strings.ToLower(host) || !strings.Contains(host, ".") || net.ParseIP(host) != nil || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) > 63 {
			return false
		}
	}
	return true
}
func ValidateDomains(a Application) error {
	if len(a.Domains) > 20 {
		return fmt.Errorf("domains: at most 20 custom hostnames are supported")
	}
	for host, name := range a.Domains {
		if !ValidHostname(host) {
			return fmt.Errorf("domains: use lowercase DNS hostnames without a scheme, port, path or wildcard")
		}
		service, ok := a.Services[name]
		if !ok || !service.Public || service.Port == 0 {
			return fmt.Errorf("domains.%s: select a public HTTP service", host)
		}
	}
	return nil
}
