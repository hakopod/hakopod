package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/license"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var migrations embed.FS

var ErrConflict = errors.New("revision or idempotency conflict")
var ErrUnauthorized = errors.New("credential is invalid, expired, revoked, or disabled")
var ErrForbidden = errors.New("credential does not allow this operation in the requested scope")

type Store struct {
	Pool             *pgxpool.Pool
	ShowcaseEnabled  bool
	ProtectedDomains []string
	// Trusted embedding/test configuration; never supplied by a request.
	LicenseVerifier *license.Verifier
}

func Open(ctx context.Context, url string) (*Store, error) {
	c, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	c.MaxConns = 10
	c.MinConns = 0
	c.MaxConnIdleTime = 2 * time.Minute
	c.MaxConnLifetime = 30 * time.Minute
	c.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	p, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &Store{Pool: p, LicenseVerifier: license.ReleaseVerifier()}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044211)"); err != nil {
		return err
	}
	var exists bool
	err = tx.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, "CREATE TABLE schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
			return err
		}
	}
	entries, err := migrations.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("invalid migration filename %s", entry.Name())
		}
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrations.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1) ON CONFLICT DO NOTHING", version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func JSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type Principal struct {
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	Admin               bool             `json:"admin"`
	Owner               bool             `json:"owner"`
	Email               string           `json:"email,omitempty"`
	CredentialType      string           `json:"credential_type"`
	ProjectRoles        []ProjectRole    `json:"project_roles,omitempty"`
	AvatarStyle         string           `json:"avatar_style,omitempty"`
	AvatarSeed          string           `json:"avatar_seed,omitempty"`
	AvatarURL           string           `json:"avatar_url,omitempty"`
	ProfileRevision     int64            `json:"profile_revision"`
	HostPermissions     []HostPermission `json:"host_permissions"`
	KeyID               string           `json:"-"`
	Project             string           `json:"project"`
	Environment         string           `json:"environment"`
	Application         string           `json:"application,omitempty"`
	Permissions         []string         `json:"permissions"`
	IdentityProject     string           `json:"-"`
	IdentityEnvironment string           `json:"-"`
	IdentityPermissions []string         `json:"-"`
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func (p Principal) Allows(permission, project, environment, application string) bool {
	if p.Project != "" && p.Project != project {
		return false
	}
	if p.Environment != "" && p.Environment != environment {
		return false
	}
	if p.Application != "" && p.Application != application {
		return false
	}
	if p.IdentityProject != "" && p.IdentityProject != project {
		return false
	}
	if p.IdentityEnvironment != "" && p.IdentityEnvironment != environment {
		return false
	}
	keyOK := contains(p.Permissions, permission) || contains(p.Permissions, "admin")
	identityOK := p.Admin || contains(p.IdentityPermissions, permission)
	if p.Email != "" && !p.Admin {
		identityOK = false
		for _, role := range p.ProjectRoles {
			if role.Project == project && contains(rolePermissions(role.Role), permission) {
				identityOK = true
				break
			}
		}
	}
	return keyOK && identityOK
}
func (p Principal) IsAdmin() bool {
	return p.Admin && contains(p.Permissions, "admin") && p.Project == "" && p.Environment == "" && p.Application == "" && p.IdentityProject == "" && p.IdentityEnvironment == ""
}

type Key struct {
	ID          string     `json:"id"`
	IdentityID  string     `json:"identity_id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Application string     `json:"application,omitempty"`
	Permissions []string   `json:"permissions"`
	ExpiresAt   time.Time  `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}
type KeyInput struct {
	Name        string    `json:"name"`
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Application string    `json:"application"`
	Permissions []string  `json:"permissions"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func validKeyInput(in KeyInput) error {
	if len(in.Name) < 1 || len(in.Name) > 100 {
		return errors.New("name must contain 1–100 characters")
	}
	if in.ExpiresAt.Before(time.Now().Add(time.Minute)) || in.ExpiresAt.After(time.Now().Add(90*24*time.Hour)) {
		return errors.New("expires_at must be between one minute and 90 days from now")
	}
	if len(in.Permissions) == 0 {
		return errors.New("at least one permission is required")
	}
	for _, v := range in.Permissions {
		if !contains([]string{"admin", "deployments:read", "deployments:write", "logs:read"}, v) {
			return fmt.Errorf("unsupported permission %q", v)
		}
	}
	if !contains(in.Permissions, "admin") && (in.Project == "" || in.Environment == "") {
		return errors.New("machine keys require explicit project and environment")
	}
	return nil
}
func makeKey() (id, raw string, digest []byte) {
	id = NewID()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	raw = "hp_" + id + "_" + hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return id, raw, sum[:]
}
func (s *Store) Bootstrap(ctx context.Context, name string) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044212)"); err != nil {
		return "", err
	}
	var n int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM identities").Scan(&n); err != nil {
		return "", err
	}
	if n != 0 {
		return "", errors.New("administrator already exists; bootstrap is disabled")
	}
	ident := NewID()
	id, raw, digest := makeKey()
	if _, err = tx.Exec(ctx, "INSERT INTO identities(id,name,admin) VALUES($1,$2,true)", ident, name); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,permissions,expires_at) VALUES($1,$2,'bootstrap',$3,$4,ARRAY['admin'],now()+interval '7 days')", id, ident, digest, "hp_"+id[:8]); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO projects(name) VALUES('demo'); INSERT INTO environments(project,name) VALUES('demo','development')"); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return raw, nil
}
func (s *Store) principal(ctx context.Context, keyID string) (Principal, []byte, error) {
	var p Principal
	var digest []byte
	err := s.Pool.QueryRow(ctx, `SELECT i.id,i.name,i.admin,k.id,k.project,k.environment,k.application,k.permissions,i.project,i.environment,i.permissions,k.digest,COALESCE(i.email,''),i.owner,k.kind,i.avatar_style,i.avatar_seed,i.profile_revision FROM api_keys k JOIN identities i ON i.id=k.identity_id WHERE k.id=$1 AND k.revoked_at IS NULL AND k.expires_at>now() AND NOT i.disabled`, keyID).Scan(&p.ID, &p.Name, &p.Admin, &p.KeyID, &p.Project, &p.Environment, &p.Application, &p.Permissions, &p.IdentityProject, &p.IdentityEnvironment, &p.IdentityPermissions, &digest, &p.Email, &p.Owner, &p.CredentialType, &p.AvatarStyle, &p.AvatarSeed, &p.ProfileRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil, ErrUnauthorized
	}
	if err == nil && p.Email != "" {
		p.ProjectRoles, err = s.projectRoles(ctx, p.ID)
	}
	if err == nil && p.IsHuman() {
		p.AvatarURL = AvatarURL(p.AvatarStyle, p.AvatarSeed, p.ID)
		p.HostPermissions, err = s.hostPermissions(ctx, p.ID)
	}
	return p, digest, err
}
func (s *Store) Authenticate(ctx context.Context, raw string) (Principal, error) {
	parts := strings.Split(raw, "_")
	if len(parts) != 3 || (parts[0] != "hp" && parts[0] != "hs") || len(parts[1]) != 32 || len(parts[2]) != 64 {
		return Principal{}, ErrUnauthorized
	}
	p, digest, err := s.principal(ctx, parts[1])
	if err != nil {
		return p, err
	}
	if (parts[0] == "hp") != (p.CredentialType == "machine") {
		return Principal{}, ErrUnauthorized
	}
	if p.CredentialType == "integration" {
		return Principal{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(raw))
	if subtle.ConstantTimeCompare(sum[:], digest) != 1 {
		return Principal{}, ErrUnauthorized
	}
	_, err = s.Pool.Exec(ctx, "UPDATE api_keys SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at<now()-interval '1 minute')", p.KeyID)
	return p, err
}
func (s *Store) Reauthorize(ctx context.Context, keyID, project, environment, app string) error {
	p, _, err := s.principal(ctx, keyID)
	if err != nil {
		return err
	}
	if !p.Allows("deployments:write", project, environment, app) {
		return ErrForbidden
	}
	return nil
}
func (s *Store) KeyPrincipal(ctx context.Context, keyID string) (Principal, error) {
	p, _, err := s.principal(ctx, keyID)
	return p, err
}
func (s *Store) CreateKey(ctx context.Context, p Principal, in KeyInput) (Key, string, error) {
	if !p.IsAdmin() {
		return Key{}, "", ErrForbidden
	}
	if err := validKeyInput(in); err != nil {
		return Key{}, "", err
	}
	if in.Project != "" {
		var exists bool
		if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM environments WHERE project=$1 AND name=$2)", in.Project, in.Environment).Scan(&exists); err != nil {
			return Key{}, "", err
		}
		if !exists {
			return Key{}, "", errors.New("project/environment does not exist")
		}
	}
	id, raw, digest := makeKey()
	k := Key{ID: id, IdentityID: p.ID, Name: in.Name, Prefix: "hp_" + id[:8], Project: in.Project, Environment: in.Environment, Application: in.Application, Permissions: in.Permissions, ExpiresAt: in.ExpiresAt, CreatedAt: time.Now().UTC()}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return k, "", err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO api_keys(id,identity_id,name,digest,prefix,project,environment,application,permissions,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", id, p.ID, in.Name, digest, k.Prefix, in.Project, in.Environment, in.Application, in.Permissions, in.ExpiresAt)
	if err != nil {
		return k, "", err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'key.create',$3)", p.ID, p.KeyID, id)
	if err != nil {
		return k, "", err
	}
	err = tx.Commit(ctx)
	return k, raw, err
}
func (s *Store) Keys(ctx context.Context) ([]Key, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,identity_id,name,prefix,project,environment,application,permissions,expires_at,revoked_at,last_used_at,created_at FROM api_keys WHERE kind='machine' ORDER BY created_at DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		var k Key
		if err = rows.Scan(&k.ID, &k.IdentityID, &k.Name, &k.Prefix, &k.Project, &k.Environment, &k.Application, &k.Permissions, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

type Application struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Project     string              `json:"project"`
	Environment string              `json:"environment"`
	Revision    int64               `json:"revision"`
	Status      string              `json:"status"`
	Spec        spec.Application    `json:"spec"`
	Observed    json.RawMessage     `json:"observed"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Deployments []DeploymentSummary `json:"deployments,omitempty"`
}

// DeploymentSummary keeps immutable configuration bodies off history lists.
// Full specifications remain available on the explicit deployment detail API.
type DeploymentSummary struct {
	ID              string          `json:"id"`
	ApplicationID   string          `json:"application_id"`
	IdentityID      string          `json:"identity_id"`
	Revision        int64           `json:"revision"`
	Status          string          `json:"status"`
	Result          json.RawMessage `json:"result"`
	Error           string          `json:"error"`
	CancelRequested bool            `json:"cancel_requested"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at"`
}
type Deployment struct {
	ID              string            `json:"id"`
	ApplicationID   string            `json:"application_id"`
	IdentityID      string            `json:"identity_id"`
	KeyID           string            `json:"-"`
	Revision        int64             `json:"revision"`
	Status          string            `json:"status"`
	Spec            spec.Application  `json:"spec"`
	ResolvedSpec    *spec.Application `json:"resolved_spec"`
	Result          json.RawMessage   `json:"result"`
	Error           string            `json:"error"`
	CancelRequested bool              `json:"cancel_requested"`
	CreatedAt       time.Time         `json:"created_at"`
	StartedAt       *time.Time        `json:"started_at"`
	FinishedAt      *time.Time        `json:"finished_at"`
	Events          []Event           `json:"events"`
}
type Event struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	Service string    `json:"service"`
}

const appCols = "id,name,project,environment,revision,status,spec,observed,created_at,updated_at"
const depCols = "id,application_id,identity_id,key_id,revision,status,spec,resolved_spec,result,error,cancel_requested,created_at,started_at,finished_at"

type scanner interface{ Scan(...any) error }

func scanApp(r scanner) (Application, error) {
	var a Application
	err := r.Scan(&a.ID, &a.Name, &a.Project, &a.Environment, &a.Revision, &a.Status, &a.Spec, &a.Observed, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}
func scanDep(r scanner) (Deployment, error) {
	var d Deployment
	err := r.Scan(&d.ID, &d.ApplicationID, &d.IdentityID, &d.KeyID, &d.Revision, &d.Status, &d.Spec, &d.ResolvedSpec, &d.Result, &d.Error, &d.CancelRequested, &d.CreatedAt, &d.StartedAt, &d.FinishedAt)
	d.Events = []Event{}
	return d, err
}
func (s *Store) Application(ctx context.Context, id string) (Application, error) {
	return scanApp(s.Pool.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1", id))
}
func (s *Store) FindApplication(ctx context.Context, project, env, name string) (Application, error) {
	return scanApp(s.Pool.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE project=$1 AND environment=$2 AND name=$3", project, env, name))
}

// ApplicationPage bounds both row count and configuration bytes retained for a
// response. The one-row lookahead determines whether a continuation is needed;
// keyset pagination does not grow slower or allocate larger offsets over time.
func (s *Store) ApplicationPage(ctx context.Context, project, env, cursor string) ([]Application, string, error) {
	const maxItems = 25
	const byteBudget = 512 << 10
	rows, err := s.Pool.Query(ctx, "SELECT "+appCols+" FROM applications WHERE project=$1 AND environment=$2 AND id>$3 ORDER BY id LIMIT 26", project, env, cursor)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]Application, 0, maxItems)
	var used int64
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, "", err
		}
		// Measure the actual Go JSON representation: PostgreSQL jsonb text can
		// be much smaller when the HTTP encoder escapes HTML characters.
		size := int64(len(JSON(app.Spec)) + len(JSON(app.Observed)))
		if len(items) == maxItems || (len(items) > 0 && used+size > byteBudget) {
			return items, items[len(items)-1].ID, nil
		}
		items = append(items, app)
		used += size
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return items, "", nil
}
func (s *Store) Deployment(ctx context.Context, id string) (Deployment, error) {
	return scanDep(s.Pool.QueryRow(ctx, "SELECT "+depCols+" FROM deployments WHERE id=$1", id))
}
func (s *Store) History(ctx context.Context, app string) ([]DeploymentSummary, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,application_id,identity_id,revision,status,result,error,cancel_requested,created_at,started_at,finished_at FROM deployments WHERE application_id=$1 ORDER BY revision DESC LIMIT 30", app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeploymentSummary{}
	for rows.Next() {
		var d DeploymentSummary
		if err := rows.Scan(&d.ID, &d.ApplicationID, &d.IdentityID, &d.Revision, &d.Status, &d.Result, &d.Error, &d.CancelRequested, &d.CreatedAt, &d.StartedAt, &d.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) Events(ctx context.Context, id string) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,time,type,message,service FROM (SELECT id,time,type,message,service FROM deployment_events WHERE deployment_id=$1 ORDER BY id DESC LIMIT 200) e ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.ID, &e.Time, &e.Type, &e.Message, &e.Service); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) Event(ctx context.Context, id, typ, message, service string) error {
	if len(message) > 2048 {
		message = message[:2048]
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO deployment_events(deployment_id,type,message,service) VALUES($1,$2,$3,$4)", id, typ, message, service)
	return err
}
