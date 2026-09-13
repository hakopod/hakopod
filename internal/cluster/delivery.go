package cluster

import (
	"context"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

// ValidateDelivery is read-only. It runs during planning, durable acceptance
// and reconciliation; approval never substitutes for a current ownership check.
func (c *Client) ValidateDelivery(ctx context.Context, t Target) error {
	if !spec.HasDeliveryCapabilities(t.Spec) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.ValidateAWSIdentities(t.Project, t.Environment, t.Spec); err != nil {
		return err
	}
	if err := c.ValidateBackendCertificates(ctx, t); err != nil {
		return err
	}
	return c.ValidatePublicTCP(ctx, t)
}
