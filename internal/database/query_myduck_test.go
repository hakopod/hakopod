package database

import "testing"

func TestMyDuckQueryCapabilitiesMatchNativeGuarantees(t *testing.T) {
	c := CapabilitiesForQuery("duckdb")
	if !c.Supported || c.ReadOnlySupported || c.TransactionalDML || c.TransactionalDDL || len(c.ExecutionModes) != 1 || c.ExecutionModes[0] != "nontransactional" || c.ApplicationIdentity != "postgres (managed application owner)" || c.ParameterStyle != "$1" || c.ReadOnlyEnforcement != "unsupported" || c.DDLCommit != "nontransactional" {
		t.Fatal("MyDuck capabilities exceed native guarantees")
	}
}
