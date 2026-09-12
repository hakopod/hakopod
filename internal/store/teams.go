package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}
type TeamMember struct {
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

// Deletion remains available to the recovery administrator without Pro. Foreign
// keys remove memberships, project-team grants and pending team invitations.
func (s *Store) DeleteTeam(ctx context.Context, p Principal, id string) error {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "DELETE FROM teams WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'team.delete',$3)", p.ID, p.KeyID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Teams(ctx context.Context, p Principal) ([]Team, error) {
	if p.CredentialType != "browser" {
		return nil, ErrForbidden
	}
	if !p.IsAdmin() {
		if err := s.RequireFeatures(ctx, "teams"); err != nil {
			return nil, err
		}
	}
	rows, err := s.Pool.Query(ctx, "SELECT t.id,t.name,COALESCE(m.role,'admin') FROM teams t LEFT JOIN team_members m ON m.team_id=t.id AND m.identity_id=$1 WHERE $2 OR m.identity_id IS NOT NULL ORDER BY t.name,t.id LIMIT 100", p.ID, p.IsAdmin())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err = rows.Scan(&t.ID, &t.Name, &t.Role); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) CreateTeam(ctx context.Context, p Principal, name string) (Team, error) {
	if p.CredentialType != "browser" || !p.IsAdmin() {
		return Team{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 100 {
		return Team{}, ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Team{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, "teams"); err != nil {
		return Team{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044214)"); err != nil {
		return Team{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM teams").Scan(&count); err != nil {
		return Team{}, err
	}
	if count >= 100 {
		return Team{}, fmt.Errorf("%w: team limit reached", ErrInput)
	}
	t := Team{NewID(), name, "owner"}
	if _, err = tx.Exec(ctx, "INSERT INTO teams(id,name) VALUES($1,$2)", t.ID, t.Name); err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO team_members(team_id,identity_id,role) VALUES($1,$2,'owner')", t.ID, p.ID); err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}
func (s *Store) CanManageTeam(ctx context.Context, p Principal, id string) bool {
	if p.CredentialType != "browser" {
		return false
	}
	if p.IsAdmin() {
		return true
	}
	if s.RequireFeatures(ctx, "teams") != nil {
		return false
	}
	var yes bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id=$1 AND identity_id=$2 AND role IN ('owner','admin'))", id, p.ID).Scan(&yes)
	return err == nil && yes
}
func (s *Store) TeamMembers(ctx context.Context, p Principal, id string) ([]TeamMember, error) {
	if p.CredentialType != "browser" {
		return nil, ErrForbidden
	}
	if !p.IsAdmin() {
		if err := s.RequireFeatures(ctx, "teams"); err != nil {
			return nil, err
		}
	}
	var member bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id=$1 AND identity_id=$2)", id, p.ID).Scan(&member); err != nil {
		return nil, err
	}
	if !member && !p.IsAdmin() {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, "SELECT i.id,i.name,i.email,m.role,m.username,i.avatar_style,i.avatar_seed FROM team_members m JOIN identities i ON i.id=m.identity_id WHERE m.team_id=$1 ORDER BY i.name,i.id LIMIT 200", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamMember{}
	for rows.Next() {
		var v TeamMember
		var style, seed string
		if err = rows.Scan(&v.ID, &v.Name, &v.Email, &v.Role, &v.Username, &style, &seed); err != nil {
			return nil, err
		}
		v.AvatarURL = AvatarURL(style, seed, v.ID)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SetTeamMember(ctx context.Context, p Principal, team, id, role string) error {
	if !s.CanManageTeam(ctx, p, team) {
		return ErrForbidden
	}
	if role != "" && !contains([]string{"admin", "member"}, role) {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if role != "" {
		if err = s.requireFeaturesTx(ctx, tx, "teams"); err != nil {
			return err
		}
	}
	var current string
	err = tx.QueryRow(ctx, "SELECT role FROM team_members WHERE team_id=$1 AND identity_id=$2 FOR UPDATE", team, id).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if current == "owner" {
		return fmt.Errorf("%w: team owner cannot be removed or demoted", ErrInput)
	}
	if role == "" {
		_, err = tx.Exec(ctx, "DELETE FROM team_members WHERE team_id=$1 AND identity_id=$2", team, id)
	} else {
		_, err = tx.Exec(ctx, "INSERT INTO team_members(team_id,identity_id,role) SELECT $1,id,$3 FROM identities WHERE id=$2 AND email IS NOT NULL ON CONFLICT(team_id,identity_id) DO UPDATE SET role=EXCLUDED.role", team, id, role)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'team.member',$3,$4)", p.ID, p.KeyID, team, JSON(map[string]string{"identity_id": id, "role": role})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) SetProjectMember(ctx context.Context, p Principal, project, id, team, role string) error {
	if p.CredentialType != "browser" || !p.CanManageProject(project) {
		return ErrForbidden
	}
	if (id == "") == (team == "") {
		return ErrInput
	}
	if role != "" && !contains([]string{"admin", "developer", "viewer"}, role) {
		return ErrInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var personal bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM personal_workspaces WHERE project=$1)", project).Scan(&personal); err != nil {
		return err
	}
	if personal {
		return fmt.Errorf("%w: personal workspaces cannot be shared", ErrForbidden)
	}
	if role != "" {
		features := []string{"project_rbac"}
		if team != "" {
			features = append(features, "teams")
		}
		if err = s.requireFeaturesTx(ctx, tx, features...); err != nil {
			return err
		}
	}
	if id != "" {
		if role == "" {
			_, err = tx.Exec(ctx, "DELETE FROM project_members WHERE project=$1 AND identity_id=$2", project, id)
		} else {
			_, err = tx.Exec(ctx, "INSERT INTO project_members(project,identity_id,role) SELECT $1,id,$3 FROM identities WHERE id=$2 AND email IS NOT NULL ON CONFLICT(project,identity_id) DO UPDATE SET role=EXCLUDED.role", project, id, role)
		}
	} else {
		if role == "" {
			_, err = tx.Exec(ctx, "DELETE FROM project_teams WHERE project=$1 AND team_id=$2", project, team)
		} else {
			_, err = tx.Exec(ctx, "INSERT INTO project_teams(project,team_id,role) VALUES($1,$2,$3) ON CONFLICT(project,team_id) DO UPDATE SET role=EXCLUDED.role", project, team, role)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type ProjectMember struct {
	IdentityID string `json:"identity_id,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
	Name       string `json:"name"`
	Role       string `json:"role"`
}

func (s *Store) ProjectMembers(ctx context.Context, p Principal, project string) ([]ProjectMember, error) {
	if p.CredentialType != "browser" || !p.CanManageProject(project) {
		return nil, ErrForbidden
	}
	if !p.IsAdmin() {
		if err := s.RequireFeatures(ctx, "project_rbac"); err != nil {
			return nil, err
		}
	}
	rows, err := s.Pool.Query(ctx, "SELECT m.identity_id,'' AS team_id,i.name,m.role FROM project_members m JOIN identities i ON i.id=m.identity_id WHERE m.project=$1 UNION ALL SELECT '',t.id,t.name,m.role FROM project_teams m JOIN teams t ON t.id=m.team_id WHERE m.project=$1 LIMIT 200", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectMember{}
	for rows.Next() {
		var v ProjectMember
		if err = rows.Scan(&v.IdentityID, &v.TeamID, &v.Name, &v.Role); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type Invite struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	TeamID    string    `json:"team_id,omitempty"`
	Project   string    `json:"project,omitempty"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Store) CreateInvite(ctx context.Context, p Principal, email, team, project, role string) (Invite, string, error) {
	if p.CredentialType != "browser" {
		return Invite{}, "", ErrForbidden
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return Invite{}, "", err
	}
	if team == "" && project == "" {
		return Invite{}, "", ErrInput
	}
	if team != "" && !s.CanManageTeam(ctx, p, team) {
		return Invite{}, "", ErrForbidden
	}
	if project != "" && !p.CanManageProject(project) {
		return Invite{}, "", ErrForbidden
	}
	validRoles := []string{"admin", "member"}
	if project != "" {
		validRoles = []string{"admin", "developer", "viewer"}
	}
	if !contains(validRoles, role) {
		return Invite{}, "", ErrInput
	}
	token := NewID() + NewID()
	digest := sha256.Sum256([]byte(token))
	v := Invite{NewID(), email, team, project, role, time.Now().UTC().Add(7 * 24 * time.Hour)}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return v, "", err
	}
	defer tx.Rollback(ctx)
	var personal bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM personal_workspaces WHERE project=$1)", project).Scan(&personal); err != nil {
		return v, "", err
	}
	if personal {
		return v, "", fmt.Errorf("%w: personal workspaces cannot be shared", ErrForbidden)
	}
	features := []string{"invitations"}
	if team != "" {
		features = append(features, "teams")
	}
	if project != "" {
		features = append(features, "project_rbac")
	}
	if err = s.requireFeaturesTx(ctx, tx, features...); err != nil {
		return v, "", err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044215)"); err != nil {
		return v, "", err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM invites WHERE expires_at<now()-interval '1 day'"); err != nil {
		return v, "", err
	}
	var n int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM invites WHERE accepted_at IS NULL").Scan(&n); err != nil {
		return v, "", err
	}
	if n >= 1000 {
		return v, "", ErrBusy
	}
	if _, err = tx.Exec(ctx, "INSERT INTO invites(id,digest,email,team_id,project,role,created_by,expires_at) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8)", v.ID, digest[:], email, team, project, role, p.ID, v.ExpiresAt); err != nil {
		return v, "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'invite.create',$3)", p.ID, p.KeyID, v.ID); err != nil {
		return v, "", err
	}
	return v, v.ID + "." + token, tx.Commit(ctx)
}
func (s *Store) AcceptInvite(ctx context.Context, token, name, password string, p *Principal) (string, error) {
	return s.AcceptInviteChoice(ctx, token, name, password, p, false)
}
func (s *Store) AcceptInviteChoice(ctx context.Context, token, name, password string, p *Principal, personal bool) (string, error) {
	if err := s.RequireFeatures(ctx, "invitations"); err != nil {
		return "", err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 64 {
		return "", ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(parts[1]))
	var hash []byte
	var err error
	if p == nil {
		if len(strings.TrimSpace(name)) < 1 || len(name) > 100 {
			return "", ErrInput
		}
		hash, err = HashPassword(ctx, password)
		if err != nil {
			return "", err
		}
	} else if p.CredentialType != "browser" {
		return "", ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044216)"); err != nil {
		return "", err
	}
	var email, team, project, role string
	var want []byte
	err = tx.QueryRow(ctx, "SELECT email,COALESCE(team_id,''),COALESCE(project,''),role,digest FROM invites WHERE id=$1 AND accepted_at IS NULL AND expires_at>now() FOR UPDATE", parts[0]).Scan(&email, &team, &project, &role, &want)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare(digest[:], want) != 1 {
		return "", ErrUnauthorized
	}
	features := []string{"invitations"}
	if team != "" {
		features = append(features, "teams")
	}
	if project != "" {
		features = append(features, "project_rbac")
	}
	if err = s.requireFeaturesTx(ctx, tx, features...); err != nil {
		return "", err
	}
	var id string
	if p != nil {
		if p.Email != email {
			return "", ErrForbidden
		}
		id = p.ID
	} else {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM identities WHERE email=$1)", email).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			return "", fmt.Errorf("%w: this invitation belongs to an existing account; sign in first", ErrForbidden)
		}
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044216)"); err != nil {
			return "", err
		}
		var n int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM identities WHERE email IS NOT NULL").Scan(&n); err != nil {
			return "", err
		}
		if n >= 200 {
			return "", ErrBusy
		}
		id = NewID()
		if _, err = tx.Exec(ctx, "INSERT INTO identities(id,name,email,email_verified,password_hash) VALUES($1,$2,$3,true,$4)", id, strings.TrimSpace(name), email, hash); err != nil {
			return "", err
		}
	}
	if personal {
		if _, err = personalWorkspaceTx(ctx, tx, id); err != nil {
			return "", err
		}
	}
	if team != "" && !personal {
		teamRole := role
		if project != "" {
			teamRole = "member"
		}
		if _, err = tx.Exec(ctx, "INSERT INTO team_members(team_id,identity_id,role) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", team, id, teamRole); err != nil {
			return "", err
		}
	}
	if project != "" && !personal {
		if _, err = tx.Exec(ctx, "INSERT INTO project_members(project,identity_id,role) VALUES($1,$2,$3) ON CONFLICT(project,identity_id) DO UPDATE SET role=EXCLUDED.role", project, id, role); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE invites SET accepted_at=now() WHERE id=$1", parts[0]); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE identities SET onboarding_required=false,email_verified=true WHERE id=$1", id); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,action,resource) VALUES($1,'invite.accept',$2)", id, parts[0]); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

type Challenge struct {
	ID        string
	Kind      string
	Data      []byte
	ExpiresAt time.Time
}

func (s *Store) NewChallenge(ctx context.Context, kind string, data any, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 15*time.Minute {
		return "", ErrInput
	}
	id, secret := NewID(), NewID()+NewID()
	sum := sha256.Sum256([]byte(secret))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044217)"); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM auth_challenges WHERE expires_at<now()"); err != nil {
		return "", err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM auth_challenges").Scan(&count); err != nil {
		return "", err
	}
	if count >= 1000 {
		return "", ErrBusy
	}
	if _, err = tx.Exec(ctx, "INSERT INTO auth_challenges(id,kind,digest,data,expires_at) VALUES($1,$2,$3,$4,$5)", id, kind, sum[:], JSON(data), time.Now().Add(ttl)); err != nil {
		return "", err
	}
	return id + "." + secret, tx.Commit(ctx)
}
func (s *Store) ConsumeChallenge(ctx context.Context, token, kind string) (Challenge, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 64 {
		return Challenge{}, ErrUnauthorized
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return Challenge{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(parts[1]))
	var c Challenge
	err := s.Pool.QueryRow(ctx, "DELETE FROM auth_challenges WHERE id=$1 AND kind=$2 AND digest=$3 AND expires_at>now() RETURNING id,kind,data,expires_at", parts[0], kind, sum[:]).Scan(&c.ID, &c.Kind, &c.Data, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUnauthorized
	}
	return c, err
}
