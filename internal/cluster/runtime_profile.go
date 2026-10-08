package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// RuntimeProfileBinding grants one service access to an installed runtime.
// The operator owns the handler configuration and its kernel controls.
type RuntimeProfileBinding struct {
	Name         string `toml:"name" json:"name"`
	Project      string `toml:"project" json:"project"`
	Environment  string `toml:"environment" json:"environment"`
	Application  string `toml:"application" json:"application"`
	Service      string `toml:"service" json:"service"`
	RuntimeClass string `toml:"runtime_class" json:"runtime_class"`
	Handler      string `toml:"handler" json:"handler"`
}

func ValidateRuntimeProfileBindings(bindings []RuntimeProfileBinding) error {
	if len(bindings) > 64 {
		return fmt.Errorf("runtime profiles support at most 64 bindings")
	}
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if !awsBindingName.MatchString(binding.Name) || seen[binding.Name] {
			return fmt.Errorf("runtime profile names must be unique lowercase names of at most 63 characters")
		}
		seen[binding.Name] = true
		for _, scope := range []string{binding.Project, binding.Environment, binding.Application, binding.Service} {
			if !awsScopeName.MatchString(scope) {
				return fmt.Errorf("runtime profiles require exact project, environment, application and service names")
			}
		}
		if len(validation.IsDNS1123Subdomain(binding.RuntimeClass)) != 0 || len(validation.IsDNS1123Label(binding.Handler)) != 0 {
			return fmt.Errorf("runtime profiles require a valid RuntimeClass name and handler")
		}
	}
	return nil
}

func ReadRuntimeProfileBindingsFile(path string) ([]RuntimeProfileBinding, error) {
	if path == "" {
		return nil, nil
	}
	initial, err := os.Lstat(path)
	if err != nil || !initial.Mode().IsRegular() {
		return nil, fmt.Errorf("runtime profile bindings require a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open the runtime profile bindings file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(initial, info) || !info.Mode().IsRegular() || info.Size() > 64<<10 || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("runtime profile bindings require a regular file of at most 64 KiB without group or other write access")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, fmt.Errorf("cannot read the bounded runtime profile bindings file")
	}
	var config struct {
		SchemaVersion int                     `toml:"schema_version"`
		Bindings      []RuntimeProfileBinding `toml:"bindings"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil || config.SchemaVersion != 1 {
		return nil, fmt.Errorf("runtime profiles require strict TOML with schema_version = 1")
	}
	if err := ValidateRuntimeProfileBindings(config.Bindings); err != nil {
		return nil, err
	}
	return config.Bindings, nil
}

func validateRuntimeProfileInstallation(options Options) error {
	if len(options.RuntimeProfileBindings) > 0 && (options.DeploymentMode == DeploymentManagedCloud || options.WorkloadPolicy != nil || options.PlacementPolicy != nil) {
		return fmt.Errorf("runtime profile bindings require a self-hosted installation without a hosted workload policy")
	}
	return ValidateRuntimeProfileBindings(options.RuntimeProfileBindings)
}

func (c *Client) resolveRuntimeProfile(ctx context.Context, project, environment, application, service string, svc spec.Service) (RuntimeProfileBinding, error) {
	if err := validateRuntimeProfileInstallation(c.options); err != nil {
		return RuntimeProfileBinding{}, err
	}
	if svc.Actions != nil || svc.Serverless != nil {
		return RuntimeProfileBinding{}, fmt.Errorf("runtime profiles are unavailable for Managed Actions and serverless services")
	}
	for _, binding := range c.options.RuntimeProfileBindings {
		if binding.Name != svc.RuntimeProfile || binding.Project != project || binding.Environment != environment || binding.Application != application || binding.Service != service {
			continue
		}
		if c.kube == nil {
			return RuntimeProfileBinding{}, fmt.Errorf("the Kubernetes client is unavailable for runtime profile validation")
		}
		probe, stop := context.WithTimeout(ctx, 5*time.Second)
		class, err := c.kube.NodeV1().RuntimeClasses().Get(probe, binding.RuntimeClass, metav1.GetOptions{})
		stop()
		if err != nil || class.DeletionTimestamp != nil || class.Handler != binding.Handler {
			return RuntimeProfileBinding{}, fmt.Errorf("the approved RuntimeClass is unavailable or its handler does not match")
		}
		// The application planner does not include RuntimeClass overhead yet.
		// Refuse it instead of approving an understated resource reservation.
		if class.Overhead != nil && len(class.Overhead.PodFixed) != 0 {
			return RuntimeProfileBinding{}, fmt.Errorf("runtime profiles do not yet support RuntimeClass pod overhead")
		}
		return binding, nil
	}
	return RuntimeProfileBinding{}, fmt.Errorf("runtime profile is not approved for this project, environment, application and service")
}

// ValidateRuntimeProfiles checks grants and installed classes before acceptance.
// This does not certify the runtime's kernel controls or revoke existing pods.
func (c *Client) ValidateRuntimeProfiles(ctx context.Context, project, environment string, app spec.Application) error {
	for _, name := range spec.Names(app) {
		svc := app.Services[name]
		if svc.RuntimeProfile == "" {
			continue
		}
		if _, err := c.resolveRuntimeProfile(ctx, project, environment, app.Name, name, svc); err != nil {
			return fmt.Errorf("services.%s.runtime_profile: %w", name, err)
		}
	}
	return nil
}

// Each workload path checks the grant again before submitting its pod template.
func (c *Client) prepareRuntimeProfile(ctx context.Context, t Target, name string, svc spec.Service, wanted *appsv1.Deployment) error {
	if svc.RuntimeProfile == "" {
		return nil
	}
	binding, err := c.resolveRuntimeProfile(ctx, t.Project, t.Environment, t.Spec.Name, name, svc)
	if err != nil {
		return err
	}
	pod := &wanted.Spec.Template
	if pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName != binding.RuntimeClass {
		return fmt.Errorf("runtime profile conflicts with the existing workload runtime policy")
	}
	pod.Spec.RuntimeClassName = ptr(binding.RuntimeClass)
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["hakopod.io/runtime-profile"] = binding.Name
	return nil
}
