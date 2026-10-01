package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func mongodbUnitFixture() database.Resource {
	return database.Resource{ID: strings.Repeat("b", 32), Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "mongodb", Engine: "mongodb", Version: "8.0", Mode: "cluster", Shards: 1, Replicas: 2, CPU: "500m", Memory: "1Gi", StorageGiB: 2, TLS: &database.TLSConfig{Mode: "required"}}}
}

func TestMongoDBObjectLimitsImagesCredentialsAndPlacement(t *testing.T) {
	d := mongodbUnitFixture()
	object, err := DatabaseObject(d)
	if err != nil {
		t.Fatal(err)
	}
	if object.GetKind() != "MongoDBCommunity" {
		t.Fatal("incorrect MongoDB controller")
	}
	for _, kind := range []string{"containers", "initContainers"} {
		containers, _, _ := unstructured.NestedSlice(object.Object, "spec", "statefulSet", "spec", "template", "spec", kind)
		if len(containers) != 2 {
			t.Fatal("MongoDB helper allocation missing")
		}
		for _, raw := range containers {
			container := raw.(map[string]any)
			if !strings.Contains(container["image"].(string), "@sha256:") || container["resources"] == nil {
				t.Fatal("unbounded or mutable MongoDB container")
			}
		}
	}
	users, _, _ := unstructured.NestedSlice(object.Object, "spec", "users")
	app := users[0].(map[string]any)
	roles := app["roles"].([]any)
	if app["name"] != "app" || app["db"] != "app" || len(roles) != 1 || roles[0].(map[string]any)["name"] != "readWrite" {
		t.Fatal("application has administrative MongoDB privileges")
	}
	custom, _, _ := unstructured.NestedSlice(object.Object, "spec", "security", "roles")
	if len(custom) != 1 {
		t.Fatal("MongoDB recovery role missing")
	}
	recovery := custom[0].(map[string]any)
	privileges := recovery["privileges"].([]any)
	resource := privileges[0].(map[string]any)["resource"].(map[string]any)
	actions := privileges[0].(map[string]any)["actions"].([]any)
	restrictions := recovery["authenticationRestrictions"].([]any)
	if recovery["db"] != "app" || resource["db"] != "app" || resource["collection"] != "" || len(actions) != 1 || actions[0] != "bypassDocumentValidation" || restrictions[0].(map[string]any)["clientSource"].([]any)[0] != "127.0.0.1/32" {
		t.Fatal("MongoDB recovery permissions escaped their scope")
	}
	d.Spec.Placement.Spread = "zones"
	applyDatabasePlacement(object, d.Spec, []string{"one", "two", "three"})
	affinity, _, _ := unstructured.NestedMap(object.Object, "spec", "statefulSet", "spec", "template", "spec", "affinity")
	if affinity["nodeAffinity"] == nil || affinity["podAntiAffinity"] == nil {
		t.Fatal("MongoDB placement was omitted")
	}
	if optional, _, _ := unstructured.NestedBool(object.Object, "spec", "security", "tls", "optional"); optional {
		t.Fatal("plaintext MongoDB enabled")
	}
}

func TestMongoDBNativeHealthRejectsReplicaIdentityAndQuorumGaps(t *testing.T) {
	d := mongodbUnitFixture()
	members := []database.Member{{Name: "database-0"}, {Name: "database-1"}, {Name: "database-2"}}
	rows := []map[string]any{}
	for i, member := range members {
		state := 2
		if i == 0 {
			state = 1
		}
		rows = append(rows, map[string]any{"name": mongodbMemberHost(d, member) + ":27017", "health": 1, "state": state, "self": i == 0, "configVersion": 4})
	}
	base := map[string]any{"set": "database", "myState": 1, "members": rows}
	raw, _ := json.Marshal(base)
	decode := func(input map[string]any) mongodbReplicaView {
		data, _ := bson.Marshal(input)
		var view mongodbReplicaView
		if err := bson.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	if primary, role, err := verifyMongoDBReplicaView(decode(base), d, members[0], members); err != nil || primary != "database-0" || role != "primary" {
		t.Fatal("healthy native MongoDB membership rejected", err)
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["set"] = "other" },
		func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["health"] = 0 },
		func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["state"] = 1 },
		func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["configVersion"] = 3 },
		func(v map[string]any) { v["members"].([]any)[1].(map[string]any)["name"] = "outside.example:27017" },
	} {
		var input map[string]any
		_ = json.Unmarshal(raw, &input)
		change(input)
		if _, _, err := verifyMongoDBReplicaView(decode(input), d, members[0], members); err == nil {
			t.Fatal("unsafe MongoDB membership accepted")
		}
	}
}
