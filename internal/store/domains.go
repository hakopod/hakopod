package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

type DomainVerification struct {
	Hostname   string     `json:"hostname"`
	Service    string     `json:"service"`
	Token      string     `json:"-"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

func (s *Store) DomainAllowed(host string) bool {
	if !spec.ValidHostname(host) {
		return false
	}
	for _, suffix := range s.ProtectedDomains {
		if suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix)) {
			return false
		}
	}
	return true
}
func (s *Store) DomainVerifications(ctx context.Context, app string) ([]DomainVerification, error) {
	rows, err := s.Pool.Query(ctx, "SELECT hostname,service,token,verified_at FROM domain_verifications WHERE application_id=$1 ORDER BY hostname LIMIT 40", app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DomainVerification{}
	for rows.Next() {
		var d DomainVerification
		if err = rows.Scan(&d.Hostname, &d.Service, &d.Token, &d.VerifiedAt); err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, rows.Err()
}
func (s *Store) BeginDomainVerification(ctx context.Context, p Principal, a Application, host, service string) (DomainVerification, error) {
	d := DomainVerification{Hostname: host, Service: service}
	if !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return d, ErrForbidden
	}
	if !s.DomainAllowed(host) {
		return d, fmt.Errorf("%w: use a custom hostname outside the installation's reserved domains", ErrInput)
	}
	svc, ok := a.Spec.Services[service]
	if !ok || !svc.Public || svc.Port == 0 {
		return d, fmt.Errorf("%w: select a public HTTP service", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM applications WHERE id=$1 FOR UPDATE", a.ID); err != nil {
		return d, err
	}
	var existing string
	err = tx.QueryRow(ctx, "SELECT application_id FROM application_domains WHERE hostname=$1", host).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return d, err
	}
	if existing != "" && existing != a.ID {
		return d, fmt.Errorf("%w: hostname is already reserved by another application", ErrConflict)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM domain_verifications WHERE application_id=$1 AND hostname<>$2", a.ID, host).Scan(&count); err != nil {
		return d, err
	}
	if count >= 40 {
		return d, fmt.Errorf("%w: at most 40 domain reservations and pending verifications are supported", ErrInput)
	}
	d.Token = "hakopod-verification=" + NewID() + NewID()
	err = tx.QueryRow(ctx, "INSERT INTO domain_verifications(application_id,hostname,service,token) VALUES($1,$2,$3,$4) ON CONFLICT(application_id,hostname) DO UPDATE SET service=EXCLUDED.service RETURNING token,verified_at", a.ID, host, service, d.Token).Scan(&d.Token, &d.VerifiedAt)
	if err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}
func (s *Store) ConfirmDomainVerification(ctx context.Context, p Principal, a Application, d DomainVerification) error {
	if !p.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE domain_verifications SET verified_at=now() WHERE application_id=$1 AND hostname=$2 AND token=$3", a.ID, d.Hostname, d.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'domain.verify',$3,$4)", p.ID, p.KeyID, a.ID, JSON(map[string]string{"hostname": d.Hostname}))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) reserveDomains(ctx context.Context, tx pgx.Tx, app string, next spec.Application) error {
	if err := spec.ValidateDomains(next); err != nil {
		return fmt.Errorf("%w: %s", ErrInput, err)
	}
	hosts := make([]string, 0, len(next.Domains))
	for host := range next.Domains {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		if !s.DomainAllowed(host) {
			return fmt.Errorf("%w: hostname is reserved for installation routing", ErrInput)
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,12))", host); err != nil {
			return err
		}
		var owner string
		err := tx.QueryRow(ctx, "SELECT application_id FROM application_domains WHERE hostname=$1", host).Scan(&owner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if owner == app {
			continue
		}
		if owner != "" {
			return fmt.Errorf("%w: hostname is reserved by another application", ErrConflict)
		}
		var verified bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM domain_verifications WHERE application_id=$1 AND hostname=$2 AND verified_at>now()-interval '1 hour')", app, host).Scan(&verified); err != nil {
			return err
		}
		if !verified {
			return fmt.Errorf("%w: verify DNS ownership of %s before applying it", ErrInput, host)
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM application_domains WHERE application_id=$1", app).Scan(&count); err != nil {
			return err
		}
		if count >= 40 {
			return fmt.Errorf("%w: this application already reserves 40 domains", ErrInput)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO application_domains(hostname,application_id) VALUES($1,$2)", host, app); err != nil {
			return err
		}
	}
	// A removed route keeps its reservation for historical rollbacks. Releasing
	// it before all old releases are retired could route another app's hostname.
	return nil
}
