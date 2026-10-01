package main

import "testing"

func TestOperatorClickHouseSandboxConfiguration(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		settings, err := operatorSettings([]byte("schema_version=1\n[server]\nclickhouse_sandbox="+value), t.TempDir(), noOperatorEnvironment)
		if err != nil || settings["HAKOPOD_CLICKHOUSE_SANDBOX"] != value {
			t.Fatalf("ClickHouse sandbox setting: %v", err)
		}
	}
	if _, err := operatorSettings([]byte("schema_version=1\n[server]\nclickhouse_sandbox='yes'"), t.TempDir(), noOperatorEnvironment); err == nil {
		t.Fatal("untyped sandbox setting accepted")
	}
}
