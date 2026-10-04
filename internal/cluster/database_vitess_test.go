package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	if strings.Contains(init, "CREATE DATABASE IF NOT EXISTS app") || !strings.Contains(init, "CREATE DATABASE IF NOT EXISTS _vt;") {
		t.Fatal("Vitess bootstrap must preserve native fresh-storage detection and metadata setup")
	}
	if !strings.Contains(init, "GRANT ALL ON app.* TO 'vt_app'@'localhost'") ||
		!strings.Contains(init, "GRANT SELECT, CREATE ON _vt.tables TO 'vt_app'@'localhost'; REVOKE CREATE ON _vt.tables FROM 'vt_app'@'localhost'") ||
		!strings.Contains(init, "GRANT SELECT, UPDATE, CREATE ON _vt.schema_migrations TO 'vt_app'@'localhost'; REVOKE CREATE ON _vt.schema_migrations FROM 'vt_app'@'localhost'") ||
		strings.Contains(init, "GRANT ALL ON *.* TO 'vt_app'") || !strings.Contains(init, "REQUIRE SSL") {
		t.Fatal("application or replication grants exceed their boundary")
	}
	if !strings.Contains(init, "GRANT ALL ON _vt.* TO 'vt_allprivs'@'localhost'") || strings.Contains(init, "GRANT ALL ON *.* TO 'vt_allprivs'") || strings.Contains(init, "GRANT ALL ON _vt.* TO 'vt_app'") || strings.Contains(init, "GRANT INSERT ON _vt.") || strings.Contains(init, "GRANT DELETE ON _vt.") {
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

func TestVitessRuntimeAdmissionMatchesBuild(t *testing.T) {
	d := vitessTestDatabase()
	err := vitessRuntimeSupported(d.Spec)
	if (vitessNativeAcceptance || vitessReleaseQualified) && err != nil {
		t.Fatal("native acceptance build rejected the valid Vitess runtime", err)
	}
	if !vitessNativeAcceptance && !vitessReleaseQualified && err == nil {
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

func TestVitessFixtureNodesAcceptExactDedicatedTopology(t *testing.T) {
	dedicated := append([]string(nil), vitessDedicatedFixtureNodes...)
	if !validVitessFixtureNodes(dedicated) {
		t.Fatal("exact dedicated Vitess worker topology was rejected")
	}
	for _, nodes := range [][]string{
		dedicated[:2],
		{"k3d-hakopod-vitess-worker-0", "k3d-hakopod-dev-server-0"},
		{"k3d-hakopod-vitess-worker-0", "k3d-hakopod-vitess-worker-0", "k3d-hakopod-vitess-worker-2"},
	} {
		if validVitessFixtureNodes(nodes) {
			t.Fatalf("invalid dedicated Vitess topology accepted: %v", nodes)
		}
	}
}

func TestVitessNativeFixturePolicySelectionPreservesLegacyTopology(t *testing.T) {
	legacy := "k3d-hakopod-dev-server-0,k3d-hakopod-database-worker-0"
	for _, nodes := range []string{"", legacy} {
		selected, err := selectVitessNativeFixturePolicy(nodes)
		if err != nil || selected {
			t.Fatalf("legacy fixture selection %q enabled dedicated policy: selected=%v err=%v", nodes, selected, err)
		}
	}
	selected, err := selectVitessNativeFixturePolicy(strings.Join(vitessDedicatedFixtureNodes, ","))
	if err != nil || !selected {
		t.Fatal("exact dedicated fixture selection did not enable its policy", err)
	}
	for _, nodes := range []string{
		strings.Join(vitessDedicatedFixtureNodes[:2], ","),
		vitessDedicatedFixtureNodes[0] + ",k3d-hakopod-dev-server-0",
		vitessDedicatedFixtureNodes[0] + "," + vitessDedicatedFixtureNodes[0] + "," + vitessDedicatedFixtureNodes[2],
	} {
		if selected, err := selectVitessNativeFixturePolicy(nodes); err == nil || selected {
			t.Fatalf("invalid dedicated selection %q was not rejected", nodes)
		}
	}
}

func TestVitessNativeFixturePolicyIsScopedAndSchedulesEveryComponent(t *testing.T) {
	destination := backup.Destination{ID: strings.Repeat("d", 32), Revision: 7}
	resolve := vitessNativeFixtureDatabasePolicy(map[string]vitessLiveStorage{
		"recovery-source": {DatabaseID: strings.Repeat("e", 32), Dedicated: true, Destination: destination},
	})
	d := vitessTestDatabase()
	d.Project = "demo"
	d.Spec.Name = "vitess-development-recovery-source"
	d.Spec.Placement.NodeNames = append([]string(nil), vitessDedicatedFixtureNodes...)
	d.Spec.Vitess.BackupDestinationID = destination.ID
	d.Spec.Vitess.BackupDestinationRevision = destination.Revision
	policy, err := resolve(context.Background(), d.Project, d.Environment, d.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Pool != "vitess-acceptance" || policy.RuntimeClass != "runsc" || policy.StorageClass != "local-path" || !validVitessDedicatedFixtureNodes(policy.NodeNames) {
		t.Fatal("native fixture policy changed", policy)
	}
	object := &unstructured.Unstructured{Object: map[string]any{"spec": vitessDatabaseSpec(d, vitessResources(d.Spec.CPU, d.Spec.Memory))}}
	applyVitessPolicy(object, d.Spec, policy)
	count := 0
	mutateVitessComponents(object, func(item map[string]any, role string) {
		count++
		tolerations, ok := item["tolerations"].([]any)
		if !ok || len(tolerations) != 1 || tolerations[0].(map[string]any)["value"] != "vitess-acceptance" {
			t.Fatalf("%s omitted the dedicated pool toleration", role)
		}
		raw, err := json.Marshal(item["affinity"])
		if err != nil || !strings.Contains(string(raw), DatabaseDefaultRuntimeLabel) || !strings.Contains(string(raw), "runsc") || !strings.Contains(string(raw), "vitess-acceptance") {
			t.Fatalf("%s omitted the trusted dedicated-node affinity", role)
		}
	})
	if count != 5 {
		t.Fatalf("checked %d Vitess component classes, want 5", count)
	}
	operator := vitessOperatorObject(d, "namespace-uid")
	if err := applyVitessOperatorPolicy(operator, d.Spec.Placement.NodeNames, &policy); err != nil {
		t.Fatal(err)
	}
	operatorRaw, err := json.Marshal(operator.Object["spec"])
	if err != nil || !strings.Contains(string(operatorRaw), DatabaseDefaultRuntimeLabel) || !strings.Contains(string(operatorRaw), "runsc") || !strings.Contains(string(operatorRaw), "vitess-acceptance") || !strings.Contains(string(operatorRaw), "NoSchedule") {
		t.Fatal("Vitess operator omitted the trusted dedicated-node scheduling policy")
	}

	for name, mutate := range map[string]func(*database.Resource){
		"wrong project": func(resource *database.Resource) { resource.Project = "other" },
		"wrong environment": func(resource *database.Resource) { resource.Environment = "production" },
		"unknown fixture": func(resource *database.Resource) { resource.Spec.Name = "vitess-development-other" },
		"wrong version": func(resource *database.Resource) { resource.Spec.Version = "22" },
		"wrong destination": func(resource *database.Resource) { resource.Spec.Vitess.BackupDestinationID = strings.Repeat("f", 32) },
		"wrong destination revision": func(resource *database.Resource) { resource.Spec.Vitess.BackupDestinationRevision++ },
		"partial topology": func(resource *database.Resource) { resource.Spec.Placement.NodeNames = resource.Spec.Placement.NodeNames[:2] },
		"mixed topology": func(resource *database.Resource) { resource.Spec.Placement.NodeNames[2] = "k3d-hakopod-dev-server-0" },
		"duplicate topology": func(resource *database.Resource) { resource.Spec.Placement.NodeNames[2] = resource.Spec.Placement.NodeNames[0] },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := d
			candidate.Spec.Placement.NodeNames = append([]string(nil), d.Spec.Placement.NodeNames...)
			candidate.Spec.Vitess = &database.VitessConfig{BackupDestinationID: d.Spec.Vitess.BackupDestinationID, BackupDestinationRevision: d.Spec.Vitess.BackupDestinationRevision}
			mutate(&candidate)
			if _, err := resolve(context.Background(), candidate.Project, candidate.Environment, candidate.Spec); err == nil {
				t.Fatal("unapproved native fixture policy was accepted")
			}
		})
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

func TestVitessTabletAndBackupProcessBudgets(t *testing.T) {
	d := vitessTestDatabase()
	object := vitessDatabaseSpec(d, vitessResources(d.Spec.CPU, d.Spec.Memory))
	keyspace := object["keyspaces"].([]any)[0].(map[string]any)
	partition := keyspace["partitionings"].([]any)[0].(map[string]any)["equal"].(map[string]any)
	pool := partition["shardTemplate"].(map[string]any)["tabletPools"].([]any)[0].(map[string]any)
	values := map[string]string{}
	for _, raw := range pool["extraEnv"].([]any) {
		item := raw.(map[string]any)
		if value, ok := item["value"].(string); ok {
			values[item["name"].(string)] = value
		}
	}
	if values["GOMAXPROCS"] != "1" || values["GOMEMLIMIT"] != "192MiB" {
		t.Fatal("tablet restore process has no bounded Go budget")
	}
	for _, name := range []string{"requests", "limits"} {
		resources := vitessBackupResources(d.Spec)[name].(map[string]any)
		if resources["cpu"] != "600m" || resources["memory"] != "1536Mi" {
			t.Fatal("backup omitted its Go or MySQL process allocation", resources)
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
