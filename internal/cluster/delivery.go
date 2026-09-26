package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

// ValidateDelivery is read-only. It runs during planning, durable acceptance
// and reconciliation; approval never substitutes for a current ownership check.
func (c *Client) ValidateDelivery(ctx context.Context, t Target) error {
	_, err := c.ValidateDeliveryWithReport(ctx, t)
	return err
}

func (c *Client) ValidateDeliveryWithReport(ctx context.Context, t Target) (PreflightReport, error) {
	t.Spec = spec.RuntimeEnvironment(t.Spec)
	if err := c.validateDeliveryPolicy(ctx, t); err != nil {
		return PreflightReport{}, err
	}
	report, err := c.Preflight(ctx, t)
	if err != nil {
		return report, err
	}
	return report, report.Validate()
}
func (c *Client) validateDeliveryPolicy(ctx context.Context, t Target) error {
	for _, svc := range t.Spec.Services {
		if svc.Actions != nil && !svc.Suspended {
			if err := c.ActionsAvailable(ctx); err != nil {
				return err
			}
			break
		}
	}
	if err := c.validateServerless(t); err != nil {
		return err
	}
	if _, err := c.serverlessGatewaySources(ctx, t); err != nil {
		return err
	}
	if _, err := c.resolvePrivateEgress(t); err != nil {
		return err
	}
	if err := c.validateWorkloadPolicy(ctx, t); err != nil {
		return err
	}
	if err := c.validatePlacement(ctx, t); err != nil {
		return err
	}
	if err := c.validateStorage(ctx, t); err != nil {
		return err
	}
	if err := c.ValidateReadiness(t.Spec); err != nil {
		return err
	}
	if policy := c.PublicTCPPolicy(); spec.HasPublicTCP(t.Spec) && !policy.Allowed {
		return fmt.Errorf("%w: %s", ErrPublicTCPDisabled, policy.Message)
	}
	if err := c.ValidateCloudSpec(t.Spec); err != nil {
		return err
	}
	if err := c.ValidateCloudCapacity(ctx); err != nil {
		return err
	}
	if !spec.HasDeliveryCapabilities(t.Spec) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.ValidateAWSIdentities(t.Project, t.Environment, t.Spec); err != nil {
		return err
	}
	if err := c.ValidateContainerDaemons(t.Project, t.Environment, t.Spec); err != nil {
		return err
	}
	if err := c.ValidateBackendCertificates(ctx, t); err != nil {
		return err
	}
	return c.ValidatePublicTCP(ctx, t)
}
