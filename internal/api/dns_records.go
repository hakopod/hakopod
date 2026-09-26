package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/dnsprovider"
	"github.com/hakopod/hakopod/internal/store"
)

const (
	// Twenty hostnames is the bulk this endpoint accepts, matching the other
	// bounded multi-item requests.
	maxDNSRecordHostnames = 20
	// One hostname costs a zone lookup plus a read and a write per record. Six
	// seconds is comfortable for that and short enough that one unresponsive
	// hostname cannot eat the whole request.
	dnsRecordHostnameBudget = 6 * time.Second
	// The whole request is bounded too, because twenty hostnames run one after
	// another: the provider client holds a single connection, so running them in
	// parallel would queue on the same socket and buy nothing. Hostnames left
	// when the budget is gone are reported as skipped, which is safe because a
	// retry sees the records that landed as existing.
	dnsRecordsBudget = 60 * time.Second
	// A short TTL on the ownership record keeps a corrected value from waiting
	// out a long cache.
	dnsRecordTTL = 60
)

const (
	dnsRecordCreated  = "created"
	dnsRecordExists   = "exists"
	dnsRecordConflict = "conflict"
	dnsRecordFailed   = "failed"
	dnsRecordSkipped  = "skipped"
)

// A hostname carries two records but one status, so the worst outcome wins.
var dnsRecordRank = map[string]int{dnsRecordSkipped: 0, dnsRecordExists: 1, dnsRecordCreated: 2, dnsRecordConflict: 3, dnsRecordFailed: 4}

// Every message a caller can see is one of these constants. Nothing formats a
// provider's response into a result, so there is no provider text to cap.
var dnsRecordMessages = map[string]string{
	dnsRecordCreated:  "The records were created at the provider. DNS still has to propagate, and this domain stays awaiting verification until the verify step confirms it.",
	dnsRecordExists:   "The records were already present at the provider, unchanged.",
	dnsRecordConflict: "A different record already occupies this name. Check it, then retry with replace_existing to overwrite it.",
}

type dnsRecordView struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type dnsRecordResult struct {
	Hostname string          `json:"hostname"`
	Status   string          `json:"status"`
	Message  string          `json:"message"`
	Records  []dnsRecordView `json:"records"`
}

func (s *Server) registerDNSRecordRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/applications/{id}/domains/dns-providers", s.applicationDNSProviders)
	m.HandleFunc("POST /api/v1/applications/{id}/domains/dns-records", s.createApplicationDNSRecords)
}

// desiredDNSRecords is the same pair of records the Domains page tells a user to
// enter by hand, read from the one place that builds them. No address record is
// ever written: hakopod publishes a service hostname, not an ingress address.
func (s *Server) desiredDNSRecords(a store.Application, d store.DomainVerification, approved map[string]bool) []dnsprovider.Record {
	view := s.domainView(a, d, approved)
	records := []dnsprovider.Record{{Type: "TXT", Name: view.VerificationName, Value: view.VerificationValue, TTL: dnsRecordTTL}}
	if view.Target != "" {
		records = append(records, dnsprovider.Record{Type: "CNAME", Name: d.Hostname, Value: view.Target, TTL: dnsRecordTTL})
	}
	return records
}

func dnsRecordViews(records []dnsprovider.Record) []dnsRecordView {
	views := make([]dnsRecordView, 0, len(records))
	for _, record := range records {
		views = append(views, dnsRecordView{Type: record.Type, Name: record.Name, Value: record.Value})
	}
	return views
}

// dnsFailureMessage turns a sentinel into fixed prose. It never prints
// err.Error() for a provider failure, because internal/dnsprovider collapses
// those to one opaque value precisely so no provider wording, and no token a
// provider quoted back, can reach a response.
func dnsFailureMessage(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "The provider did not answer within the time allowed for this hostname. Retry."
	case errors.Is(err, dnsprovider.ErrInput):
		return "This provider has no zone that can hold this hostname, or the hostname is outside the provider's permitted zones."
	default:
		return "The DNS provider did not accept this change. Check the provider's access and permissions, then retry."
	}
}

// createDNSRecords writes the records for each hostname and reports each one
// separately. It returns no error for a per-hostname failure on purpose: no
// transaction spans a third party, so a request that created eight records and
// was refused on the ninth has to say exactly that. The only error it returns is
// a refusal to use this provider at all.
func createDNSRecords(ctx context.Context, client dnsprovider.Client, provider dnsprovider.Provider, project, environment string, desired map[string][]dnsprovider.Record, hostnames []string, replace bool) ([]dnsRecordResult, error) {
	if !provider.Allows(project, environment) {
		return nil, fmt.Errorf("%w: this DNS provider is not available to this application", store.ErrForbidden)
	}
	results := make([]dnsRecordResult, 0, len(hostnames))
	for _, hostname := range hostnames {
		hostname = strings.ToLower(strings.TrimSpace(hostname))
		records, known := desired[hostname]
		switch {
		case !known:
			// Only a hostname this application is already awaiting or holding
			// verification for can be written, so a provider credential cannot be
			// pointed at a name nobody claimed here.
			results = append(results, dnsRecordResult{Hostname: hostname, Status: dnsRecordSkipped, Message: "This hostname is not awaiting or holding verification for this application. Add the domain first.", Records: []dnsRecordView{}})
		case ctx.Err() != nil:
			results = append(results, dnsRecordResult{Hostname: hostname, Status: dnsRecordSkipped, Message: "The request ran out of time before this hostname. Retry to continue; records that already landed are reported as existing.", Records: dnsRecordViews(records)})
		default:
			status, message := createDNSRecordSet(ctx, client, hostname, records, replace)
			results = append(results, dnsRecordResult{Hostname: hostname, Status: status, Message: message, Records: dnsRecordViews(records)})
		}
	}
	return results, nil
}

func createDNSRecordSet(ctx context.Context, client dnsprovider.Client, hostname string, records []dnsprovider.Record, replace bool) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, dnsRecordHostnameBudget)
	defer cancel()
	zone, err := client.Zone(ctx, hostname)
	if err != nil {
		return dnsRecordFailed, dnsFailureMessage(err)
	}
	status, message := dnsRecordExists, dnsRecordMessages[dnsRecordExists]
	for _, record := range records {
		next, text := applyDNSRecord(ctx, client, zone, record, replace)
		if dnsRecordRank[next] > dnsRecordRank[status] {
			status, message = next, text
		}
	}
	return status, message
}

// applyDNSRecord reads before it writes, so nothing is ever blind-written: an
// identical record is left alone, a different one is a conflict unless the caller
// asked for a replacement, and only the record that was read is replaced.
func applyDNSRecord(ctx context.Context, client dnsprovider.Client, zone dnsprovider.Zone, record dnsprovider.Record, replace bool) (string, string) {
	existing, err := client.Records(ctx, zone, record.Name, record.Type)
	if err != nil {
		return dnsRecordFailed, dnsFailureMessage(err)
	}
	for _, item := range existing {
		if item.Value == record.Value {
			return dnsRecordExists, dnsRecordMessages[dnsRecordExists]
		}
	}
	if len(existing) > 0 {
		if !replace {
			return dnsRecordConflict, dnsRecordMessages[dnsRecordConflict]
		}
		if err = client.Replace(ctx, zone, existing[0], record); err != nil {
			return dnsRecordFailed, dnsFailureMessage(err)
		}
		return dnsRecordCreated, dnsRecordMessages[dnsRecordCreated]
	}
	if err = client.Create(ctx, zone, record); err != nil {
		// A record that appeared between the read and the write is existing, not a
		// failure.
		if errors.Is(err, dnsprovider.ErrExists) {
			return dnsRecordExists, dnsRecordMessages[dnsRecordExists]
		}
		return dnsRecordFailed, dnsFailureMessage(err)
	}
	return dnsRecordCreated, dnsRecordMessages[dnsRecordCreated]
}

func (s *Server) createApplicationDNSRecords(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authorizedApp(w, r, r.PathValue("id"), "deployments:write")
	if !ok {
		return
	}
	if _, ok = backupIdempotency(w, r); !ok {
		return
	}
	var in struct {
		ProviderID      string   `json:"provider_id"`
		Hostnames       []string `json:"hostnames"`
		ReplaceExisting bool     `json:"replace_existing"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Hostnames) == 0 || len(in.Hostnames) > maxDNSRecordHostnames {
		problem(w, 400, "invalid_request", fmt.Sprintf("List between 1 and %d hostnames.", maxDNSRecordHostnames))
		return
	}
	key, ok := s.dnsEncryptionKey(w)
	if !ok {
		return
	}
	provider, err := s.Store.DNSProviderCredential(r.Context(), in.ProviderID)
	if err != nil {
		failure(w, err)
		return
	}
	// The scope is checked before the credential is opened, so a provider this
	// application may not use is never decrypted.
	if !provider.Allows(a.Project, a.Environment) {
		problem(w, 403, "forbidden", "This DNS provider is not available to this application.")
		return
	}
	credentials, err := dnsprovider.OpenCredentials(key, provider)
	if err != nil {
		problem(w, 503, "unavailable", "The stored provider credential cannot be opened with the configured authentication encryption key. Save the provider again.")
		return
	}
	newClient := dnsprovider.NewCloudflare
	if s.dnsClient != nil {
		newClient = s.dnsClient
	}
	client, err := newClient(provider, credentials)
	if err != nil {
		dnsProviderFailure(w, err)
		return
	}
	domains, err := s.Store.DomainVerifications(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	approved, err := s.Store.ApprovedDomains(r.Context(), a.ID)
	if err != nil {
		failure(w, err)
		return
	}
	desired := map[string][]dnsprovider.Record{}
	for _, d := range domains {
		desired[d.Hostname] = s.desiredDNSRecords(a, d, approved)
	}
	ctx, cancel := context.WithTimeout(r.Context(), dnsRecordsBudget)
	defer cancel()
	// Writing a record is not verifying it. Nothing here calls the verification
	// path and no result reports a domain as verified: a provider accepting a
	// record means it reached that provider's edge, not that hakopod's resolver
	// can see it. Verifying here would also cache a negative answer and break the
	// user's next retry.
	results, err := createDNSRecords(ctx, client, provider, a.Project, a.Environment, desired, in.Hostnames, in.ReplaceExisting)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"results": results})
}
