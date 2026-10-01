package cluster

import (
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestVitessSocketVisibilityRequiresBothSocketsInBothContainers(t *testing.T) {
	want := []string{"bash", "-ceu", `test -S "$1"; test -S "$2"; printf 'socket-ready\n'`, "vitess-socket-check", "/vt/socket/mysql.sock", "/vt/socket/mysqlctl.sock"}
	var containers []string
	err := verifyVitessSocketVisibility(func(container string, command []string, stdout io.Writer) error {
		if !reflect.DeepEqual(command, want) {
			t.Fatalf("unexpected socket probe: %#v", command)
		}
		containers = append(containers, container)
		_, _ = io.WriteString(stdout, "socket-ready\n")
		return nil
	})
	if err != nil || !reflect.DeepEqual(containers, []string{"vttablet", "mysqld"}) {
		t.Fatalf("socket visibility check failed: containers=%v err=%v", containers, err)
	}
}

func TestVitessSocketVisibilityFailsClosed(t *testing.T) {
	calls := 0
	err := verifyVitessSocketVisibility(func(container string, command []string, stdout io.Writer) error {
		calls++
		if container == "vttablet" {
			_, _ = io.WriteString(stdout, "socket-ready\n")
			return nil
		}
		return errors.New("missing socket")
	})
	if err == nil || calls != 2 {
		t.Fatalf("missing mysqld socket was accepted: calls=%d err=%v", calls, err)
	}
}
