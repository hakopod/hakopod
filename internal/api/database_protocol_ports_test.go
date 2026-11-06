package api

import (
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestProtocolPortsSkipRoleEndpoints(t *testing.T) {
	host := "database.hdb-x.svc.cluster.local"
	clickhouse := []database.Endpoint{{Purpose: "read_write", Host: host, Port: 9440}, {Purpose: "native", Host: host, Port: 9440}, {Purpose: "https", Host: host, Port: 8443}}
	if got := fmt.Sprint(protocolPorts(clickhouse, clickhouse[0])); got != "[8443]" {
		t.Fatal(got)
	}
	router := []database.Endpoint{{Purpose: "read_write", Host: "router", Port: 6446}, {Purpose: "read_only", Host: "router", Port: 6447}}
	if got := protocolPorts(router, router[0]); len(got) != 0 {
		t.Fatal("a read-write binding gained the read-only port", got)
	}
}
