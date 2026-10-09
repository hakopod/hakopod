package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
)

func TestApplicationProvisioningSecretWriteFailureSeparatesConflictFromRetry(t *testing.T) {
	status, phase, message := applicationProvisioningSecretWriteFailure(fmt.Errorf("write: %w", cluster.ErrProvisionedWorkloadSecretConflict), "provisioning")
	if status != "failed" || phase != "credentials" || !strings.Contains(message, "Review") || strings.Contains(message, "value") {
		t.Fatalf("ownership conflict did not require a safe new review: %q %q %q", status, phase, message)
	}
	status, phase, message = applicationProvisioningSecretWriteFailure(errors.New("temporary transport failure"), "verifying")
	if status != "queued" || phase != "verifying" || !strings.Contains(message, "retrying") {
		t.Fatalf("transient write failure was not retained for retry: %q %q %q", status, phase, message)
	}
}
