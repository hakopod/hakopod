package cluster

import (
	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestClickHouseTenantAdministrationIsOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		d := clickhouseFixture()
		if enabled {
			d.Spec.ClickHouse = &database.ClickHouseConfig{AccessProfile: "tenant_admin"}
		}
		object := clickhouseDatabaseSpec(d, map[string]any{})
		grants := object["configuration"].(map[string]any)["users"].(map[string]any)["app/grants/query"].([]any)
		var all []string
		for _, grant := range grants {
			all = append(all, grant.(string))
		}
		joined := strings.Join(all, "\n")
		if strings.Contains(joined, "CREATE USER") != enabled {
			t.Fatal("tenant access did not follow profile")
		}
		for _, forbidden := range []string{"GRANT ALL", "ACCESS MANAGEMENT", "ON system.*", "CREATE ROLE", "ALTER USER", "GRANT OPTION ON *.*"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("unexpected privilege: %s", forbidden)
			}
		}
		if enabled && !strings.Contains(joined, "GRANT SELECT ON app.* WITH GRANT OPTION") {
			t.Fatal("tenant read delegation missing")
		}
	}
}

func TestClickHouseRevisionCompletionRejectsStaleAccess(t *testing.T) {
	for _, tc := range []struct {
		name, state, task string
		completed         []any
		ready             bool
	}{
		{"current", "Completed", "revision-2", []any{"revision-2", "revision-1"}, true},
		{"previous", "Completed", "revision-1", []any{"revision-1"}, false},
		{"not recorded", "Completed", "revision-2", []any{"revision-1"}, false},
		{"running", "InProgress", "revision-2", []any{"revision-1"}, false},
		{"missing task", "Completed", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"status": tc.state, "taskID": tc.task, "taskIDsCompleted": tc.completed}}}
			if clickhouseRevisionCompleted(object, 2) != tc.ready {
				t.Fatal("stale controller status qualified new ClickHouse access")
			}
		})
	}
}
