package agentaccess

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxApproverIDLength = 128

var fields = map[string]bool{FieldTeam: true, FieldKey: true, FieldCategory: true, FieldOperation: true, FieldProject: true, FieldEnvironment: true}

// Validate reports the first problem in the document, naming the field.
func (d Document) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if utf8.RuneCountInString(d.Description) > MaxDescriptionLength {
		return fmt.Errorf("description must be at most %d characters", MaxDescriptionLength)
	}
	if len(d.When) == 0 || len(d.When) > MaxConditions {
		return fmt.Errorf("when must have between 1 and %d conditions", MaxConditions)
	}
	seenField := map[string]bool{}
	for i, c := range d.When {
		if !fields[c.Field] {
			return fmt.Errorf("when[%d].field %q is not a known field", i, c.Field)
		}
		if seenField[c.Field] {
			return fmt.Errorf("when[%d].field %q appears more than once", i, c.Field)
		}
		seenField[c.Field] = true
		if c.Operator != OpIsAnyOf && c.Operator != OpIsNoneOf && c.Operator != OpMatches {
			return fmt.Errorf("when[%d].operator %q is not a known operator", i, c.Operator)
		}
		if len(c.Values) == 0 || len(c.Values) > MaxValues {
			return fmt.Errorf("when[%d].values must have between 1 and %d values", i, MaxValues)
		}
		seen := map[string]bool{}
		for j, v := range c.Values {
			if err := checkValue(c.Operator, v); err != nil {
				return fmt.Errorf("when[%d].values[%d] %s", i, j, err)
			}
			if seen[v] {
				return fmt.Errorf("when[%d].values[%d] %q is a duplicate", i, j, v)
			}
			seen[v] = true
		}
	}
	t := d.Then
	switch t.Effect {
	case EffectBlock:
		if len(t.Approvers) > 0 {
			return fmt.Errorf("then.approvers must be empty when the effect is block")
		}
		if t.ApprovalTTLSeconds != 0 {
			return fmt.Errorf("then.approval_ttl_seconds must be unset when the effect is block")
		}
	case EffectRequireApproval:
		if len(t.Approvers) > MaxApprovers {
			return fmt.Errorf("then.approvers must have at most %d entries", MaxApprovers)
		}
		if t.ApprovalTTLSeconds < MinApprovalTTLSeconds || t.ApprovalTTLSeconds > MaxApprovalTTLSeconds {
			return fmt.Errorf("then.approval_ttl_seconds must be between %d and %d", MinApprovalTTLSeconds, MaxApprovalTTLSeconds)
		}
		seen := map[Approver]bool{}
		for i, a := range t.Approvers {
			switch a.Kind {
			case ApproverProjectAdmins:
				if a.ID != "" {
					return fmt.Errorf("then.approvers[%d].id must be empty for project_admins", i)
				}
			case ApproverTeam, ApproverIdentity:
				if a.ID == "" || len(a.ID) > maxApproverIDLength {
					return fmt.Errorf("then.approvers[%d].id must be between 1 and %d characters", i, maxApproverIDLength)
				}
			default:
				return fmt.Errorf("then.approvers[%d].kind %q is not a known approver kind", i, a.Kind)
			}
			if seen[a] {
				return fmt.Errorf("then.approvers[%d] is a duplicate", i)
			}
			seen[a] = true
		}
	default:
		return fmt.Errorf("then.effect %q must be block or require_approval", t.Effect)
	}
	if c := t.MaxCredentialTTLSeconds; c != 0 && (c < MinCredentialTTLSeconds || c > MaxCredentialTTLSeconds) {
		return fmt.Errorf("then.max_credential_ttl_seconds must be between %d and %d", MinCredentialTTLSeconds, MaxCredentialTTLSeconds)
	}
	return nil
}

func checkValue(op, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(v) > MaxValueLength {
		return fmt.Errorf("must be at most %d bytes", MaxValueLength)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("must not contain control characters")
		}
	}
	if op == OpMatches && strings.ContainsAny(v, `?[]\`) {
		return fmt.Errorf("may use only '*' as a wildcard")
	}
	if op != OpMatches && strings.Contains(v, "*") {
		return fmt.Errorf("contains '*'; use the matches operator for wildcards")
	}
	return nil
}

// MatchPattern reports whether value matches pattern, where '*' matches any run of
// characters and everything else is literal and case-sensitive. It uses the
// two-pointer wildcard walk, so it never backtracks exponentially.
func MatchPattern(pattern, value string) bool {
	p, v := 0, 0
	star, mark := -1, 0
	for v < len(value) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, v
			p++
		case p < len(pattern) && pattern[p] == value[v]:
			p++
			v++
		case star >= 0:
			p = star + 1
			mark++
			v = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// Matches reports whether every condition matches the request.
func (d Document) Matches(r Request) bool {
	for _, c := range d.When {
		if !c.matches(r) {
			return false
		}
	}
	return true
}

func (c Condition) matches(r Request) bool {
	var subjects []string
	switch c.Field {
	case FieldTeam:
		subjects = r.TeamIDs
	case FieldKey:
		subjects = []string{r.KeyID}
	case FieldCategory, FieldOperation:
		s := r.Category
		if c.Field == FieldOperation {
			s = r.Operation
		}
		subjects = []string{s}
	case FieldProject:
		subjects = []string{r.Project}
	case FieldEnvironment:
		subjects = []string{r.Environment}
	default:
		return false
	}
	// An empty subject (unclassified operation, no project or environment, no
	// teams) never equals a listed value: is_none_of matches it, is_any_of and
	// matches do not.
	if !slices.ContainsFunc(subjects, func(s string) bool { return s != "" }) {
		return c.Operator == OpIsNoneOf
	}
	hit := false
	for _, s := range subjects {
		for _, v := range c.Values {
			if (c.Operator == OpMatches && MatchPattern(v, s)) || (c.Operator != OpMatches && v == s) {
				hit = true
			}
		}
	}
	if c.Operator == OpIsNoneOf {
		return !hit
	}
	return hit
}

// Evaluate decides a request against published policies. Enforced matches
// decide with block ahead of require_approval; an incident exception only
// downgrades the excepted policy; monitor matches are recorded but never act.
func Evaluate(r Request, policies []Published) Decision {
	d := Decision{Matches: []Match{}}
	for _, p := range policies {
		if p.Status != StatusEnforced && p.Status != StatusMonitor {
			continue
		}
		if !p.Document.Matches(r) {
			continue
		}
		d.Matches = append(d.Matches, Match{
			PolicyID:  p.ID,
			Name:      p.Name,
			Version:   p.Version,
			Status:    p.Status,
			Effect:    p.Document.Then.Effect,
			Exception: p.Document.Except != nil && p.Document.Except.ActiveIncident && r.ActiveIncident,
		})
	}
	find := func(status, effect string, exception bool) *Match {
		for _, m := range d.Matches {
			if m.Status == status && m.Exception == exception && (effect == "" || m.Effect == effect) {
				m := m
				return &m
			}
		}
		return nil
	}
	label := func(m *Match) string { return fmt.Sprintf("%q v%d", m.Name, m.Version) }
	if m := find(StatusEnforced, EffectBlock, false); m != nil {
		d.Decision, d.Policy, d.Reason = DecisionBlock, m, "Blocked by "+label(m)+"."
	} else if m := find(StatusEnforced, EffectRequireApproval, false); m != nil {
		d.Decision, d.Policy, d.Reason = DecisionRequireApproval, m, "Held for approval by "+label(m)+"."
	} else if m := find(StatusEnforced, "", true); m != nil {
		d.Decision, d.Policy, d.Reason = DecisionException, m, "Allowed because an alarm is active; "+label(m)+" has an incident exception."
	} else if m := find(StatusMonitor, EffectBlock, false); m != nil {
		d.Decision, d.Policy, d.Reason = DecisionMonitorBlock, m, label(m)+" would block this request (monitor)."
	} else if m := find(StatusMonitor, EffectRequireApproval, false); m != nil {
		d.Decision, d.Policy, d.Reason = DecisionMonitorApproval, m, label(m)+" would hold this request for approval (monitor)."
	} else {
		d.Decision, d.Reason = DecisionAllow, "No policy matches."
	}
	return d
}

// Restrictiveness ranks a decision: block > require_approval > monitor_block >
// monitor_approval > exception > allow. Unknown decisions rank lowest.
func Restrictiveness(decision string) int {
	switch decision {
	case DecisionBlock:
		return 5
	case DecisionRequireApproval:
		return 4
	case DecisionMonitorBlock:
		return 3
	case DecisionMonitorApproval:
		return 2
	case DecisionException:
		return 1
	}
	return 0
}

// RequestDigest identifies one exact request. An approval is bound to this
// digest, so it covers only a request with the same key, method, path, query
// and body. Each part is length-prefixed (8-byte big-endian) so parts cannot
// run into each other.
func RequestDigest(keyID, method, path, rawQuery string, body []byte) [32]byte {
	h := sha256.New()
	var n [8]byte
	for _, part := range [][]byte{[]byte(keyID), []byte(strings.ToUpper(method)), []byte(path), []byte(rawQuery), body} {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// CapsCredential returns the credential expiry cap and whether one is set.
func CapsCredential(d Document) (time.Duration, bool) {
	s := d.Then.MaxCredentialTTLSeconds
	return time.Duration(s) * time.Second, s > 0
}
