package managedplatform

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
)

var neonSettingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
var neonConfLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.]*)[\t ]*=[\t ]*('([^'\\]|\\.|'')*'|[A-Za-z0-9_./+:-]+)[\t ]*(#.*)?$`)

// BindNeonComputeRuntime owns the listener, transport and WAL role. Caller
// settings remain available only where they cannot replace those bindings.
func BindNeonComputeRuntime(raw json.RawMessage, tenantID, timelineID string, safekeepers []string, replica bool) (json.RawMessage, error) {
	var root map[string]any
	if len(raw) > maxNeonResponseBytes || decodeNeonJSON(raw, &root) != nil {
		return nil, fmt.Errorf("decode Neon compute configuration")
	}
	spec, ok := root["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Neon compute configuration requires a spec object")
	}
	spec["tenant_id"], spec["timeline_id"], spec["mode"] = tenantID, timelineID, "Primary"
	if replica {
		spec["mode"] = "Replica"
	}
	features, ok := spec["features"].([]any)
	if !ok && spec["features"] != nil {
		return nil, fmt.Errorf("Neon compute features must be a list")
	}
	if len(features) > 32 {
		return nil, fmt.Errorf("Neon compute features exceed their bound")
	}
	boundFeatures := make([]any, 0, len(features)+1)
	for _, value := range features {
		feature, ok := value.(string)
		if !ok || feature == "" || len(feature) > 128 || strings.ContainsAny(feature, "\x00\r\n") {
			return nil, fmt.Errorf("Neon compute feature is invalid")
		}
		if feature != "tls_experimental" {
			boundFeatures = append(boundFeatures, feature)
		}
	}
	spec["features"] = append(boundFeatures, "tls_experimental")
	cluster, ok := spec["cluster"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Neon compute configuration requires a cluster object")
	}
	if value := cluster["postgresql_conf"]; value != nil {
		conf, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("Neon PostgreSQL configuration must be text")
		}
		var retained []string
		for _, line := range strings.Split(conf, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			match := neonConfLine.FindStringSubmatch(line)
			if match == nil || strings.ContainsAny(line, "\x00\r") || strings.HasPrefix(strings.ToLower(match[1]), "include") {
				return nil, fmt.Errorf("Neon PostgreSQL configuration requires single-line assignments without includes")
			}
			if !neonOwnedComputeSetting(match[1]) {
				retained = append(retained, line)
			}
		}
		cluster["postgresql_conf"] = strings.Join(retained, "\n")
	}
	settings, ok := cluster["settings"].([]any)
	if !ok && cluster["settings"] != nil || len(settings) > 256 {
		return nil, fmt.Errorf("Neon compute settings must be a bounded list")
	}
	bound := make([]any, 0, len(settings)+24)
	for _, value := range settings {
		setting, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Neon compute setting must be an object")
		}
		name, ok := setting["name"].(string)
		if !ok || !neonSettingName.MatchString(name) || strings.HasPrefix(strings.ToLower(name), "include") {
			return nil, fmt.Errorf("Neon compute setting requires a valid name without includes")
		}
		if neonOwnedComputeSetting(name) {
			continue
		}
		text, ok := setting["value"].(string)
		if !ok || len(text) > 8192 || strings.ContainsAny(text, "\x00\r\n") {
			return nil, fmt.Errorf("Neon compute setting requires a bounded single-line value")
		}
		// PostgreSQL accepts quoted values for every GUC type. Quoting every
		// caller value prevents an arbitrary vartype from injecting config lines.
		bound = append(bound, neonComputeSetting(name, text))
	}
	for _, setting := range [][2]string{
		{"max_connections", "64"}, {"superuser_reserved_connections", "4"}, {"reserved_connections", "0"},
		{"port", "55433"}, {"listen_addresses", "*"}, {"shared_preload_libraries", "neon"},
		{"ssl", "on"}, {"ssl_cert_file", "server.crt"}, {"ssl_key_file", "server.key"}, {"ssl_min_protocol_version", "TLSv1.2"},
		{"hba_file", "/etc/hakopod-postgres/pg_hba.conf"}, {"wal_level", "logical"}, {"max_wal_senders", "10"}, {"max_replication_slots", "10"},
		{"hot_standby", "on"}, {"synchronous_commit", "on"}, {"wal_sender_timeout", "5s"}, {"wal_keep_size", "0"}, {"restart_after_crash", "off"},
	} {
		bound = append(bound, neonComputeSetting(setting[0], setting[1]))
	}
	cluster["settings"] = bound
	if err := bindNeonWALRouting(spec, safekeepers); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func neonComputeSetting(name, value string) map[string]any {
	return map[string]any{"name": name, "value": value, "vartype": "string"}
}

func neonOwnedComputeSetting(name string) bool {
	name = strings.ToLower(name)
	if name == "ssl" || strings.HasPrefix(name, "ssl_") || strings.HasPrefix(name, "recovery_target") {
		return true
	}
	switch name {
	case "max_connections", "superuser_reserved_connections", "reserved_connections", "port", "listen_addresses", "shared_preload_libraries", "hba_file", "config_file", "data_directory", "wal_level", "max_wal_senders", "max_replication_slots", "hot_standby", "synchronous_commit", "synchronous_standby_names", "wal_sender_timeout", "wal_keep_size", "restart_after_crash", "primary_conninfo", "primary_slot_name", "recovery_prefetch", "restore_command", "archive_command", "archive_library", "neon.compute_mode", "neon.tenant_id", "neon.timeline_id", "neon.pageserver_connstring", "neon.safekeepers", "neon.stripe_size", "neon.safekeepers_generation", "neon.safekeeper_conninfo_options", "neon.privileged_role_name":
		return true
	}
	return false
}

func bindNeonWALRouting(spec map[string]any, safekeepers []string) error {
	cluster, ok := spec["cluster"].(map[string]any)
	if !ok {
		return fmt.Errorf("Neon compute cluster configuration is missing")
	}
	settings, ok := cluster["settings"].([]any)
	if !ok && cluster["settings"] != nil {
		return fmt.Errorf("Neon compute settings are invalid")
	}
	bound := make([]any, 0, len(settings)+5)
	for _, value := range settings {
		setting, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("Neon compute setting is invalid")
		}
		name, _ := setting["name"].(string)
		switch strings.ToLower(name) {
		case "neon.pageserver_connstring", "neon.safekeepers", "neon.stripe_size", "neon.safekeepers_generation", "neon.safekeeper_conninfo_options", "primary_conninfo", "primary_slot_name", "recovery_prefetch", "synchronous_standby_names":
			continue
		}
		bound = append(bound, setting)
	}
	spec["safekeeper_connstrings"] = append([]string{}, safekeepers...)
	if spec["mode"] == "Replica" {
		// A nonempty neon.safekeepers starts walproposer and promotes recovery.
		// Replica WAL therefore uses PostgreSQL's ordinary WAL receiver.
		spec["safekeeper_connstrings"] = []string{}
		conninfo, err := neonReplicaConninfo(spec, safekeepers)
		if err != nil {
			return err
		}
		bound = append(bound, neonComputeSetting("primary_conninfo", conninfo), neonComputeSetting("primary_slot_name", "repl_"+spec["timeline_id"].(string)+"_"), neonComputeSetting("recovery_prefetch", "off"), neonComputeSetting("synchronous_standby_names", ""))
	} else {
		bound = append(bound, neonComputeSetting("synchronous_standby_names", "walproposer"))
	}
	bound = append(bound, neonComputeSetting("neon.safekeeper_conninfo_options", "sslmode=verify-full sslrootcert=/var/run/secrets/hakopod/safekeeper-auth/ca.crt"))
	cluster["settings"] = bound
	return nil
}

func neonReplicaConninfo(spec map[string]any, safekeepers []string) (string, error) {
	tenant, tenantOK := spec["tenant_id"].(string)
	timeline, timelineOK := spec["timeline_id"].(string)
	if !tenantOK || !timelineOK || !neonID.MatchString(tenant) || !neonID.MatchString(timeline) || len(safekeepers) != 3 {
		return "", fmt.Errorf("Neon replica storage identity is invalid")
	}
	hosts, ports := make([]string, len(safekeepers)), make([]string, len(safekeepers))
	seen := map[string]bool{}
	for i, address := range safekeepers {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n,'\\=") || port != "5454" || seen[host] {
			return "", fmt.Errorf("Neon replica storage address is invalid")
		}
		seen[host] = true
		hosts[i], ports[i] = host, port
	}
	// The provider passes storage_auth_token as PGPASSWORD to the PostgreSQL
	// child. Keep credentials out of settings, diagnostics and routing digests.
	return "host=" + strings.Join(hosts, ",") + " port=" + strings.Join(ports, ",") + " options='-c timeline_id=" + timeline + " tenant_id=" + tenant + "' application_name=replica replication=true sslmode=verify-full sslrootcert=/var/run/secrets/hakopod/safekeeper-auth/ca.crt connect_timeout=5", nil
}

func validateNeonReplicaWALSettings(raw json.RawMessage) error {
	var root map[string]any
	if decodeNeonJSON(raw, &root) != nil {
		return fmt.Errorf("invalid Neon replica configuration")
	}
	spec, _ := root["spec"].(map[string]any)
	cluster, _ := spec["cluster"].(map[string]any)
	settings, _ := cluster["settings"].([]any)
	values := map[string]string{}
	for _, value := range settings {
		setting, _ := value.(map[string]any)
		name, _ := setting["name"].(string)
		text, _ := setting["value"].(string)
		if _, duplicate := values[name]; duplicate {
			return fmt.Errorf("duplicate Neon replica setting")
		}
		values[name] = text
	}
	timeline, _ := spec["timeline_id"].(string)
	if values["primary_slot_name"] != "repl_"+timeline+"_" || values["hot_standby"] != "on" || values["recovery_prefetch"] != "off" || values["synchronous_standby_names"] != "" || values["neon.safekeepers"] != "" {
		return fmt.Errorf("Neon replica WAL settings are invalid")
	}
	conninfo := values["primary_conninfo"]
	head, _, found := strings.Cut(conninfo, " ")
	if !found || !strings.HasPrefix(head, "host=") {
		return fmt.Errorf("Neon replica WAL connection is invalid")
	}
	hosts := strings.Split(strings.TrimPrefix(head, "host="), ",")
	for i := range hosts {
		hosts[i] = net.JoinHostPort(hosts[i], "5454")
	}
	expected, err := neonReplicaConninfo(spec, hosts)
	if err != nil || conninfo != expected {
		return fmt.Errorf("Neon replica WAL connection is invalid")
	}
	return nil
}
