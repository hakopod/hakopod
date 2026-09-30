//go:build !linux

package cluster

import "fmt"

func newActionsWorkspaceProbeHost() (actionsWorkspaceProbeHost, error) {
	return nil, fmt.Errorf("workspace probe must run as root on the selected Linux node")
}
