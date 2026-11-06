// Package agentaccess evaluates agent policies: rules that block a machine
// key's request or hold it until a person approves that exact request. It has
// no storage or HTTP dependencies; callers load published policies and record
// the decision.
package agentaccess

import "time"

const SchemaVersion = 1

// Condition fields. Team and key values are ids; category and operation come
// from the API contract's operation catalog.
const (
	FieldTeam        = "team"
	FieldKey         = "key"
	FieldCategory    = "category"
	FieldOperation   = "operation"
	FieldProject     = "project"
	FieldEnvironment = "environment"
)

// Operators. Matches accepts '*' as a wildcard for any run of characters.
const (
	OpIsAnyOf  = "is_any_of"
	OpIsNoneOf = "is_none_of"
	OpMatches  = "matches"
)

const (
	EffectBlock           = "block"
	EffectRequireApproval = "require_approval"
)

const (
	ApproverProjectAdmins = "project_admins"
	ApproverTeam          = "team"
	ApproverIdentity      = "identity"
)

// Policy status. Monitor records what would have happened and never blocks.
const (
	StatusDraft    = "draft"
	StatusEnforced = "enforced"
	StatusMonitor  = "monitor"
)

// Decisions recorded for every governed request.
const (
	DecisionAllow           = "allow"
	DecisionBlock           = "block"
	DecisionRequireApproval = "require_approval"
	DecisionApproved        = "approved"
	DecisionException       = "exception"
	DecisionMonitorBlock    = "monitor_block"
	DecisionMonitorApproval = "monitor_approval"
)

// Bounds keep documents small enough to evaluate on every request.
const (
	MaxConditions           = 8
	MaxValues               = 16
	MaxValueLength          = 128
	MaxApprovers            = 8
	MaxDescriptionLength    = 200
	MinApprovalTTLSeconds   = 300
	MaxApprovalTTLSeconds   = 86400
	MinCredentialTTLSeconds = 300
	MaxCredentialTTLSeconds = 30 * 86400
	MaxPublishedPolicies    = 100
)

type Document struct {
	SchemaVersion int         `json:"schema_version"`
	Description   string      `json:"description"`
	When          []Condition `json:"when"`
	Then          Then        `json:"then"`
	Except        *Except     `json:"except,omitempty"`
}

// Condition matches when any value matches (is_any_of, matches) or when no
// value matches (is_none_of). An empty subject matches only is_none_of. All
// conditions of a document must match.
type Condition struct {
	Field    string   `json:"field"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type Then struct {
	Effect             string     `json:"effect"`
	Approvers          []Approver `json:"approvers,omitempty"`
	ApprovalTTLSeconds int        `json:"approval_ttl_seconds,omitempty"`
	// MaxCredentialTTLSeconds caps the expiry of keys that a matched request
	// creates or rotates (createKey, rotateKey). Requests asking for a longer
	// or never-expiring key are refused.
	MaxCredentialTTLSeconds int `json:"max_credential_ttl_seconds,omitempty"`
}

// Approver ID is empty for project_admins, a team id or an identity id.
type Approver struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// Except allows a matched request while an alarm incident is active in the
// request's project and environment. The request is recorded and announced.
type Except struct {
	ActiveIncident bool `json:"active_incident"`
}

// Published is one enforced or monitored policy version.
type Published struct {
	ID       string
	Name     string
	Version  int
	Status   string
	Document Document
}

// Request describes one governed machine-key request.
type Request struct {
	KeyID          string
	IdentityID     string
	TeamIDs        []string
	Operation      string
	Category       string
	Project        string
	Environment    string
	ActiveIncident bool
	Now            time.Time
}

// Match is one policy whose conditions matched the request.
type Match struct {
	PolicyID  string `json:"policy_id"`
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Effect    string `json:"effect"`
	Exception bool   `json:"exception"`
}

// Decision is the outcome for a request. Policy is the enforced match that
// decided it; it is nil when the decision is allow without an exception.
type Decision struct {
	Decision string  `json:"decision"`
	Policy   *Match  `json:"policy,omitempty"`
	Matches  []Match `json:"matches"`
	Reason   string  `json:"reason"`
}
