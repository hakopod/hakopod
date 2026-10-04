package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

// HostActionsGrant is trusted embedding configuration. The embedding must
// authorize a local host peer and the current internal workspace owner before
// calling this issuer. No HTTP request, browser identity, or key can supply it.
type HostActionsGrant struct {
	OwnerIdentity string
	Workspace     string
	Project       string
	Environment   string
	ApplicationID string
	Service       string
	// HostID identifies the retained installation recovery authority. Keep it
	// with the installation when restoring onto a different physical machine.
	HostID string
}

var hostActionsID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hostActionsHostID = regexp.MustCompile(`^[a-f0-9]{32}([a-f0-9]{32})?$`)
var hostActionsName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var hostActionsToken = regexp.MustCompile(`^hp_[a-f0-9]{32}_[a-f0-9]{64}$`)

func (g HostActionsGrant) valid() bool {
	return hostActionsID.MatchString(g.OwnerIdentity) && hostActionsID.MatchString(g.Workspace) &&
		hostActionsID.MatchString(g.ApplicationID) && hostActionsHostID.MatchString(g.HostID) &&
		hostActionsName.MatchString(g.Project) && hostActionsName.MatchString(g.Environment) && hostActionsName.MatchString(g.Service)
}

func (g HostActionsGrant) scope() string { return "host-actions:" + g.Workspace }
func (g HostActionsGrant) binding() string {
	return "host-actions:" + g.HostID + ":" + g.ApplicationID + ":" + g.Service
}

// EnsureHostActionsKey creates or renews a real, application-scoped machine key.
// It uses the canonical key generator, tables, permissions, audit and worker
// reauthorization. It never creates a browser session or installation admin key.
// A missing token for an existing binding is an explicit recovery conflict.
func (s *Store) EnsureHostActionsKey(ctx context.Context, grant HostActionsGrant, raw string) (Key, string, error) {
	return s.hostActionsKey(ctx, grant, raw, true)
}

// VerifyHostActionsKey is read-only apart from ordinary Authenticate last-use
// accounting. Unlike EnsureHostActionsKey it cannot renew an expired key.
func (s *Store) VerifyHostActionsKey(ctx context.Context, grant HostActionsGrant, raw string) (Principal, error) {
	if _, _, err := s.hostActionsKey(ctx, grant, raw, false); err != nil {
		return Principal{}, err
	}
	return s.Authenticate(ctx, raw)
}

func (s *Store) hostActionsKey(ctx context.Context, grant HostActionsGrant, raw string, renew bool) (Key, string, error) {
	if !grant.valid() || raw != "" && !hostActionsToken.MatchString(raw) || !renew && raw == "" {
		return Key{}, "", ErrUnauthorized
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Key{}, "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,793044317))", grant.scope()); err != nil {
		return Key{}, "", err
	}
	var owner bool
	err = tx.QueryRow(ctx, `SELECT owner AND admin AND email_verified AND email IS NOT NULL AND email<>'' AND NOT disabled AND project='' AND environment='' FROM identities WHERE id=$1 FOR SHARE`, grant.OwnerIdentity).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !owner {
		return Key{}, "", ErrForbidden
	}
	if err != nil {
		return Key{}, "", err
	}
	var application spec.Application
	var name, project, environment string
	err = tx.QueryRow(ctx, "SELECT name,project,environment,spec FROM applications WHERE id=$1 FOR SHARE", grant.ApplicationID).Scan(&name, &project, &environment, &application)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, "", ErrForbidden
	}
	if err != nil {
		return Key{}, "", err
	}
	service, exists := application.Services[grant.Service]
	if project != grant.Project || environment != grant.Environment || application.Name != name || len(application.Services) != 1 || !exists || service.Actions == nil {
		return Key{}, "", ErrForbidden
	}
	permissions := []string{"deployments:read", "deployments:write"}
	expires := time.Now().UTC().Add(90 * 24 * time.Hour)
	if raw == "" {
		var count int
		var existing bool
		err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(bool_or(sc.binding=$2),false) FROM automation_key_scopes sc JOIN api_keys k ON k.id=sc.key_id WHERE sc.scope_id=$1 AND k.revoked_at IS NULL`, grant.scope(), grant.binding()).Scan(&count, &existing)
		if err != nil {
			return Key{}, "", err
		}
		if existing {
			return Key{}, "", ErrConflict
		}
		if count >= 16 {
			return Key{}, "", ErrBusy
		}
		id, token, digest := makeKey()
		input := KeyInput{Name: "Host Actions recovery " + grant.ApplicationID, Project: project, Environment: environment, Application: name, Permissions: permissions, ExpiresAt: expires}
		if err = validKeyInput(input); err != nil {
			return Key{}, "", err
		}
		key := Key{ID: id, IdentityID: grant.OwnerIdentity, Name: input.Name, Prefix: "hp_" + id[:8], Project: project, Environment: environment, Application: name, Permissions: permissions, ExpiresAt: &expires, CreatedAt: time.Now().UTC()}
		_, err = tx.Exec(ctx, `INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,application,permissions,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, grant.OwnerIdentity, key.Name, digest, key.Prefix, project, environment, name, permissions, expires)
		if err != nil {
			return Key{}, "", err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO automation_key_scopes(key_id,scope_id,binding) VALUES($1,$2,$3)", id, grant.scope(), grant.binding()); err != nil {
			return Key{}, "", err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'host-actions-key.create',$3,$4)", grant.OwnerIdentity, id, grant.ApplicationID, JSON(map[string]string{"workspace": grant.Workspace, "service": grant.Service, "host_id": grant.HostID})); err != nil {
			return Key{}, "", err
		}
		return key, token, tx.Commit(ctx)
	}
	var key Key
	var digest []byte
	var scope, binding, kind string
	keyID := strings.Split(raw, "_")[1]
	err = tx.QueryRow(ctx, `SELECT k.id,k.identity_id,k.name,k.prefix,k.project,k.environment,k.application,k.permissions,k.expires_at,k.never_expires,k.created_at,k.digest,sc.scope_id,sc.binding,k.kind FROM api_keys k JOIN automation_key_scopes sc ON sc.key_id=k.id WHERE k.id=$1 AND k.revoked_at IS NULL FOR UPDATE OF k`, keyID).Scan(&key.ID, &key.IdentityID, &key.Name, &key.Prefix, &key.Project, &key.Environment, &key.Application, &key.Permissions, &key.ExpiresAt, &key.NeverExpires, &key.CreatedAt, &digest, &scope, &binding, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, "", ErrUnauthorized
	}
	if err != nil {
		return Key{}, "", err
	}
	sum := sha256.Sum256([]byte(raw))
	if subtle.ConstantTimeCompare(sum[:], digest) != 1 || kind != "machine" || key.NeverExpires || key.ExpiresAt == nil ||
		key.IdentityID != grant.OwnerIdentity || key.Project != project || key.Environment != environment || key.Application != name ||
		!slices.Equal(key.Permissions, permissions) || scope != grant.scope() || binding != grant.binding() {
		return Key{}, "", ErrUnauthorized
	}
	if !renew && !key.ExpiresAt.After(time.Now()) {
		return Key{}, "", ErrUnauthorized
	}
	if renew && key.ExpiresAt.Before(time.Now().Add(30*24*time.Hour)) {
		if _, err = tx.Exec(ctx, "UPDATE api_keys SET expires_at=$2 WHERE id=$1", key.ID, expires); err != nil {
			return Key{}, "", err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'host-actions-key.renew',$3,$4)", grant.OwnerIdentity, key.ID, grant.ApplicationID, JSON(map[string]string{"workspace": grant.Workspace, "service": grant.Service, "host_id": grant.HostID})); err != nil {
			return Key{}, "", err
		}
		key.ExpiresAt = &expires
	}
	return key, raw, tx.Commit(ctx)
}
