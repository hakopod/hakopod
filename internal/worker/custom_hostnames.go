package worker

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

type hostnameResolver interface {
	LookupCNAME(context.Context, string) (string, error)
	LookupHost(context.Context, string) ([]string, error)
	LookupTXT(context.Context, string) ([]string, error)
}

// The pure Go resolver asks the configured name servers for every CNAME,
// address and TXT check and keeps no cache, so an early miss cannot stick
// until a negative TTL expires.
var hostnameDNS hostnameResolver = &net.Resolver{PreferGo: true}

type customHostnameRuntime interface {
	ServiceHostname(cluster.Target, string) string
	ReconcileCustomHostnames(context.Context, cluster.Target, string, []string) error
	CustomHostnameCertificates(context.Context, cluster.Target, string) (map[string]cluster.CustomHostnameCertificate, bool, error)
}

// verifyHostnameDNS accepts a hostname whose CNAME chain ends at target: the
// target is specific to this service, so the CNAME shows the domain's owner
// meant it. A hostname that only resolves to one of target's addresses (an
// apex ALIAS or flattened CNAME) also needs the per-hostname token in a TXT
// record, because the shared ingress address says nothing about which service
// the owner chose; without it, any app could claim a domain aimed at another.
func verifyHostnameDNS(ctx context.Context, r hostnameResolver, host, target, generated, token string) (bool, string) {
	// The resolver reports only the end of a CNAME chain, so a custom target that
	// itself points at the generated hostname ends there; both are this service's.
	if cname, err := r.LookupCNAME(ctx, host); err == nil && (strings.EqualFold(strings.TrimSuffix(cname, "."), target) || strings.EqualFold(strings.TrimSuffix(cname, "."), generated)) {
		return true, ""
	}
	addresses, err := r.LookupHost(ctx, host)
	if err != nil || len(addresses) == 0 {
		return false, fmt.Sprintf("%s does not resolve yet. Add a CNAME record pointing at %s.", host, target)
	}
	expected, err := r.LookupHost(ctx, target)
	if err != nil {
		return false, fmt.Sprintf("The target %s does not resolve.", target)
	}
	if !slices.ContainsFunc(addresses, func(a string) bool { return slices.Contains(expected, a) }) {
		return false, fmt.Sprintf("%s does not point at %s. Replace its records with a CNAME record pointing at %s.", host, target, target)
	}
	name := store.HostnameChallengeName(host)
	records, err := r.LookupTXT(ctx, name)
	if err == nil && token != "" {
		for _, value := range records {
			if subtle.ConstantTimeCompare([]byte(value), []byte(token)) == 1 {
				return true, ""
			}
		}
	}
	if err != nil || len(records) == 0 {
		return false, fmt.Sprintf("%s reaches %s by address but not through a CNAME, so the TXT record %s is also needed and was not found. Add it, or use a CNAME record instead.", host, target, name)
	}
	return false, fmt.Sprintf("%s reaches %s by address but not through a CNAME, and the TXT record %s does not hold this hostname's verification value.", host, target, name)
}

// runCustomHostnames advances one batch of due hostnames: DNS checks with
// backoff, routing and certificates, and removal.
func (w *Worker) runCustomHostnames(ctx context.Context) {
	runtime, ok := w.background().(customHostnameRuntime)
	if !ok {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	claimed, err := w.Store.ClaimCustomHostnames(bounded, 100)
	if err != nil || len(claimed) == 0 {
		return
	}
	type key struct{ app, service string }
	groups := map[key][]store.CustomHostname{}
	order := []key{}
	for _, h := range claimed {
		k := key{h.ApplicationID, h.Service}
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], h)
	}
	for _, k := range order {
		if bounded.Err() != nil {
			return
		}
		w.customHostnameService(bounded, runtime, k.app, k.service, groups[k])
	}
}

// customHostnameService handles the claimed hostnames of one service. Any
// error leaves them leased; they are tried again when the lease expires.
func (w *Worker) customHostnameService(ctx context.Context, runtime customHostnameRuntime, appID, service string, items []store.CustomHostname) {
	a, err := w.Store.Application(ctx, appID)
	if err != nil {
		return
	}
	if a.Spec, err = w.Store.ObservedSpec(ctx, a); err != nil {
		return
	}
	target := cluster.Target{Project: a.Project, Environment: a.Environment, ApplicationID: a.ID, Revision: a.Revision, Spec: a.Spec}
	svc := a.Spec.Services[service]
	generated := runtime.ServiceHostname(target, service)
	dnsTarget := generated
	if svc.CustomHostnames != nil && svc.CustomHostnames.Target != "" {
		dnsTarget = svc.CustomHostnames.Target
	}
	reconcile := false
	issuing := []store.CustomHostname{}
	for _, h := range items {
		switch h.Status {
		case store.HostnamePendingDNS:
			if !spec.CustomHostnamesEnabled(svc) {
				continue // The release that turns the feature on has not run yet.
			}
			lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
			matched, reason := verifyHostnameDNS(lookup, hostnameDNS, h.Hostname, dnsTarget, generated, h.Verification.Value)
			cancel()
			next := store.HostnameAfterDNS(h, matched, reason, time.Now())
			if err := w.Store.SaveCustomHostnameState(ctx, h.Status, next); err != nil {
				continue
			}
			if next.Status == store.HostnameIssuing {
				issuing = append(issuing, next)
			}
		case store.HostnameIssuing:
			issuing = append(issuing, h)
		case store.HostnameDeleting:
			reconcile = true
		}
	}
	if !reconcile && len(issuing) == 0 {
		return
	}
	hosts, asOf, err := w.Store.RoutedCustomHostnames(ctx, appID, service)
	if err != nil {
		return
	}
	if err := runtime.ReconcileCustomHostnames(ctx, target, service, hosts); err != nil {
		slog.Warn("custom hostname routing deferred", "application", appID, "service", service, "error", err)
		return
	}
	if err := w.Store.PurgeDeletedCustomHostnames(ctx, appID, service, asOf); err != nil {
		return
	}
	if len(issuing) == 0 {
		return
	}
	certificates, tls, err := runtime.CustomHostnameCertificates(ctx, target, service)
	if err != nil {
		return
	}
	for _, h := range issuing {
		state, found := certificates[h.Hostname]
		if tls && !found {
			state.Reason = "Waiting for the certificate request."
		}
		_ = w.Store.SaveCustomHostnameState(ctx, store.HostnameIssuing, store.HostnameAfterIssue(h, tls, state.Ready, state.Reason, time.Now()))
	}
}
