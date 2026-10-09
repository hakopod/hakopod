package spec

import "fmt"

const (
	DatabaseClientInfisicalPostgresV1 = "infisical-postgres-v1"
	DatabaseClientNodeExtraCAV1       = "node-extra-ca-v1"
	DatabaseClientLibpqURLV1          = "libpq-postgres-url-v1"
	DatabaseClientGlitchTipValkeyV21  = "glitchtip-valkey-v2.1-v1"
)

func validateDatabaseClientProfiles(app Application, serviceName string, service Service) error {
	if len(service.DatabaseClientProfiles) > 16 {
		return fmt.Errorf("services.%s.database_client_profiles: at most 16 profiles", serviceName)
	}
	effective := EffectiveService(app, service)
	infisicalPostgres := 0
	for variable, profile := range service.DatabaseClientProfiles {
		if !envPattern.MatchString(variable) || len(variable) > 128 {
			return fmt.Errorf("services.%s.database_client_profiles: use valid environment names", serviceName)
		}
		_, environment := effective.Env[variable]
		_, secret := effective.Secrets[variable]
		binding, bound := service.Bindings[variable]
		if !environment && !secret && !bound {
			return fmt.Errorf("services.%s.database_client_profiles.%s: the connection variable is not defined", serviceName, variable)
		}
		switch profile {
		case DatabaseClientInfisicalPostgresV1:
			infisicalPostgres++
			if variable != "DB_CONNECTION_URI" {
				return fmt.Errorf("services.%s.database_client_profiles.%s: the Infisical profile requires DB_CONNECTION_URI", serviceName, variable)
			}
			if _, ok := effective.Env["DB_ROOT_CERT"]; ok {
				return fmt.Errorf("services.%s.database_client_profiles.%s: DB_ROOT_CERT is already defined", serviceName, variable)
			}
			if _, ok := effective.Secrets["DB_ROOT_CERT"]; ok {
				return fmt.Errorf("services.%s.database_client_profiles.%s: DB_ROOT_CERT is already defined", serviceName, variable)
			}
			if _, ok := service.Bindings["DB_ROOT_CERT"]; ok {
				return fmt.Errorf("services.%s.database_client_profiles.%s: DB_ROOT_CERT is already defined", serviceName, variable)
			}
		case DatabaseClientNodeExtraCAV1:
			if _, ok := effective.Env["NODE_EXTRA_CA_CERTS"]; ok {
				return fmt.Errorf("services.%s.database_client_profiles.%s: NODE_EXTRA_CA_CERTS is already defined", serviceName, variable)
			}
			if _, ok := effective.Secrets["NODE_EXTRA_CA_CERTS"]; ok {
				return fmt.Errorf("services.%s.database_client_profiles.%s: NODE_EXTRA_CA_CERTS is already defined", serviceName, variable)
			}
			if _, ok := service.Bindings["NODE_EXTRA_CA_CERTS"]; ok {
				return fmt.Errorf("services.%s.database_client_profiles.%s: NODE_EXTRA_CA_CERTS is already defined", serviceName, variable)
			}
		case DatabaseClientLibpqURLV1, DatabaseClientGlitchTipValkeyV21:
		default:
			return fmt.Errorf("services.%s.database_client_profiles.%s: unsupported profile", serviceName, variable)
		}
		if !bound || binding.ManagedDatabase == "" {
			continue
		}
		switch profile {
		case DatabaseClientInfisicalPostgresV1, DatabaseClientLibpqURLV1:
			if binding.Protocol != "postgres" {
				return fmt.Errorf("services.%s.database_client_profiles.%s: this profile requires PostgreSQL", serviceName, variable)
			}
		case DatabaseClientNodeExtraCAV1:
			if binding.Protocol != "redis" {
				return fmt.Errorf("services.%s.database_client_profiles.%s: this profile requires Redis", serviceName, variable)
			}
		case DatabaseClientGlitchTipValkeyV21:
			return fmt.Errorf("services.%s.database_client_profiles.%s: GlitchTip 6.1 cannot load the private CA for managed Redis", serviceName, variable)
		}
	}
	if infisicalPostgres > 1 {
		return fmt.Errorf("services.%s.database_client_profiles: only one Infisical PostgreSQL profile is supported", serviceName)
	}
	return nil
}

func DatabaseClientProfile(service Service, variable string) string {
	return service.DatabaseClientProfiles[variable]
}
