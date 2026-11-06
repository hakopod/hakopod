package operations

import (
	"context"
	"regexp"
)

type approvalKey struct{}

var approvalID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidApprovalID reports whether id can name an agent approval.
func ValidApprovalID(id string) bool { return approvalID.MatchString(id) }

// WithApproval attaches an approved agent-policy request ID. Invalid IDs are ignored.
func WithApproval(ctx context.Context, id string) context.Context {
	if !ValidApprovalID(id) {
		return ctx
	}
	return context.WithValue(ctx, approvalKey{}, id)
}

// ApprovalFrom returns the approval ID for the Hakopod-Approval header, if any.
func ApprovalFrom(ctx context.Context) string {
	id, _ := ctx.Value(approvalKey{}).(string)
	return id
}
