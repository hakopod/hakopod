//go:build !hakopod_native_acceptance || !linux

package nativeacceptance

import (
	"context"
	"fmt"
	"os"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"k8s.io/client-go/rest"
)

// Configure never enables development qualification in a shipping binary.
func Configure(context.Context) (string, error) {
	if os.Getenv("HAKOPOD_NATIVE_ACCEPTANCE_CONFIG_FILE") != "" {
		return "", fmt.Errorf("native acceptance requires a separately compiled development binary")
	}
	return "", nil
}

func Plan(_, _, _ string, plan managedplatform.Plan) managedplatform.Plan { return plan }
func Recovery(_, _, _ string) bool                                        { return false }
func Recheck(context.Context, string, string, string) error {
	return fmt.Errorf("native acceptance requires a separately compiled development binary")
}
func Watch(context.Context, context.CancelFunc) {}
func KubernetesConfig() *rest.Config            { return nil }
