package spec

import (
	"fmt"
	"regexp"
)

func validateExternalDatabaseBinding(app Application, svc Service, b Binding) error {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(b.ExternalDatabase) || b.ExternalDatabaseRevision < 1 || b.ManagedDatabase != "" || b.Service != "" || b.Username != "" || b.Password != nil || b.Database != "" || b.Endpoint != "" || b.SSLMode != "" || b.ClusterAware || b.Protocol != "mysql" && b.Protocol != "postgres" {
		return fmt.Errorf("external database bindings require only a connection ID, reviewed revision and mysql or postgres protocol")
	}
	for _, name := range svc.Networks {
		if network, exists := app.Networks[name]; exists && !network.Internal {
			return nil
		}
	}
	return fmt.Errorf("external database bindings require an explicitly declared network that permits public egress")
}
