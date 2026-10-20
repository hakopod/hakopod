package cluster

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const nodeProfileAnnotation = "hakopod.io/node-profile"

// NodeProfileBinding is installation-owned authorization for a relaxed pod
// security profile: an optional unconfined seccomp profile and a privileged
// container, so a service can manage its own cgroup subtree and install its own
// Landlock and seccomp policy. It names one exact service and the one dedicated
// node that service must be pinned to. A privileged container is effective root
// on that node; the platform cannot police what the service does with it.
type NodeProfileBinding struct {
	Name        string `toml:"name" json:"name"`
	Project     string `toml:"project" json:"project"`
	Environment string `toml:"environment" json:"environment"`
	Application string `toml:"application" json:"application"`
	Service     string `toml:"service" json:"service"`
	// Node is the Kubernetes node name the service must be pinned to.
	Node string `toml:"node" json:"node"`
	// Seccomp is "runtime-default" or "unconfined".
	Seccomp string `toml:"seccomp" json:"seccomp"`
	// Privileged gives the container write access to its cgroup subtree.
	Privileged bool `toml:"privileged" json:"privileged"`
}

func ValidateNodeProfileBindings(bindings []NodeProfileBinding) error {
	if len(bindings) > 64 {
		return fmt.Errorf("node profiles support at most 64 bindings")
	}
	seen := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		if !awsBindingName.MatchString(b.Name) || seen[b.Name] {
			return fmt.Errorf("node profile names must be unique lowercase names of at most 63 characters")
		}
		seen[b.Name] = true
		for _, scope := range []string{b.Project, b.Environment, b.Application, b.Service} {
			if !awsScopeName.MatchString(scope) {
				return fmt.Errorf("node profile %s: project, environment, application and service require exact names", b.Name)
			}
		}
		if len(validation.IsDNS1123Subdomain(b.Node)) != 0 {
			return fmt.Errorf("node profile %s: node must be a Kubernetes node name", b.Name)
		}
		if b.Seccomp != "runtime-default" && b.Seccomp != "unconfined" {
			return fmt.Errorf("node profile %s: seccomp must be runtime-default or unconfined", b.Name)
		}
	}
	return nil
}

func ReadNodeProfileBindingsFile(path string) ([]NodeProfileBinding, error) {
	if path == "" {
		return nil, nil
	}
	initial, err := os.Lstat(path)
	if err != nil || !initial.Mode().IsRegular() {
		return nil, fmt.Errorf("node profile bindings require a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open the node profile bindings file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(initial, info) || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("node profile bindings require a regular file of at most 64 KiB without group or other write access")
	}
	if !runtimeProfileFileOwnerAllowed(info) {
		return nil, fmt.Errorf("the node profile bindings file must belong to root or the server user")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, fmt.Errorf("cannot read the bounded node profile bindings file")
	}
	var config struct {
		SchemaVersion int                  `toml:"schema_version"`
		Bindings      []NodeProfileBinding `toml:"bindings"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("node profiles require strict TOML with schema_version = 1")
	}
	if err := ValidateNodeProfileBindings(config.Bindings); err != nil {
		return nil, err
	}
	return config.Bindings, nil
}

func validateNodeProfileInstallation(options Options) error {
	if len(options.NodeProfileBindings) > 0 && (options.DeploymentMode == DeploymentManagedCloud || options.WorkloadPolicy != nil || options.PlacementPolicy != nil) {
		return fmt.Errorf("node profile bindings require a self-hosted installation without a hosted workload policy")
	}
	return ValidateNodeProfileBindings(options.NodeProfileBindings)
}

// resolveNodeProfile re-checks the grant on every path. The pin must match the
// binding's node, so a grant can never follow a service to another node.
func (c *Client) resolveNodeProfile(project, environment, application, service string, svc spec.Service) (NodeProfileBinding, error) {
	if err := validateNodeProfileInstallation(c.options); err != nil {
		return NodeProfileBinding{}, err
	}
	for _, b := range c.options.NodeProfileBindings {
		if b.Name != svc.NodeProfile || b.Project != project || b.Environment != environment || b.Application != application || b.Service != service {
			continue
		}
		if svc.NodeName != b.Node {
			return NodeProfileBinding{}, fmt.Errorf("node_profile %s is approved only for node_name %s", b.Name, b.Node)
		}
		if svc.Replicas > 1 || svc.Autoscaling != nil {
			return NodeProfileBinding{}, fmt.Errorf("node_profile requires a single replica without autoscaling")
		}
		return b, nil
	}
	return NodeProfileBinding{}, fmt.Errorf("node profile is not approved for this project, environment, application and service")
}

// ValidateNodeProfiles runs at plan time, at durable acceptance and again
// during reconciliation.
func (c *Client) ValidateNodeProfiles(project, environment string, app spec.Application) error {
	for _, name := range spec.Names(app) {
		svc := app.Services[name]
		if svc.NodeProfile == "" {
			continue
		}
		if _, err := c.resolveNodeProfile(project, environment, app.Name, name, svc); err != nil {
			return fmt.Errorf("services.%s.node_profile: %w", name, err)
		}
	}
	return nil
}

func (c *Client) prepareNodeProfile(t Target, name string, svc spec.Service, wanted *appsv1.Deployment) error {
	if svc.NodeProfile == "" {
		return nil
	}
	binding, err := c.resolveNodeProfile(t.Project, t.Environment, t.Spec.Name, name, svc)
	if err != nil {
		return err
	}
	applyNodeProfile(binding, &wanted.Spec)
	return nil
}

// applyNodeProfile never runs two copies at once: Recreate stops the old pod
// before the new one starts, so the node's cgroup subtree has one owner.
func applyNodeProfile(b NodeProfileBinding, d *appsv1.DeploymentSpec) {
	d.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	pod := &d.Template
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[nodeProfileAnnotation] = b.Name
	if pod.Spec.SecurityContext == nil {
		pod.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	profile := corev1.SeccompProfileTypeRuntimeDefault
	if b.Seccomp == "unconfined" {
		profile = corev1.SeccompProfileTypeUnconfined
	}
	pod.Spec.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: profile}
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.Name != pod.Spec.Containers[0].Name {
			continue // only the service container, never a helper
		}
		if c.SecurityContext == nil {
			c.SecurityContext = &corev1.SecurityContext{}
		}
		c.SecurityContext.Privileged = ptr(b.Privileged)
		c.SecurityContext.AllowPrivilegeEscalation = ptr(b.Privileged)
	}
}
