package spec

import (
	"fmt"
	"maps"
	"reflect"
)

// NameTemplateService makes single-service catalog items usable alongside an
// existing template's "main" service. Multi-service templates retain explicit
// upstream names because configuration files may refer to those names.
func NameTemplateService(app Application, name string) (Application, error) {
	if name == "" {
		return app, nil
	}
	if !namePattern.MatchString(name) {
		return Application{}, fmt.Errorf("service_name: use 1–40 lowercase letters, digits or hyphens")
	}
	if len(app.Services) != 1 {
		return Application{}, fmt.Errorf("service_name: multi-service templates keep their original names")
	}
	next, err := Normalize(app)
	if err != nil {
		return next, err
	}
	old := Names(next)[0]
	svc := next.Services[old]
	delete(next.Services, old)
	next.Services[name] = svc
	for host, target := range next.Domains {
		if target == old {
			next.Domains[host] = name
		}
	}
	return Normalize(next)
}

// AddServices preserves the destination's shared settings and rejects ambiguous
// names. Template defaults become explicit on the added services only.
func AddServices(base, addition Application) (Application, error) {
	next, err := Normalize(base)
	if err != nil {
		return Application{}, err
	}
	addition, err = Normalize(addition)
	if err != nil {
		return Application{}, err
	}
	for _, name := range Names(addition) {
		if _, exists := next.Services[name]; exists {
			return Application{}, fmt.Errorf("service %s already exists; choose a different service name or application", name)
		}
		next.Services[name] = EffectiveService(addition, addition.Services[name])
	}
	for name, value := range addition.Networks {
		if old, exists := next.Networks[name]; exists && old != value {
			return Application{}, fmt.Errorf("network %s has different settings in this application", name)
		}
		next.Networks[name] = value
	}
	if next.Volumes == nil {
		next.Volumes = map[string]NamedVolume{}
	}
	for name, value := range addition.Volumes {
		if _, exists := next.Volumes[name]; exists {
			return Application{}, fmt.Errorf("volume %s already exists; existing data cannot be reused implicitly", name)
		}
		next.Volumes[name] = value
	}
	if next.Domains == nil {
		next.Domains = map[string]string{}
	}
	for name, value := range addition.Domains {
		if _, exists := next.Domains[name]; exists {
			return Application{}, fmt.Errorf("domain %s already exists", name)
		}
		next.Domains[name] = value
	}
	return Normalize(next)
}

// MoveService plans the two revisions without mutating either input. Data and
// application-bound resources require an explicit migration, never a fresh PVC.
func MoveService(source, destination Application, name, targetName string) (Application, Application, error) {
	source, err := Normalize(source)
	if err != nil {
		return Application{}, Application{}, err
	}
	destination, err = Normalize(destination)
	if err != nil {
		return Application{}, Application{}, err
	}
	svc, ok := source.Services[name]
	if !ok {
		return Application{}, Application{}, fmt.Errorf("service %s is absent from this application", name)
	}
	if !namePattern.MatchString(targetName) {
		return Application{}, Application{}, fmt.Errorf("destination service name: use 1–40 lowercase letters, digits or hyphens")
	}
	if _, exists := destination.Services[targetName]; exists {
		return Application{}, Application{}, fmt.Errorf("service %s already exists in the destination; choose another name", targetName)
	}
	if svc.Volume != nil || len(svc.Mounts) > 0 {
		return Application{}, Application{}, fmt.Errorf("this service has persistent data; back up and restore its volumes in the destination before migrating it")
	}
	if svc.Job != nil {
		return Application{}, Application{}, fmt.Errorf("jobs need an explicit schedule or execution handover to avoid running twice; create the job in the destination after disabling the original")
	}
	if svc.TLS != nil || len(svc.CertificateMounts) > 0 || svc.AWSIdentity != "" || len(svc.PublicTCP) > 0 {
		return Application{}, Application{}, fmt.Errorf("this service has application-bound certificates, identity or public TCP; migrate those settings explicitly before moving")
	}
	for _, service := range source.Domains {
		if service == name {
			return Application{}, Application{}, fmt.Errorf("this service has custom domains; plan the domain cutover before moving it")
		}
	}
	if len(svc.DependsOn) > 0 || len(svc.Bindings) > 0 || svc.NetworkAccess != nil {
		return Application{}, Application{}, fmt.Errorf("this service has dependencies, bindings or network rules; update those connections before moving it")
	}
	for _, peer := range source.Services {
		for _, dependency := range peer.DependsOn {
			if dependency == name {
				return Application{}, Application{}, fmt.Errorf("another service depends on %s; update that dependency before moving", name)
			}
		}
		for _, binding := range peer.Bindings {
			if binding.Service == name {
				return Application{}, Application{}, fmt.Errorf("another service has a binding to %s; update that binding before moving", name)
			}
		}
		if peer.NetworkAccess != nil {
			for _, from := range peer.NetworkAccess.From {
				if from == name {
					return Application{}, Application{}, fmt.Errorf("another service has a network rule for %s; update that rule before moving", name)
				}
			}
		}
	}
	svc = EffectiveService(source, svc)
	for _, ref := range SecretReferences(svc) {
		if ref.Provider != "" {
			return Application{}, Application{}, fmt.Errorf("external secret providers are application-scoped; configure their destination bindings before migrating this service")
		}
	}
	for _, network := range svc.Networks {
		value := source.Networks[network]
		if value.VirtualNetwork != "" {
			return Application{}, Application{}, fmt.Errorf("shared virtual-network membership needs an explicit migration")
		}
		if old, exists := destination.Networks[network]; exists && !reflect.DeepEqual(old, value) {
			return Application{}, Application{}, fmt.Errorf("network %s differs in the destination", network)
		}
		destination.Networks[network] = value
	}
	// Freeze inherited values as service overrides. New destination defaults are
	// still inherited, as they are for any service added to that application.
	svc.Env = maps.Clone(svc.Env)
	destination.Services[targetName] = svc
	delete(source.Services, name)
	source, err = Normalize(source)
	if err != nil {
		return Application{}, Application{}, err
	}
	destination, err = Normalize(destination)
	return source, destination, err
}
