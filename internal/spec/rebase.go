package spec

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Rebase carries a staged draft forward onto a newer current revision. Each
// service and each top-level setting is merged three ways from the base the
// draft started from: a part only the draft changed keeps the draft, a part
// only the current revision changed takes the current one, and a part both
// changed differently is a conflict. Conflicts are named, never resolved silently.
func Rebase(base, current, draft Application) (Application, []string, error) {
	b, err := asMap(base)
	if err != nil {
		return Application{}, nil, err
	}
	c, err := asMap(current)
	if err != nil {
		return Application{}, nil, err
	}
	d, err := asMap(draft)
	if err != nil {
		return Application{}, nil, err
	}
	merged := map[string]any{}
	var conflicts []string
	for _, key := range keys(b, c, d) {
		if key == "services" {
			continue
		}
		value, conflict := merge3(b[key], c[key], d[key])
		if conflict {
			conflicts = append(conflicts, key)
		}
		if value != nil {
			merged[key] = value
		}
	}
	bs, cs, ds := submap(b["services"]), submap(c["services"]), submap(d["services"])
	services := map[string]any{}
	for _, name := range keys(bs, cs, ds) {
		value, conflict := merge3(bs[name], cs[name], ds[name])
		if conflict {
			conflicts = append(conflicts, "services."+name)
		}
		if value != nil {
			services[name] = value
		}
	}
	merged["services"] = services
	data, err := json.Marshal(merged)
	if err != nil {
		return Application{}, nil, err
	}
	var out Application
	if err := json.Unmarshal(data, &out); err != nil {
		return Application{}, nil, fmt.Errorf("rebased draft is not a valid application: %w", err)
	}
	return out, conflicts, nil
}

func merge3(base, current, draft any) (any, bool) {
	switch {
	case reflect.DeepEqual(current, draft), reflect.DeepEqual(base, draft):
		return current, false
	case reflect.DeepEqual(base, current):
		return draft, false
	default:
		return current, true
	}
}

func asMap(a Application) (map[string]any, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, json.Unmarshal(data, &out)
}

func submap(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func keys(maps ...map[string]any) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
