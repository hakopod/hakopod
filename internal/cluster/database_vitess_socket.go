package cluster

import (
	"context"
	"fmt"
	"io"

	"github.com/hakopod/hakopod/internal/database"
)

var vitessSocketPaths = []string{"/vt/socket/mysql.sock", "/vt/socket/mysqlctl.sock"}

func verifyVitessSocketVisibility(exec func(string, []string, io.Writer) error) error {
	command := []string{"bash", "-ceu", `test -S "$1"; test -S "$2"; printf 'socket-ready\n'`, "vitess-socket-check", vitessSocketPaths[0], vitessSocketPaths[1]}
	for _, container := range []string{"vttablet", "mysqld"} {
		out := &databaseBoundedWriter{limit: 64}
		if err := exec(container, command, out); err != nil || string(out.Bytes()) != "socket-ready\n" {
			return fmt.Errorf("Vitess tablet sockets are unavailable in %s", container)
		}
	}
	return nil
}

func (c *Client) verifyVitessTabletSockets(ctx context.Context, inventory *vitessObservationInventory, member database.Member) error {
	pod, container, err := c.tabletForExec(ctx, member, inventory)
	if err != nil || container != "vttablet" {
		return fmt.Errorf("Vitess tablet socket target changed")
	}
	return verifyVitessSocketVisibility(func(container string, command []string, stdout io.Writer) error {
		return c.databaseExecVerifiedPod(ctx, pod, container, command, nil, stdout)
	})
}
