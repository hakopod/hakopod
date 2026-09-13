package cluster

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
)

func ParsePublicTCPPorts(value string) ([]int32, error) {
	if value == "" {
		return nil, nil
	}
	values := strings.Split(value, ",")
	if len(values) > 16 {
		return nil, fmt.Errorf("HAKOPOD_PUBLIC_TCP_PORTS supports at most 16 ports")
	}
	ports := make([]int32, 0, len(values))
	seen := map[int32]bool{}
	for _, raw := range values {
		number, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
		port := int32(number)
		if err != nil || !spec.ValidPublicTCPPort(port) || seen[port] {
			return nil, fmt.Errorf("HAKOPOD_PUBLIC_TCP_PORTS requires unique, non-platform TCP ports separated by commas")
		}
		seen[port] = true
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, nil
}
