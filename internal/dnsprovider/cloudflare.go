package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The endpoint is pinned, not user supplied, in this version. See http.go for
// what that removes and when it comes back.
const cloudflareEndpoint = "https://api.cloudflare.com"

const (
	maxZoneLookups = 8   // hostname suffixes tried when discovering a zone
	maxRecordPage  = 100 // records read for one name and type
	maxValueBytes  = 2048
	minTTL         = 60
	maxTTL         = 86400
	autoTTL        = 1 // the provider's "let the zone decide" TTL
)

type Zone struct{ ID, Name string }

type Record struct {
	Type    string
	Name    string
	Value   string
	TTL     int
	Proxied bool
}

// Create and Replace are deliberately separate: a single upsert could not
// refuse an unintended overwrite, which is the safety property that matters
// here. There is no Delete because nothing needs one.
type Client interface {
	Zone(ctx context.Context, hostname string) (Zone, error)
	Records(ctx context.Context, z Zone, name, kind string) ([]Record, error)
	Create(ctx context.Context, z Zone, r Record) error
	Replace(ctx context.Context, z Zone, existing, r Record) error
}

type cloudflare struct {
	transport
	provider Provider
}

// NewCloudflare builds a client over the bounded transport. The provider and
// credentials are validated here so a bad row never reaches the network.
func NewCloudflare(p Provider, c Credentials) (Client, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Kind != KindCloudflare || !p.Enabled {
		return nil, fmt.Errorf("%w: the provider is disabled", ErrInput)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &cloudflare{transport: transport{http: newBoundedClient(), endpoint: cloudflareEndpoint, token: c.Token}, provider: p}, nil
}

// envelope is the provider's response shape. Codes and messages are inspected
// but never returned: callers see a package sentinel only.
type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code int `json:"code"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (e envelope) conflict() bool {
	for _, item := range e.Errors {
		// 81053, 81057 and 81058 all mean a record is already there.
		if item.Code == 81053 || item.Code == 81057 || item.Code == 81058 {
			return true
		}
	}
	return false
}

func (c *cloudflare) call(ctx context.Context, method, path string, query url.Values, body, result any) error {
	var out envelope
	if err := c.request(ctx, method, path, query, body, &out); err != nil {
		return err
	}
	if !out.Success {
		if out.conflict() {
			return ErrExists
		}
		return ErrUnavailable
	}
	if result != nil && json.Unmarshal(out.Result, result) != nil {
		return ErrUnavailable
	}
	return nil
}

// zoneCandidates returns the hostname's suffixes of two or more labels, longest
// first, so the first zone found is the most specific one.
func zoneCandidates(hostname string) []string {
	labels := strings.Split(hostname, ".")
	var names []string
	for i := 0; i+2 <= len(labels) && len(names) < maxZoneLookups; i++ {
		names = append(names, strings.Join(labels[i:], "."))
	}
	return names
}

func (c *cloudflare) Zone(ctx context.Context, hostname string) (Zone, error) {
	// The zone filter is enforced here, before any request is made: a hostname
	// outside it is refused whatever the token itself can reach.
	if !c.provider.AllowsHostname(hostname) {
		return Zone{}, fmt.Errorf("%w: %s", ErrInput, "the hostname is outside this provider's permitted zones")
	}
	for _, name := range zoneCandidates(hostname) {
		var zones []Zone
		query := url.Values{"name": {name}, "status": {"active"}, "per_page": {"2"}}
		if err := c.call(ctx, http.MethodGet, "/client/v4/zones", query, nil, &zones); err != nil {
			return Zone{}, err
		}
		var found []Zone
		for _, zone := range zones {
			if zone.ID != "" && zone.Name == name {
				found = append(found, zone)
			}
		}
		// Two zones of the same name are equally specific, so neither is chosen.
		if len(found) > 1 {
			return Zone{}, fmt.Errorf("%w: %s", ErrInput, "more than one DNS zone matches this hostname equally well")
		}
		if len(found) == 1 {
			return found[0], nil
		}
	}
	return Zone{}, fmt.Errorf("%w: %s", ErrInput, "no DNS zone at this provider matches this hostname")
}

func validZone(z Zone) bool {
	return z.ID != "" && len(z.ID) <= 64 && !strings.ContainsAny(z.ID, "\x00\r\n/?#") && validDNSName(z.Name)
}

func validKind(kind string) bool { return kind == "TXT" || kind == "CNAME" }

func (r Record) validate() error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInput, message) }
	if !validKind(r.Type) {
		return invalid("only TXT and CNAME records are supported")
	}
	if !validDNSName(r.Name) {
		return invalid("the record name must be a lowercase DNS name")
	}
	if r.Value == "" || len(r.Value) > maxValueBytes || strings.ContainsAny(r.Value, "\x00\r\n") {
		return invalid("the record value exceeds bounds or contains control characters")
	}
	if r.TTL != autoTTL && (r.TTL < minTTL || r.TTL > maxTTL) {
		return invalid("the record TTL is outside the supported range")
	}
	// Cloudflare's proxy terminates TLS at their edge, which breaks certificate
	// issuance and the coverage hakopod assumes. The field exists so the wire
	// shape is right; it is never set true.
	if r.Proxied {
		return invalid("proxied records are not supported")
	}
	return nil
}

// wireRecord is the provider's record shape. The record ID stays inside this
// package: callers address a record by name, type and value.
type wireRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

func (c *cloudflare) list(ctx context.Context, z Zone, name, kind string) ([]wireRecord, error) {
	if !validZone(z) || !validKind(kind) || !validDNSName(name) {
		return nil, fmt.Errorf("%w: %s", ErrInput, "read records by zone, lowercase name and record type")
	}
	if !c.provider.AllowsHostname(name) || !c.provider.AllowsHostname(z.Name) {
		return nil, fmt.Errorf("%w: %s", ErrInput, "the hostname is outside this provider's permitted zones")
	}
	var records []wireRecord
	query := url.Values{"name": {name}, "type": {kind}, "per_page": {fmt.Sprint(maxRecordPage)}}
	if err := c.call(ctx, http.MethodGet, "/client/v4/zones/"+url.PathEscape(z.ID)+"/dns_records", query, nil, &records); err != nil {
		return nil, err
	}
	if len(records) > maxRecordPage {
		return nil, ErrUnavailable
	}
	return records, nil
}

func (c *cloudflare) Records(ctx context.Context, z Zone, name, kind string) ([]Record, error) {
	records, err := c.list(ctx, z, name, kind)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(records))
	for _, item := range records {
		result = append(result, Record{Type: item.Type, Name: item.Name, Value: item.Content, TTL: item.TTL, Proxied: item.Proxied})
	}
	return result, nil
}

func (c *cloudflare) Create(ctx context.Context, z Zone, r Record) error {
	if err := r.validate(); err != nil {
		return err
	}
	existing, err := c.list(ctx, z, r.Name, r.Type)
	if err != nil {
		return err
	}
	// A record already at this name and type is reported as existing, not as a
	// failure, and is never overwritten by Create.
	if len(existing) > 0 {
		return ErrExists
	}
	return c.call(ctx, http.MethodPost, "/client/v4/zones/"+url.PathEscape(z.ID)+"/dns_records", nil, wireRecord{Type: r.Type, Name: r.Name, Content: r.Value, TTL: r.TTL}, nil)
}

func (c *cloudflare) Replace(ctx context.Context, z Zone, existing, r Record) error {
	if err := existing.validate(); err != nil {
		return err
	}
	if err := r.validate(); err != nil {
		return err
	}
	if existing.Name != r.Name || existing.Type != r.Type {
		return fmt.Errorf("%w: %s", ErrInput, "a replacement keeps the same record name and type")
	}
	records, err := c.list(ctx, z, r.Name, r.Type)
	if err != nil {
		return err
	}
	// Only the exact record the caller read is replaced, so an unseen record is
	// never overwritten.
	for _, item := range records {
		if item.Content == existing.Value && item.ID != "" {
			return c.call(ctx, http.MethodPut, "/client/v4/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(item.ID), nil, wireRecord{Type: r.Type, Name: r.Name, Content: r.Value, TTL: r.TTL}, nil)
		}
	}
	return fmt.Errorf("%w: %s", ErrInput, "the record being replaced is no longer present; read it again")
}
