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

func TestVerifyHostnameDNS(t *testing.T) {
	const target = "web-0123456789ab.apps.example.test"
	r := fakeResolver{
		cnames: map[string]string{
			"direct.customer.com": target + ".",
			// A chain resolves to the target's own CNAME, its load balancer.
			"chained.customer.com": "lb.cloud.example.",
			"wrong.customer.com":   "elsewhere.example.",
		},
		hosts: map[string][]string{
			target:                  {"192.0.2.10", "192.0.2.11"},
			"direct.customer.com":   {"192.0.2.10", "192.0.2.11"},
			"chained.customer.com":  {"192.0.2.11"},
			"apex.customer.com":     {"192.0.2.10"},
			"wrong.customer.com":    {"198.51.100.7"},
			"other-ip.customer.com": {"198.51.100.8"},
		},
	}
	for host, want := range map[string]bool{
		"direct.customer.com":   true,
		"chained.customer.com":  true,
		"apex.customer.com":     true,
		"wrong.customer.com":    false,
		"other-ip.customer.com": false,
		"missing.customer.com":  false,
	} {
		ok, reason := verifyHostnameDNS(context.Background(), r, host, target)
		if ok != want {
			t.Errorf("%s: matched=%v, want %v (%s)", host, ok, want, reason)
		}
		if !ok && !strings.Contains(reason, target) {
			t.Errorf("%s: the reason must name the CNAME target: %q", host, reason)
		}
	}
	if ok, reason := verifyHostnameDNS(context.Background(), fakeResolver{hosts: map[string][]string{"a.customer.com": {"192.0.2.10"}}}, "a.customer.com", target); ok || !strings.Contains(reason, "target") {
		t.Fatal("an unresolvable target must not verify anything", reason)
	}
}
