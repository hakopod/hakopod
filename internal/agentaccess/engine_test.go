package agentaccess

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validDoc() Document {
	return Document{
		SchemaVersion: SchemaVersion,
		When:          []Condition{{Field: FieldOperation, Operator: OpIsAnyOf, Values: []string{"deploy"}}},
		Then:          Then{Effect: EffectRequireApproval, ApprovalTTLSeconds: 600, Approvers: []Approver{{Kind: ApproverProjectAdmins}, {Kind: ApproverTeam, ID: "t1"}}},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Document)
		ok   bool
	}{
		{"valid approval", func(d *Document) {}, true},
		{"valid block with cap", func(d *Document) { d.Then = Then{Effect: EffectBlock, MaxCredentialTTLSeconds: 3600} }, true},
		{"valid matches star", func(d *Document) { d.When[0] = Condition{FieldProject, OpMatches, []string{"prod-*"}} }, true},
		{"no approvers", func(d *Document) { d.Then.Approvers = nil }, true},
		{"schema", func(d *Document) { d.SchemaVersion = 2 }, false},
		{"description", func(d *Document) { d.Description = strings.Repeat("x", 201) }, false},
		{"no conditions", func(d *Document) { d.When = nil }, false},
		{"too many conditions", func(d *Document) { d.When = make([]Condition, 9) }, false},
		{"bad field", func(d *Document) { d.When[0].Field = "nope" }, false},
		{"bad operator", func(d *Document) { d.When[0].Operator = "eq" }, false},
		{"no values", func(d *Document) { d.When[0].Values = nil }, false},
		{"too many values", func(d *Document) { d.When[0].Values = strings.Split(strings.Repeat("a,", 16)+"b", ",") }, false},
		{"blank value", func(d *Document) { d.When[0].Values = []string{"  "} }, false},
		{"long value", func(d *Document) { d.When[0].Values = []string{strings.Repeat("a", 129)} }, false},
		{"control char", func(d *Document) { d.When[0].Values = []string{"a\nb"} }, false},
		{"duplicate value", func(d *Document) { d.When[0].Values = []string{"a", "a"} }, false},
		{"glob meta", func(d *Document) { d.When[0] = Condition{FieldProject, OpMatches, []string{"a?"}} }, false},
		{"star in any_of", func(d *Document) { d.When[0].Values = []string{"a*"} }, false},
		{"duplicate field", func(d *Document) { d.When = append(d.When, d.When[0]) }, false},
		{"bad effect", func(d *Document) { d.Then.Effect = "allow" }, false},
		{"ttl low", func(d *Document) { d.Then.ApprovalTTLSeconds = 10 }, false},
		{"ttl high", func(d *Document) { d.Then.ApprovalTTLSeconds = 86401 }, false},
		{"too many approvers", func(d *Document) { d.Then.Approvers = make([]Approver, 9) }, false},
		{"admins with id", func(d *Document) { d.Then.Approvers = []Approver{{ApproverProjectAdmins, "x"}} }, false},
		{"team without id", func(d *Document) { d.Then.Approvers = []Approver{{Kind: ApproverTeam}} }, false},
		{"long id", func(d *Document) { d.Then.Approvers = []Approver{{ApproverIdentity, strings.Repeat("i", 129)}} }, false},
		{"bad kind", func(d *Document) { d.Then.Approvers = []Approver{{Kind: "anyone"}} }, false},
		{"duplicate approver", func(d *Document) { d.Then.Approvers = []Approver{{ApproverTeam, "t"}, {ApproverTeam, "t"}} }, false},
		{"block with approvers", func(d *Document) { d.Then.Effect = EffectBlock; d.Then.ApprovalTTLSeconds = 0 }, false},
		{"block with ttl", func(d *Document) { d.Then = Then{Effect: EffectBlock, ApprovalTTLSeconds: 600} }, false},
		{"credential cap low", func(d *Document) { d.Then.MaxCredentialTTLSeconds = 10 }, false},
	}
	for _, c := range cases {
		d := validDoc()
		c.edit(&d)
		if err := d.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: got %v", c.name, err)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		p, v string
		want bool
	}{
		{"", "", true}, {"", "a", false}, {"*", "", true}, {"*", "anything", true},
		{"prod-*", "prod-eu", true}, {"*-eu", "prod-eu", true}, {"a*c", "abbbc", true},
		{"a*c", "abcd", false}, {"abc", "abc", true}, {"abc", "ABC", false}, {"a*b*c", "aXbYc", true},
	}
	for _, c := range cases {
		if MatchPattern(c.p, c.v) != c.want {
			t.Errorf("MatchPattern(%q,%q) want %v", c.p, c.v, c.want)
		}
	}
	start := time.Now()
	if MatchPattern(strings.Repeat("*a", 50)+"b", strings.Repeat("a", 10000)) {
		t.Fatal("pathological pattern matched")
	}
	if time.Since(start) > time.Second {
		t.Fatal("pathological pattern was slow")
	}
}

func TestMatches(t *testing.T) {
	r := Request{KeyID: "k1", TeamIDs: []string{"t1", "t2"}, Operation: "deploy", Category: "deployments", Project: "shop", Environment: "prod"}
	cases := []struct {
		c    Condition
		r    Request
		want bool
	}{
		{Condition{FieldTeam, OpIsAnyOf, []string{"t2"}}, r, true},
		{Condition{FieldTeam, OpIsAnyOf, []string{"t3"}}, r, false},
		{Condition{FieldTeam, OpIsNoneOf, []string{"t3"}}, r, true},
		{Condition{FieldTeam, OpIsNoneOf, []string{"t1"}}, r, false},
		{Condition{FieldTeam, OpMatches, []string{"t*"}}, r, true},
		{Condition{FieldTeam, OpIsNoneOf, []string{"t1"}}, Request{}, true},
		{Condition{FieldKey, OpIsAnyOf, []string{"k1"}}, r, true},
		{Condition{FieldCategory, OpMatches, []string{"deploy*"}}, r, true},
		{Condition{FieldOperation, OpIsNoneOf, []string{"read"}}, r, true},
		{Condition{FieldProject, OpIsAnyOf, []string{"other"}}, r, false},
		{Condition{FieldEnvironment, OpMatches, []string{"pr*"}}, r, true},
		{Condition{FieldOperation, OpIsNoneOf, []string{"read"}}, Request{Category: "x"}, true},
		{Condition{FieldOperation, OpMatches, []string{"*"}}, Request{}, false},
		{Condition{FieldCategory, OpIsNoneOf, []string{"x"}}, Request{}, true},
	}
	for i, c := range cases {
		if got := (Document{When: []Condition{c.c}}).Matches(c.r); got != c.want {
			t.Errorf("case %d %+v: got %v", i, c.c, got)
		}
	}
	two := Document{When: []Condition{{FieldKey, OpIsAnyOf, []string{"k1"}}, {FieldProject, OpIsAnyOf, []string{"other"}}}}
	if two.Matches(r) {
		t.Error("all conditions must match")
	}
}

func pol(id, status, effect string, except bool) Published {
	d := Document{When: []Condition{{FieldKey, OpIsAnyOf, []string{"k"}}}, Then: Then{Effect: effect}}
	if except {
		d.Except = &Except{ActiveIncident: true}
	}
	return Published{ID: id, Name: id, Version: 1, Status: status, Document: d}
}

func TestEmptySubjects(t *testing.T) {
	for _, c := range []struct {
		c    Condition
		want bool
	}{
		{Condition{FieldProject, OpMatches, []string{"*"}}, false},
		{Condition{FieldProject, OpIsAnyOf, []string{"shop"}}, false},
		{Condition{FieldProject, OpIsNoneOf, []string{"shop"}}, true},
		{Condition{FieldTeam, OpIsNoneOf, []string{"t1"}}, true},
		{Condition{FieldTeam, OpMatches, []string{"*"}}, false},
	} {
		if got := c.c.matches(Request{}); got != c.want {
			t.Fatalf("%+v: got %v", c.c, got)
		}
	}
}

func TestRestrictiveness(t *testing.T) {
	order := []string{DecisionAllow, DecisionException, DecisionMonitorApproval, DecisionMonitorBlock, DecisionRequireApproval, DecisionBlock}
	for i := 1; i < len(order); i++ {
		if Restrictiveness(order[i]) <= Restrictiveness(order[i-1]) {
			t.Fatalf("%s not above %s", order[i], order[i-1])
		}
	}
}

func TestEvaluate(t *testing.T) {
	r := Request{KeyID: "k"}
	inc := Request{KeyID: "k", ActiveIncident: true}
	cases := []struct {
		name     string
		r        Request
		ps       []Published
		want, by string
		matches  int
	}{
		{"block over approval", r, []Published{pol("a", StatusEnforced, EffectRequireApproval, false), pol("b", StatusEnforced, EffectBlock, false)}, DecisionBlock, "b", 2},
		{"enforced over monitor", r, []Published{pol("m", StatusMonitor, EffectBlock, false), pol("e", StatusEnforced, EffectRequireApproval, false)}, DecisionRequireApproval, "e", 2},
		{"exception", inc, []Published{pol("x", StatusEnforced, EffectBlock, true)}, DecisionException, "x", 1},
		{"exception without incident", r, []Published{pol("x", StatusEnforced, EffectBlock, true)}, DecisionBlock, "x", 1},
		{"other block still blocks", inc, []Published{pol("x", StatusEnforced, EffectBlock, true), pol("b", StatusEnforced, EffectBlock, false)}, DecisionBlock, "b", 2},
		{"monitor block", r, []Published{pol("ma", StatusMonitor, EffectRequireApproval, false), pol("mb", StatusMonitor, EffectBlock, false)}, DecisionMonitorBlock, "mb", 2},
		{"monitor approval", r, []Published{pol("ma", StatusMonitor, EffectRequireApproval, false)}, DecisionMonitorApproval, "ma", 1},
		{"draft skipped", r, []Published{pol("d", StatusDraft, EffectBlock, false)}, DecisionAllow, "", 0},
		{"no match", Request{KeyID: "z"}, []Published{pol("b", StatusEnforced, EffectBlock, false)}, DecisionAllow, "", 0},
	}
	for _, c := range cases {
		d := Evaluate(c.r, c.ps)
		by := ""
		if d.Policy != nil {
			by = d.Policy.PolicyID
		}
		if d.Decision != c.want || by != c.by || len(d.Matches) != c.matches || d.Reason == "" {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
	d := Evaluate(r, nil)
	b, _ := json.Marshal(d)
	if d.Decision != DecisionAllow || d.Policy != nil || !strings.Contains(string(b), `"matches":[]`) {
		t.Errorf("empty input: %s", b)
	}
	p := pol("b", StatusEnforced, EffectBlock, false)
	p.Name, p.Version = "Weekend change freeze", 3
	if got := Evaluate(r, []Published{p}).Reason; got != `Blocked by "Weekend change freeze" v3.` {
		t.Errorf("reason %q", got)
	}
}

func TestRequestDigest(t *testing.T) {
	base := RequestDigest("k", "post", "/p", "q=1", []byte("b"))
	if base != RequestDigest("k", "POST", "/p", "q=1", []byte("b")) {
		t.Error("digest not stable")
	}
	for i, d := range [][32]byte{
		RequestDigest("k2", "post", "/p", "q=1", []byte("b")),
		RequestDigest("k", "get", "/p", "q=1", []byte("b")),
		RequestDigest("k", "post", "/p2", "q=1", []byte("b")),
		RequestDigest("k", "post", "/p", "q=2", []byte("b")),
		RequestDigest("k", "post", "/p", "q=1", []byte("c")),
	} {
		if d == base {
			t.Errorf("component %d change did not change digest", i)
		}
	}
	if RequestDigest("ab", "c", "", "", nil) == RequestDigest("a", "bc", "", "", nil) {
		t.Error("length prefix collision")
	}
}

func TestCapsCredential(t *testing.T) {
	if _, ok := CapsCredential(Document{}); ok {
		t.Error("unset cap reported")
	}
	if d, ok := CapsCredential(Document{Then: Then{MaxCredentialTTLSeconds: 600}}); !ok || d != 10*time.Minute {
		t.Error("cap wrong")
	}
}
