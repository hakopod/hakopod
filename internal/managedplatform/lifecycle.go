package managedplatform

import "context"

// DurableOperation is the immutable identity of one PostgreSQL-leased
// reconciliation. Implementations must fence every mutation with the current
// lease, platform revision, and reviewed authority.
type DurableOperation struct {
	ID         string
	PlatformID string
	Revision   int64
	Kind       string
}

// DurableResourceClaim records ownership of an external identity. Generation
// is Hakopod's immutable ownership generation; mutable provider generations
// belong in observations instead.
type DurableResourceClaim struct {
	PlatformID          string
	PlatformRevision    int64
	Component           string
	Kind                string
	ResourceID          string
	ImmutableGeneration int64
	OwnerOperationID    string
}

// DurableResourceIntent reserves one exact provider-facing key before an
// external mutation. Confirmed remains false until a provider response has
// supplied the immutable identity recorded in a DurableResourceClaim.
type DurableResourceIntent struct {
	ID               string
	PlatformID       string
	PlatformRevision int64
	Component        string
	Kind             string
	ExternalKey      string
	OwnerOperationID string
	Confirmed        bool
}

// DurableLifecycle is the production persistence boundary for managed
// platform reconcilers. It deliberately exposes no SQL or credentials.
type DurableLifecycle interface {
	Operation() DurableOperation
	Heartbeat(context.Context) error
	Claims(context.Context, int64) ([]DurableResourceClaim, error)
	Reserve(context.Context, DurableResourceIntent) (DurableResourceIntent, error)
	Intents(context.Context, int64) ([]DurableResourceIntent, error)
	Confirm(context.Context, DurableResourceIntent, DurableResourceClaim) error
	Cancel(context.Context, DurableResourceIntent) error
	Claim(context.Context, DurableResourceClaim) error
	Advance(context.Context, DurableResourceClaim) (DurableResourceClaim, error)
	Verify(context.Context, DurableResourceClaim) error
	Release(context.Context, DurableResourceClaim) error
}
