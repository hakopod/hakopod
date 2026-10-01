package cluster

import (
	"errors"
	"fmt"
	"testing"

	mysqlclient "github.com/go-sql-driver/mysql"
)

func TestVitessPlaintextRefusalRequiresNativeEvidence(t *testing.T) {
	explicit := &mysqlclient.MySQLError{Number: 1105, Message: "unknown error: Code: UNAVAILABLE\nserver does not allow insecure connections, client must use SSL/TLS\n"}
	if !vitessPlaintextRefused(fmt.Errorf("handshake: %w", explicit)) {
		t.Fatal("pinned Vitess secure-transport refusal was rejected")
	}
	for _, err := range []error{nil, errors.New("connection reset"),
		&mysqlclient.MySQLError{Number: 1105, Message: "internal server error"},
		&mysqlclient.MySQLError{Number: 1105, Message: "server does not allow insecure connections"},
		&mysqlclient.MySQLError{Number: 1040, Message: explicit.Message}} {
		if vitessPlaintextRefused(err) {
			t.Fatalf("unrelated error was accepted as transport enforcement: %T", err)
		}
	}
}
