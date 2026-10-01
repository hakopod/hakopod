package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func vitessTestDatabase() database.Resource {
	return database.Resource{ID: strings.Repeat("a", 32), Revision: 1, Project: "test", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "test", Engine: "vitess", Version: "23", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "1Gi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}, Vitess: &database.VitessConfig{BackupDestinationID: strings.Repeat("c", 32), BackupDestinationRevision: 1}}}
}

func TestVitessScopedCredentials(t *testing.T) {
	d := vitessTestDatabase()
	data, err := vitessAccountData(d.Spec, []byte(strings.Repeat("p", 32)), []byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string][]struct {
		Password string
		UserData string
	}
	if err = json.Unmarshal(data["users.json"], &auth); err != nil || len(auth) != 1 || len(auth["app"]) != 1 || auth["app"][0].UserData != "app" {
		t.Fatal("invalid gateway authentication")
	}
	if strings.Contains(string(data["users.json"]), "vt_dba") || strings.Contains(string(data["users.json"]), strings.Repeat("a", 32)) {
		t.Fatal("internal credentials exposed to gateway users")
	}
	init := string(data["init.sql"])
	if !strings.Contains(init, "GRANT ALL ON app.* TO 'vt_app'@'localhost'") || strings.Contains(init, "GRANT ALL ON *.* TO 'vt_app'") || !strings.Contains(init, "REQUIRE SSL") {
		t.Fatal("application or replication grants exceed their boundary")
	}
	if !strings.Contains(init, "GRANT ALL ON _vt.* TO 'vt_allprivs'@'localhost'") || strings.Contains(init, "GRANT ALL ON *.* TO 'vt_allprivs'") || strings.Contains(init, "GRANT ALL ON _vt.* TO 'vt_app'") {
		t.Fatal("Vitess metadata access must stay with the local internal account")
	}
}

func TestVitessReplicationCredentialMatchesNativeLimit(t *testing.T) {
	d := vitessTestDatabase()
	for _, password := range []string{strings.Repeat("a", 64), strings.Repeat("a", 31), strings.Repeat("z", 32)} {
		if _, err := vitessAccountData(d.Spec, []byte(strings.Repeat("p", 32)), []byte(password)); err == nil {
			t.Fatal("Vitess accepted an invalid native replication credential")
		}
	}
}

func TestVitessPolicyLimitsClientsToGateway(t *testing.T) {
	d := vitessTestDatabase()
	policies := vitessNetworkPolicies(d, "namespace-uid", nil, Options{})
	if len(policies) != 4 || len(policies[2].Spec.Ingress) != 1 || policies[2].Spec.PodSelector.MatchLabels[vitessComponentLabel] != "gateway" {
		t.Fatal("client policy does not select only gateways")
	}
	ports := policies[2].Spec.Ingress[0].Ports
	if len(ports) != 1 || ports[0].Port.IntVal != 3306 {
		t.Fatal("client policy exposes internal ports")
	}
	d.Status = "restoring"
	policies = vitessNetworkPolicies(d, "namespace-uid", nil, Options{})
	if len(policies[2].Spec.Ingress) != 0 {
		t.Fatal("restoring database permits application ingress")
	}
	for _, raw := range vitessRoleRules() {
		rule := raw.(map[string]any)
		for _, resource := range rule["resources"].([]any) {
			if resource == "*" || resource == "namespaces" || resource == "clusterroles" || resource == "pods/exec" {
				t.Fatal("operator has unnecessary cluster or exec permissions")
			}
		}
	}
}

func TestVitessIncompleteRuntimeFailsClosed(t *testing.T) {
	d := vitessTestDatabase()
	if vitessRuntimeSupported(d.Spec) == nil {
		t.Fatal("unaccepted Vitess runtime became available")
	}
	object := vitessDatabaseSpec(d, vitessResources(d.Spec.CPU, d.Spec.Memory))
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"client-cert-auth", "peer-client-cert-auth", "require_secure_transport=ON", "queryserver-config-strict-table-acl", "verify_identity", "@sha256:"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("candidate omitted %s", required)
		}
	}
}

func TestVitessControlRuntimeHasBoundedGoResources(t *testing.T) {
	d := vitessTestDatabase()
	object := vitessDatabaseSpec(d, vitessResources(d.Spec.CPU, d.Spec.Memory))
	control := object["vitessDashboard"].(map[string]any)
	keyspace := object["keyspaces"].([]any)[0].(map[string]any)
	orchestrator := keyspace["vitessOrchestrator"].(map[string]any)
	for _, component := range []map[string]any{control, orchestrator} {
		environment := component["extraEnv"].([]any)
		values := map[string]string{}
		for _, raw := range environment {
			item := raw.(map[string]any)
			values[item["name"].(string)] = item["value"].(string)
		}
		if values["GOMAXPROCS"] != "1" || values["GOMEMLIMIT"] != "192MiB" || len(values) != 2 {
			t.Fatal("Vitess control Go runtime budget changed")
		}
	}
}

func TestVitessTopologyProbesDoNotDependOnReadyService(t *testing.T) {
	d := vitessTestDatabase()
	object := vitessDatabaseSpec(d, vitessResources(d.Spec.CPU, d.Spec.Memory))
	etcd := object["globalLockserver"].(map[string]any)["etcd"].(map[string]any)
	environment := etcd["extraEnv"].([]any)
	identity := environment[0].(map[string]any)
	if identity["name"] != "POD_NAME" || identity["valueFrom"].(map[string]any)["fieldRef"].(map[string]any)["fieldPath"] != "metadata.name" {
		t.Fatal("topology probes cannot address their own pod before readiness")
	}
	endpoint := environment[1].(map[string]any)
	want := "https://$(POD_NAME)." + vitessGeneratedName("database", "etcd") + "-peer." + DatabaseNamespace(d.ID) + ".svc.cluster.local:2379"
	if endpoint["name"] != "ETCDCTL_ENDPOINTS" || endpoint["value"] != want {
		t.Fatal("topology readiness must use verified TLS through the published peer address")
	}
	flags := etcd["extraFlags"].(map[string]any)
	if flags["advertise-client-urls"] != want {
		t.Fatal("topology members must advertise their own verified endpoint")
	}
	if flags["quota-backend-bytes"] != nil || flags["max-request-bytes"] != nil {
		t.Fatal("topology options conflict with the operator's environment variables")
	}
	limits := map[string]string{}
	for _, raw := range environment {
		entry := raw.(map[string]any)
		if value, ok := entry["value"].(string); ok {
			limits[entry["name"].(string)] = value
		}
	}
	if limits["ETCD_QUOTA_BACKEND_BYTES"] != "536870912" || limits["ETCD_MAX_REQUEST_BYTES"] != "1048576" {
		t.Fatal("topology storage and request bounds were lost")
	}
}

func TestVitessOrchestratorUsesClientTLSFlags(t *testing.T) {
	d := vitessTestDatabase()
	object := vitessDatabaseSpec(d, vitessResources(d.Spec.CPU, d.Spec.Memory))
	keyspace := object["keyspaces"].([]any)[0].(map[string]any)
	flags := keyspace["vitessOrchestrator"].(map[string]any)["extraFlags"].(map[string]any)
	if flags["grpc-cert"] != nil || flags["grpc-key"] != nil {
		t.Fatal("vtorc does not provide a gRPC server and rejects its server TLS flags")
	}
	if flags["tablet-manager-grpc-ca"] != vitessTLSPath+"/ca.crt" || flags["tablet-manager-grpc-server-name"] != "database-internal."+DatabaseNamespace(d.ID)+".svc.cluster.local" {
		t.Fatal("vtorc lost verified tablet-manager TLS")
	}
}
