package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeResolver struct {
	cnames map[string]string
	hosts  map[string][]string
	txt    map[string][]string
}

func (f fakeResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if cname, ok := f.cnames[host]; ok {
		return cname, nil
	}
	return "", errors.New("no such host")
}

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if addresses, ok := f.hosts[host]; ok {
		return addresses, nil
	}
	return nil, errors.New("no such host")
}

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if records, ok := f.txt[name]; ok {
		return records, nil
	}
	return nil, errors.New("no such host")
}

func TestVerifyHostnameDNS(t *testing.T) {
	const target = "web-0123456789ab.apps.example.test"
	const token = "4f9c2a7e0b13d58c6a1e9f20b7d4c3e18a5f6b2c9d0e7f1a3b4c5d6e7f8a9b0c"
	r := fakeResolver{
		cnames: map[string]string{
			"direct.customer.com": target + ".",
			// A chain whose end is the target, which is itself an A record.
			"chained.customer.com": target + ".",
			"wrong.customer.com":   "elsewhere.example.",
			// Ends past the target at its load balancer: only addresses match.
			"past.customer.com": "lb.cloud.example.",
		},
		hosts: map[string][]string{
			target:                      {"192.0.2.10", "192.0.2.11"},
			"direct.customer.com":       {"192.0.2.10", "192.0.2.11"},
			"chained.customer.com":      {"192.0.2.10", "192.0.2.11"},
			"past.customer.com":         {"192.0.2.11"},
			"apex.customer.com":         {"192.0.2.10"},
			"apex-no-txt.customer.com":  {"192.0.2.10"},
			"apex-bad-txt.customer.com": {"192.0.2.10"},
			"wrong.customer.com":        {"198.51.100.7"},
			"other-ip.customer.com":     {"198.51.100.8"},
		},
		txt: map[string][]string{
			"_hakopod-challenge.apex.customer.com":         {"unrelated", token},
			"_hakopod-challenge.past.customer.com":         {token},
			"_hakopod-challenge.apex-bad-txt.customer.com": {"another-tenant-token"},
			"_hakopod-challenge.wrong.customer.com":        {token},
		},
	}
	for host, want := range map[string]struct {
		ok     bool
		reason string
	}{
		"direct.customer.com":       {true, ""},
		"chained.customer.com":      {true, ""},
		"apex.customer.com":         {true, ""},
		"past.customer.com":         {true, ""},
		"apex-no-txt.customer.com":  {false, "was not found"},
		"apex-bad-txt.customer.com": {false, "does not hold this hostname's verification value"},
		"wrong.customer.com":        {false, "does not point at"},
		"other-ip.customer.com":     {false, "does not point at"},
		"missing.customer.com":      {false, "does not resolve yet"},
	} {
		ok, reason := verifyHostnameDNS(context.Background(), r, host, target, target, token)
		if ok != want.ok || !strings.Contains(reason, want.reason) {
			t.Errorf("%s: matched=%v reason=%q, want %v and %q", host, ok, reason, want.ok, want.reason)
		}
		if !ok && !strings.Contains(reason, target) {
			t.Errorf("%s: the reason must name the CNAME target: %q", host, reason)
		}
	}
	if ok, reason := verifyHostnameDNS(context.Background(), r, "apex-no-txt.customer.com", target, target, token); ok || !strings.Contains(reason, "_hakopod-challenge.apex-no-txt.customer.com") {
		t.Fatal("the reason must name the missing TXT record", reason)
	}
	// With a custom target that CNAMEs to the generated hostname, the chain the
	// resolver reports ends at the generated hostname; both belong to the service.
	if ok, reason := verifyHostnameDNS(context.Background(), r, "direct.customer.com", "stores.acme-cdn.example", target, token); !ok {
		t.Fatal("a chain through a custom target must verify", reason)
	}
	// A row without a token (created before tokens existed) verifies only by CNAME.
	if ok, _ := verifyHostnameDNS(context.Background(), fakeResolver{hosts: r.hosts, txt: map[string][]string{"_hakopod-challenge.apex.customer.com": {""}}}, "apex.customer.com", target, target, ""); ok {
		t.Fatal("an empty token must never match")
	}
	if ok, reason := verifyHostnameDNS(context.Background(), fakeResolver{hosts: map[string][]string{"a.customer.com": {"192.0.2.10"}}}, "a.customer.com", target, target, token); ok || !strings.Contains(reason, "target") {
		t.Fatal("an unresolvable target must not verify anything", reason)
	}
}
