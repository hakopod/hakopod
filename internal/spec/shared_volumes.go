package spec

import (
	"fmt"
	"slices"
)

// SharedReadWriteOnceGroups returns connected services that must run on one
// node. Connections through several volumes are transitive; ReadWriteMany
// volumes do not constrain placement. Groups and their members are sorted.
func SharedReadWriteOnceGroups(app Application) [][]string {
	users := map[string][]string{}
	for _, name := range Names(app) {
		for _, mount := range app.Services[name].Mounts {
			volume, ok := app.Volumes[mount.Volume]
			if ok && volume.AccessMode != "ReadWriteMany" && !slices.Contains(users[mount.Volume], name) {
				users[mount.Volume] = append(users[mount.Volume], name)
			}
		}
	}
	neighbors := map[string][]string{}
	for _, members := range users {
		if len(members) < 2 {
			continue
		}
		for _, name := range members {
			neighbors[name] = append(neighbors[name], members...)
		}
	}
	groups := [][]string{}
	seen := map[string]bool{}
	for _, name := range Names(app) {
		if seen[name] || len(neighbors[name]) == 0 {
			continue
		}
		queue := []string{name}
		group := []string{}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if seen[current] {
				continue
			}
			seen[current] = true
			group = append(group, current)
			queue = append(queue, neighbors[current]...)
		}
		slices.Sort(group)
		groups = append(groups, group)
	}
	return groups
}

func normalizeSharedReadWriteOnce(app Application) error {
	for _, group := range SharedReadWriteOnceGroups(app) {
		node, architecture := "", ""
		for _, name := range group {
			service := app.Services[name]
			if service.Job != nil || service.Actions != nil {
				return fmt.Errorf("services.%s: jobs and Managed Actions cannot share ReadWriteOnce volumes across services", name)
			}
			if service.NodeName != "" {
				if node != "" && node != service.NodeName {
					return fmt.Errorf("services.%s.node_name: services sharing ReadWriteOnce storage must use the same node", name)
				}
				node = service.NodeName
			}
			if service.Architecture != "" {
				if architecture != "" && architecture != service.Architecture {
					return fmt.Errorf("services.%s.architecture: services sharing ReadWriteOnce storage must use the same architecture", name)
				}
				architecture = service.Architecture
			}
		}
		// A peer's explicit placement applies to the whole group. Keeping it in
		// the normalized specification also makes the reviewed plan accurate.
		for _, name := range group {
			service := app.Services[name]
			service.NodeName = node
			service.Architecture = architecture
			app.Services[name] = service
		}
	}
	return nil
}
