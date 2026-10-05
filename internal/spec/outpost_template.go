package spec

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Dependencies are independent choices. Remove only the selected bundled
// services and replace their bindings with application-scoped provider secrets.
func configureOutpostTemplate(app *Application, values map[string]string) error {
	redisHost := values["redis-host"]
	redisPort, redisDatabase := 0, 0
	if values["redis-mode"] == "external" {
		if !validTemplateConnectionHost(redisHost) {
			return fmt.Errorf("redis-host: use a hostname or IP address without a scheme, port or credentials")
		}
		// Outpost v1.6.0 concatenates the host and port in its Redis client.
		if ip := net.ParseIP(redisHost); ip != nil && ip.To4() == nil {
			redisHost = "[" + redisHost + "]"
		}
		var err error
		redisPort, err = strconv.Atoi(values["redis-port"])
		if err != nil || redisPort < 1 || redisPort > 65535 {
			return fmt.Errorf("redis-port: use a port between 1 and 65535")
		}
		redisDatabase, err = strconv.Atoi(values["redis-database"])
		if err != nil || redisDatabase < 0 || redisDatabase > 15 {
			return fmt.Errorf("redis-database: use a database number between 0 and 15")
		}
		if user := values["redis-username"]; len(user) > 256 || strings.TrimSpace(user) != user {
			return fmt.Errorf("redis-username: use up to 256 characters without surrounding whitespace")
		}
	}
	for _, name := range []string{"main", "delivery", "log", "migrate"} {
		service := app.Services[name]
		if values["database-mode"] == "external" {
			delete(service.Env, "PGSSLMODE")
			delete(service.Bindings, "POSTGRES_URL")
			service.Secrets["POSTGRES_URL"] = SecretRef{Ref: "database-url"}
			service.DependsOn = slices.DeleteFunc(service.DependsOn, func(dep string) bool { return dep == "db" })
		}
		if values["broker-mode"] == "external" {
			if _, hasBroker := service.Bindings["RABBITMQ_SERVER_URL"]; hasBroker {
				delete(service.Bindings, "RABBITMQ_SERVER_URL")
				service.Secrets["RABBITMQ_SERVER_URL"] = SecretRef{Ref: "broker-url"}
			}
			service.DependsOn = slices.DeleteFunc(service.DependsOn, func(dep string) bool { return dep == "broker" })
		}
		if values["redis-mode"] == "external" {
			service.Env["REDIS_HOST"] = redisHost
			service.Env["REDIS_PORT"] = strconv.Itoa(redisPort)
			service.Env["REDIS_USERNAME"] = values["redis-username"]
			service.Env["REDIS_DATABASE"] = strconv.Itoa(redisDatabase)
			service.Env["REDIS_TLS_ENABLED"] = values["redis-tls"]
			service.Env["REDIS_TLS_VERIFY"] = "true"
			service.DependsOn = slices.DeleteFunc(service.DependsOn, func(dep string) bool { return dep == "redis" })
		}
		app.Services[name] = service
	}
	for mode, service := range map[string]string{"database-mode": "db", "redis-mode": "redis", "broker-mode": "broker"} {
		if values[mode] == "external" {
			delete(app.Services, service)
		}
	}
	return nil
}
