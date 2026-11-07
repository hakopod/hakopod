package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// Custom hostname states. pending_dns waits for the customer's CNAME, issuing
// routes the host and waits for its certificate, active serves it, failed
// stopped trying, and deleting waits for the cluster to drop the route.
const (
	HostnamePendingDNS = "pending_dns"
	HostnameIssuing    = "issuing"
	HostnameActive     = "active"
	HostnameFailed     = "failed"
	HostnameDeleting   = "deleting"
)

// HostnameDNSWindow is how long a hostname may wait for its DNS before it fails.
const HostnameDNSWindow = 7 * 24 * time.Hour

// HostnameIssueWindow bounds certificate issuance after DNS verified. cert-manager
// retries a failed ACME order after about an hour, so this allows a few tries.
const HostnameIssueWindow = 3 * time.Hour

// DefaultCustomHostnamesPerService is the installation cap when the setting is missing.
const DefaultCustomHostnamesPerService = 10000

type CustomHostname struct {
	ID            string     `json:"id"`
	ApplicationID string     `json:"application_id"`
	Service       string     `json:"service"`
	Hostname      string     `json:"hostname"`
	Status        string     `json:"status"`
	StatusReason  string     `json:"status_reason"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	VerifiedAt    *time.Time `json:"verified_at"`
	ActiveAt      *time.Time `json:"active_at"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	NextCheckAt   time.Time  `json:"next_check_at"`
	Attempts      int        `json:"attempts"`
}

const hostnameCols = "id,application_id,service,hostname,status,status_reason,created_at,updated_at,verified_at,active_at,last_checked_at,next_check_at,attempts"

func scanHostname(r scanner) (CustomHostname, error) {
	var h CustomHostname
	err := r.Scan(&h.ID, &h.ApplicationID, &h.Service, &h.Hostname, &h.Status, &h.StatusReason, &h.CreatedAt, &h.UpdatedAt, &h.VerifiedAt, &h.ActiveAt, &h.LastCheckedAt, &h.NextCheckAt, &h.Attempts)
	return h, err
}

func (s *Store) queryHostnames(ctx context.Context, sql string, args ...any) ([]CustomHostname, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomHostname{}
	for rows.Next() {
		h, err := scanHostname(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ValidCustomHostname accepts a lowercase DNS name outside the installation's
// own domains. Wildcards, IP addresses, single labels and over-long names fail.
func ValidCustomHostname(host string, protected []string) error {
	if len(host) < 4 || len(host) > 253 || !spec.ValidHostname(host) {
		return fmt.Errorf("%w: use a lowercase DNS hostname such as shop.example.com, without a scheme, port, path, wildcard or IP address", ErrInput)
	}
	for _, suffix := range protected {
		if suffix = strings.Trim(strings.ToLower(suffix), "."); suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix)) {
			return fmt.Errorf("%w: hostname is reserved for installation routing", ErrInput)
		}
	}
	return nil
}

// HostnameBackoff is the wait after the n-th failed DNS check: 1m, 5m, 30m, then 6h.
func HostnameBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Minute
	case attempts == 2:
		return 5 * time.Minute
	case attempts == 3:
		return 30 * time.Minute
	}
	return 6 * time.Hour
}

// HostnameAfterDNS is the next state of a pending hostname after one DNS check.
func HostnameAfterDNS(h CustomHostname, matched bool, reason string, now time.Time) CustomHostname {
	h.LastCheckedAt = &now
	if matched {
		h.Status, h.StatusReason, h.VerifiedAt, h.NextCheckAt = HostnameIssuing, "", &now, now
		return h
	}
	h.Attempts++
	h.StatusReason = clip(reason)
	if now.Sub(h.CreatedAt) >= HostnameDNSWindow {
		h.Status = HostnameFailed
		h.StatusReason = clip("DNS did not point at the target within 7 days: " + reason)
		return h
	}
	h.NextCheckAt = now.Add(HostnameBackoff(h.Attempts))
	return h
}

// HostnameAfterIssue is the next state of an issuing hostname given its
// certificate. Without TLS (no issuer configured) a routed host is active.
func HostnameAfterIssue(h CustomHostname, tls, ready bool, reason string, now time.Time) CustomHostname {
	h.LastCheckedAt = &now
	if ready || !tls {
		h.Status, h.ActiveAt, h.StatusReason = HostnameActive, &now, ""
		if !tls {
			h.StatusReason = "Served over HTTP: the installation has no TLS issuer configured."
		}
		return h
	}
	started := h.CreatedAt
	if h.VerifiedAt != nil {
		started = *h.VerifiedAt
	}
	if now.Sub(started) >= HostnameIssueWindow {
		if reason == "" {
			reason = "no certificate was issued"
		}
		h.Status, h.StatusReason = HostnameFailed, clip("Certificate was not issued within 3 hours: "+reason)
		return h
	}
	h.StatusReason = clip(reason)
	h.NextCheckAt = now.Add(time.Minute)
	return h
}

func clip(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

type CustomHostnameSettings struct {
	MaxPerService int `json:"max_per_service"`
}

func customHostnameSettings(ctx context.Context, q settingsQuerier) (CustomHostnameSettings, int64, error) {
	v := CustomHostnameSettings{MaxPerService: DefaultCustomHostnamesPerService}
	var raw []byte
	var rev int64
	err := q.QueryRow(ctx, "SELECT value,revision FROM installation_settings WHERE name='custom_hostnames'").Scan(&raw, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, 0, nil
	}
	if err != nil {
		return v, 0, err
	}
	return v, rev, json.Unmarshal(raw, &v)
}

func (s *Store) CustomHostnameSettings(ctx context.Context) (CustomHostnameSettings, int64, error) {
	return customHostnameSettings(ctx, s.Pool)
}

func (s *Store) PutCustomHostnameSettings(ctx context.Context, v CustomHostnameSettings, expectedRevision int64) (int64, error) {
	if v.MaxPerService < 1 || v.MaxPerService > spec.CustomHostnamesCeiling {
		return 0, fmt.Errorf("%w: allow between 1 and 50000 custom hostnames per service", ErrInput)
	}
	var rev int64
	var err error
	if expectedRevision == 0 {
		err = s.Pool.QueryRow(ctx, "INSERT INTO installation_settings(name,value) VALUES('custom_hostnames',$1) ON CONFLICT(name) DO NOTHING RETURNING revision", JSON(v)).Scan(&rev)
	} else {
		err = s.Pool.QueryRow(ctx, "UPDATE installation_settings SET value=$1,revision=revision+1,updated_at=now() WHERE name='custom_hostnames' AND revision=$2 RETURNING revision", JSON(v), expectedRevision).Scan(&rev)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: custom hostname settings changed; reload them and try again", ErrConflict)
	}
	return rev, err
}

// CustomHostnameLimit is the effective cap of one service: its own max, never
// above the installation's max_per_service.
func CustomHostnameLimit(svc spec.Service, installation int) int {
	return min(svc.CustomHostnames.Limit(), installation)
}

// CreateCustomHostname attaches a tenant hostname to a service. Repeating it for
// the same service returns the existing record; a failed one starts again.
func (s *Store) CreateCustomHostname(ctx context.Context, p Principal, a Application, service, host string) (CustomHostname, bool, error) {
	var h CustomHostname
	if !p.Allows("hostnames:manage", a.Project, a.Environment, a.Name) {
		return h, false, ErrForbidden
	}
	svc, ok := a.Spec.Services[service]
	if !ok || !spec.CustomHostnamesEnabled(svc) {
		return h, false, fmt.Errorf("%w: enable [services.%s.custom_hostnames] and deploy before adding hostnames", ErrInput, service)
	}
	if err := ValidCustomHostname(host, s.ProtectedDomains); err != nil {
		return h, false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return h, false, err
	}
	defer tx.Rollback(ctx)
	// The same lock key as application domain reservation serializes both tables.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,12))", host); err != nil {
		return h, false, err
	}
	h, err = scanHostname(tx.QueryRow(ctx, "SELECT "+hostnameCols+" FROM custom_hostnames WHERE hostname=$1", host))
	switch {
	case err == nil && (h.ApplicationID != a.ID || h.Service != service):
		return CustomHostname{}, false, fmt.Errorf("%w: hostname is already attached to another service", ErrConflict)
	case err == nil && h.Status == HostnameDeleting:
		return CustomHostname{}, false, fmt.Errorf("%w: hostname is being removed; retry in a minute", ErrConflict)
	case err == nil && h.Status == HostnameFailed:
		h, err = scanHostname(tx.QueryRow(ctx, "UPDATE custom_hostnames SET status='pending_dns',status_reason='',attempts=0,created_at=now(),updated_at=now(),verified_at=NULL,next_check_at=now() WHERE id=$1 RETURNING "+hostnameCols, h.ID))
		if err != nil {
			return h, false, err
		}
		if err = auditHostname(ctx, tx, p, a, h, "custom_hostname.retry"); err != nil {
			return h, false, err
		}
		return h, false, tx.Commit(ctx)
	case err == nil:
		return h, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return h, false, err
	}
	var taken bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM application_domains WHERE hostname=$1)", host).Scan(&taken); err != nil {
		return h, false, err
	}
	if taken {
		return h, false, fmt.Errorf("%w: hostname is reserved by an application domain", ErrConflict)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,13))", a.ID+"/"+service); err != nil {
		return h, false, err
	}
	settings, _, err := customHostnameSettings(ctx, tx)
	if err != nil {
		return h, false, err
	}
	limit := CustomHostnameLimit(svc, settings.MaxPerService)
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM custom_hostnames WHERE application_id=$1 AND service=$2", a.ID, service).Scan(&count); err != nil {
		return h, false, err
	}
	if count >= limit {
		return h, false, fmt.Errorf("%w: this service already has %d custom hostnames, its limit", ErrConflict, limit)
	}
	h, err = scanHostname(tx.QueryRow(ctx, "INSERT INTO custom_hostnames(id,application_id,service,hostname) VALUES($1,$2,$3,$4) RETURNING "+hostnameCols, NewID(), a.ID, service, host))
	if err != nil {
		return h, false, err
	}
	if err = auditHostname(ctx, tx, p, a, h, "custom_hostname.create"); err != nil {
		return h, false, err
	}
	return h, true, tx.Commit(ctx)
}

func auditHostname(ctx context.Context, tx pgx.Tx, p Principal, a Application, h CustomHostname, action string) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,$4,$5)", p.ID, p.KeyID, action, a.ID, JSON(map[string]string{"hostname": h.Hostname, "service": h.Service}))
	return err
}

// DeleteCustomHostname marks a hostname for removal. The worker drops its route,
// certificate and secret, then the row.
func (s *Store) DeleteCustomHostname(ctx context.Context, p Principal, a Application, service, host string) (CustomHostname, error) {
	if !p.Allows("hostnames:manage", a.Project, a.Environment, a.Name) {
		return CustomHostname{}, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return CustomHostname{}, err
	}
	defer tx.Rollback(ctx)
	h, err := scanHostname(tx.QueryRow(ctx, "UPDATE custom_hostnames SET status='deleting',status_reason='',updated_at=now(),next_check_at=now() WHERE application_id=$1 AND service=$2 AND hostname=$3 RETURNING "+hostnameCols, a.ID, service, host))
	if err != nil {
		return h, err
	}
	if err = auditHostname(ctx, tx, p, a, h, "custom_hostname.delete"); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) CustomHostname(ctx context.Context, app, service, host string) (CustomHostname, error) {
	return scanHostname(s.Pool.QueryRow(ctx, "SELECT "+hostnameCols+" FROM custom_hostnames WHERE application_id=$1 AND service=$2 AND hostname=$3", app, service, host))
}

// CustomHostnames lists one page ordered by hostname. The cursor is the last
// hostname of the previous page; next is empty on the last page.
func (s *Store) CustomHostnames(ctx context.Context, app, service, status, cursor string, limit int) ([]CustomHostname, string, error) {
	limit = clampLimit(limit, 500)
	items, err := s.queryHostnames(ctx, "SELECT "+hostnameCols+" FROM custom_hostnames WHERE application_id=$1 AND service=$2 AND hostname>$3 AND ($4='' OR status=$4) ORDER BY hostname LIMIT $5", app, service, cursor, status, limit+1)
	if err != nil || len(items) <= limit {
		return items, "", err
	}
	return items[:limit], items[limit-1].Hostname, nil
}

// ClaimCustomHostnames leases due hostnames for two minutes so that several
// workers never check the same one at once.
func (s *Store) ClaimCustomHostnames(ctx context.Context, limit int) ([]CustomHostname, error) {
	return s.queryHostnames(ctx, "UPDATE custom_hostnames SET next_check_at=now()+interval '2 minutes' WHERE id IN (SELECT id FROM custom_hostnames WHERE status IN ('pending_dns','issuing','deleting') AND next_check_at<=now() ORDER BY next_check_at LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING "+hostnameCols, clampLimit(limit, 500))
}

// SaveCustomHostnameState writes a worker decision unless the row changed
// state meanwhile, for example because it was deleted.
func (s *Store) SaveCustomHostnameState(ctx context.Context, before string, h CustomHostname) error {
	_, err := s.Pool.Exec(ctx, "UPDATE custom_hostnames SET status=$3,status_reason=$4,verified_at=$5,active_at=$6,last_checked_at=$7,next_check_at=$8,attempts=$9,updated_at=now() WHERE id=$1 AND status=$2",
		h.ID, before, h.Status, h.StatusReason, h.VerifiedAt, h.ActiveAt, h.LastCheckedAt, h.NextCheckAt, h.Attempts)
	return err
}

// RoutedCustomHostnames are the hosts a service's ingress shards should carry,
// oldest first so shard placement stays stable. asOf is read first: a row
// marked deleting before it is certainly absent from the list.
func (s *Store) RoutedCustomHostnames(ctx context.Context, app, service string) ([]string, time.Time, error) {
	var asOf time.Time
	if err := s.Pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&asOf); err != nil {
		return nil, asOf, err
	}
	rows, err := s.Pool.Query(ctx, "SELECT hostname FROM custom_hostnames WHERE application_id=$1 AND service=$2 AND status IN ('issuing','active') ORDER BY created_at,id LIMIT $3", app, service, spec.CustomHostnamesCeiling)
	if err != nil {
		return nil, asOf, err
	}
	defer rows.Close()
	hosts := []string{}
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, asOf, err
		}
		hosts = append(hosts, host)
	}
	return hosts, asOf, rows.Err()
}

// RoutedHostnames adapts RoutedCustomHostnames to the cluster's deploy callback.
func (s *Store) RoutedHostnames(ctx context.Context, app, service string) ([]string, error) {
	hosts, _, err := s.RoutedCustomHostnames(ctx, app, service)
	return hosts, err
}

// PurgeDeletedCustomHostnames removes rows whose route was dropped by a
// reconcile that started at asOf.
func (s *Store) PurgeDeletedCustomHostnames(ctx context.Context, app, service string, asOf time.Time) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM custom_hostnames WHERE application_id=$1 AND service=$2 AND status='deleting' AND updated_at<$3", app, service, asOf)
	return err
}

// acceptCustomHostnames runs when a revision is accepted. A service may not ask
// for more than the installation allows; a service that stops routing tenant
// hostnames, or disappears, has all of them removed. Application domains may
// not take a hostname a tenant already holds.
func acceptCustomHostnames(ctx context.Context, tx pgx.Tx, app string, next spec.Application) error {
	settings, _, err := customHostnameSettings(ctx, tx)
	if err != nil {
		return err
	}
	enabled := []string{}
	for name, svc := range next.Services {
		if !spec.CustomHostnamesEnabled(svc) {
			continue
		}
		enabled = append(enabled, name)
		if svc.CustomHostnames.Limit() > settings.MaxPerService {
			return fmt.Errorf("%w: services.%s.custom_hostnames.max exceeds this installation's limit of %d", ErrInput, name, settings.MaxPerService)
		}
	}
	for host := range next.Domains {
		var taken bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM custom_hostnames WHERE hostname=$1)", host).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: %s is attached as a custom hostname", ErrConflict, host)
		}
	}
	_, err = tx.Exec(ctx, "UPDATE custom_hostnames SET status='deleting',status_reason='Custom hostnames were turned off for this service.',updated_at=now(),next_check_at=now() WHERE application_id=$1 AND status<>'deleting' AND NOT (service=ANY($2::text[]))", app, enabled)
	return err
}
