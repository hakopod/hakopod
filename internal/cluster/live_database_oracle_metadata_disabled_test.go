//go:build !hakopod_native_acceptance

package cluster

import (
	"context"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func oracleAcceptanceMetadataHook(*testing.T, context.Context, *Client, database.Resource, database.Member) {
}

func oracleCancellationDiagnosticContext(_ *testing.T, ctx context.Context) (context.Context, func()) {
	return ctx, func() {}
}
