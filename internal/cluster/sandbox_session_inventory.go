package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/hakopod/hakopod/internal/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Namespace deletion must not erase resources that another actor put in the namespace.
// Discovery failures block deletion because a partial inventory cannot prove ownership.
func (c *Client) sandboxCleanupInventory(ctx context.Context, t Target, r sandbox.Record) error {
	if c.dynamic == nil {
		return fmt.Errorf("session cleanup resource inventory is unavailable")
	}
	_, resources, err := c.kube.Discovery().ServerGroupsAndResources()
	if err != nil || len(resources) == 0 {
		return fmt.Errorf("cannot discover all session namespace resources")
	}
	seen := map[schema.GroupResource]bool{}
	for _, group := range resources {
		gv, err := schema.ParseGroupVersion(group.GroupVersion)
		if err != nil {
			return fmt.Errorf("session resource discovery returned an invalid version")
		}
		for _, resource := range group.APIResources {
			if !resource.Namespaced || strings.Contains(resource.Name, "/") {
				continue
			}
			listable := false
			for _, verb := range resource.Verbs {
				if verb == "list" {
					listable = true
				}
			}
			if !listable {
				continue
			}
			gvr := gv.WithResource(resource.Name)
			if seen[gvr.GroupResource()] {
				continue
			}
			seen[gvr.GroupResource()] = true
			// Events contain transient controller diagnostics, not application state.
			if resource.Name == "events" && (gv.Group == "" || gv.Group == "events.k8s.io") {
				continue
			}
			items, err := c.dynamic.Resource(gvr).Namespace(SandboxNamespace(r.ID)).List(ctx, metav1.ListOptions{Limit: 2})
			if err != nil {
				return fmt.Errorf("cannot inventory session namespace resources")
			}
			if items.GetContinue() != "" {
				return fmt.Errorf("session namespace contains unexpected resources")
			}
			for i := range items.Items {
				if !sandboxCleanupAllowed(&items.Items[i], gvr.GroupResource(), t, r) {
					return fmt.Errorf("session namespace contains an unexpected %s resource", resource.Name)
				}
			}
		}
	}
	return nil
}
func sandboxCleanupAllowed(obj *unstructured.Unstructured, kind schema.GroupResource, t Target, r sandbox.Record) bool {
	name := obj.GetName()
	if kind.Group == "" {
		switch kind.Resource {
		case "serviceaccounts":
			if name != "default" {
				return false
			}
			for _, field := range []string{"secrets", "imagePullSecrets"} {
				v, _, err := unstructured.NestedSlice(obj.Object, field)
				if err != nil || len(v) > 0 {
					return false
				}
			}
			return true
		case "configmaps":
			binary, _, binaryErr := unstructured.NestedMap(obj.Object, "binaryData")
			if binaryErr != nil || len(binary) != 0 {
				return false
			}
			data, _, err := unstructured.NestedStringMap(obj.Object, "data")
			return err == nil && name == "kube-root-ca.crt" && len(data) == 1 && data["ca.crt"] != ""
		case "secrets":
			return name == sandboxRegistrySecret && sandboxOwned(obj, t, r) == nil
		case "resourcequotas":
			return name == "session" && sandboxOwned(obj, t, r) == nil
		}
	}
	return kind.Group == "networking.k8s.io" && kind.Resource == "networkpolicies" && name == "deny-all" && sandboxOwned(obj, t, r) == nil
}
