package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestProvisionPostgresApplicationRejectsNonHexPasswordBeforeClusterAccess(t *testing.T) {
	plan := database.ApplicationProvisioningPlan{ID: strings.Repeat("a", 32), DatabaseID: strings.Repeat("b", 32), DatabaseRevision: 1, Role: "hp_fixture", LogicalDatabase: "hp_fixture_db"}
	d := database.Resource{ID: plan.DatabaseID, Revision: 1, Spec: database.Spec{Engine: "postgresql"}}
	password := []byte(strings.Repeat("z", 64))
	if _, err := (&Client{}).ProvisionPostgresApplication(context.Background(), d, plan, password, "accepted", nil); err == nil {
		t.Fatal("non-hex generated password reached cluster access")
	}
}
