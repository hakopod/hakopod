package spec

import (
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"
)

// KubernetesPatches are strategic-merge patches, written as YAML, that the
// runtime applies on top of the Deployment, Service and Ingress it generates
// for a service. The runtime refuses any patch that changes a field Hakopod's
// security model depends on. See docs/kubernetes-patches.md.
type KubernetesPatches struct {
	Deployment string `json:"deployment,omitempty" toml:"deployment,multiline,omitempty"`
	Service    string `json:"service,omitempty" toml:"service,multiline,omitempty"`
	Ingress    string `json:"ingress,omitempty" toml:"ingress,multiline,omitempty"`
}

// MaxKubernetesPatchBytes bounds each patch. Patches live in every stored
// revision and plan diff, so they stay small.
const MaxKubernetesPatchBytes = 16 << 10

// ParseKubernetesPatch decodes one patch into its JSON object form. The patch
// may not rename or move the object it applies to.
func ParseKubernetesPatch(text string) (map[string]any, error) {
	if len(text) > MaxKubernetesPatchBytes {
		return nil, fmt.Errorf("patch exceeds %d bytes", MaxKubernetesPatchBytes)
	}
	data, err := yaml.YAMLToJSON([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("invalid YAML: %v", err)
	}
	var patch map[string]any
	if err := json.Unmarshal(data, &patch); err != nil || patch == nil {
		return nil, fmt.Errorf("the patch must be a YAML mapping")
	}
	for _, key := range []string{"apiVersion", "kind"} {
		if _, ok := patch[key]; ok {
			return nil, fmt.Errorf("%s cannot be patched", key)
		}
	}
	if metadata, ok := patch["metadata"].(map[string]any); ok {
		for _, key := range []string{"name", "namespace"} {
			if _, ok := metadata[key]; ok {
				return nil, fmt.Errorf("metadata.%s cannot be patched", key)
			}
		}
	}
	return patch, nil
}

func normalizeKubernetes(svc *Service) error {
	k := svc.Kubernetes
	if k == nil {
		return nil
	}
	if *k == (KubernetesPatches{}) {
		svc.Kubernetes = nil
		return nil
	}
	if svc.Job != nil || svc.Session != nil || svc.Actions != nil || svc.Serverless != nil {
		return fmt.Errorf("kubernetes: patches apply only to a regular long-running service")
	}
	for _, p := range []struct{ name, text string }{{"deployment", k.Deployment}, {"service", k.Service}, {"ingress", k.Ingress}} {
		if p.text == "" {
			continue
		}
		if _, err := ParseKubernetesPatch(p.text); err != nil {
			return fmt.Errorf("kubernetes.%s: %w", p.name, err)
		}
	}
	if k.Service != "" && len(ServicePorts(*svc)) == 0 {
		return fmt.Errorf("kubernetes.service: requires a service with a port")
	}
	if k.Ingress != "" && (!svc.Public || svc.Port == 0) {
		return fmt.Errorf("kubernetes.ingress: requires a public service with a port")
	}
	return nil
}

// KubernetesPatchesChanged reports whether next adds or changes any service's
// Kubernetes patch. Removing a patch only returns to generated objects, so it
// is not a change that needs the kubernetes:patch permission.
func KubernetesPatchesChanged(before, after Application) bool {
	for name, svc := range after.Services {
		next := svc.Kubernetes
		if next == nil {
			continue
		}
		var old KubernetesPatches
		if previous := before.Services[name].Kubernetes; previous != nil {
			old = *previous
		}
		if next.Deployment != "" && next.Deployment != old.Deployment ||
			next.Service != "" && next.Service != old.Service ||
			next.Ingress != "" && next.Ingress != old.Ingress {
			return true
		}
	}
	return false
}
