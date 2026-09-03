package store

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Profile struct {
	Name        string `json:"name"`
	AvatarStyle string `json:"avatar_style"`
	AvatarSeed  string `json:"avatar_seed"`
	AvatarURL   string `json:"avatar_url"`
	Revision    int64  `json:"revision"`
}

type ProfileInput struct {
	Name             string `json:"name"`
	AvatarStyle      string `json:"avatar_style"`
	AvatarSeed       string `json:"avatar_seed"`
	ExpectedRevision int64  `json:"expected_revision"`
}

var avatarSeedPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,29}$`)

func AvatarURL(style, seed, identity string) string {
	if style != "identicon" && style != "glass" {
		return ""
	}
	if seed == "" {
		seed = identity
	}
	return "https://api.dicebear.com/10.x/" + style + "/svg?seed=" + url.QueryEscape(seed) + "&size=96"
}

func (s *Store) Profile(ctx context.Context, p Principal) (Profile, error) {
	var result Profile
	if !p.IsHuman() {
		return result, ErrForbidden
	}
	err := s.Pool.QueryRow(ctx, "SELECT name,avatar_style,avatar_seed,profile_revision FROM identities WHERE id=$1 AND NOT disabled", p.ID).Scan(&result.Name, &result.AvatarStyle, &result.AvatarSeed, &result.Revision)
	result.AvatarURL = AvatarURL(result.AvatarStyle, result.AvatarSeed, p.ID)
	return result, err
}

func (s *Store) UpdateProfile(ctx context.Context, p Principal, in ProfileInput) (Profile, error) {
	if p.CredentialType != "browser" || !p.IsHuman() {
		return Profile{}, ErrForbidden
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 100 || strings.ContainsAny(in.Name, "\x00\r\n") || !contains([]string{"initials", "identicon", "glass"}, in.AvatarStyle) || (in.AvatarSeed != "" && !avatarSeedPattern.MatchString(in.AvatarSeed)) || in.ExpectedRevision < 1 {
		return Profile{}, fmt.Errorf("%w: choose a display name and a supported avatar", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback(ctx)
	var result Profile
	err = tx.QueryRow(ctx, "UPDATE identities SET name=$2,avatar_style=$3,avatar_seed=$4,profile_revision=profile_revision+1 WHERE id=$1 AND NOT disabled AND profile_revision=$5 RETURNING name,avatar_style,avatar_seed,profile_revision", p.ID, in.Name, in.AvatarStyle, in.AvatarSeed, in.ExpectedRevision).Scan(&result.Name, &result.AvatarStyle, &result.AvatarSeed, &result.Revision)
	if err == pgx.ErrNoRows {
		return Profile{}, ErrConflict
	}
	if err != nil {
		return Profile{}, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource) VALUES($1,$2,'profile.update',$1)", p.ID, p.KeyID)
	if err != nil {
		return Profile{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Profile{}, err
	}
	result.AvatarURL = AvatarURL(result.AvatarStyle, result.AvatarSeed, p.ID)
	return result, nil
}

func (s *Store) SetTeamUsername(ctx context.Context, p Principal, team, user, username string) (TeamMember, error) {
	var result TeamMember
	if p.CredentialType != "browser" || !p.IsHuman() || (user != p.ID && !s.CanManageTeam(ctx, p, team)) {
		return result, ErrForbidden
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || contains([]string{"admin", "administrator", "owner", "system", "support", "hakopod", "root"}, username) {
		return result, fmt.Errorf("%w: use 2–30 lowercase letters, digits, underscores or hyphens; reserved names are unavailable", ErrInput)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = s.requireFeaturesTx(ctx, tx, "teams"); err != nil {
		return result, err
	}
	var isMember bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id=$1 AND identity_id=$2)", team, p.ID).Scan(&isMember); err != nil {
		return result, err
	}
	if !isMember && !p.IsAdmin() {
		return result, ErrForbidden
	}
	tag, err := tx.Exec(ctx, "UPDATE team_members SET username=$3 WHERE team_id=$1 AND identity_id=$2", team, user, username)
	if err != nil {
		if pg, ok := err.(*pgconn.PgError); ok && pg.Code == "23505" {
			return result, fmt.Errorf("%w: this username is taken in this team", ErrConflict)
		}
		return result, err
	}
	if tag.RowsAffected() != 1 {
		return result, pgx.ErrNoRows
	}
	var style, seed string
	err = tx.QueryRow(ctx, "SELECT i.id,i.name,i.email,m.role,m.username,i.avatar_style,i.avatar_seed FROM team_members m JOIN identities i ON i.id=m.identity_id WHERE m.team_id=$1 AND i.id=$2", team, user).Scan(&result.ID, &result.Name, &result.Email, &result.Role, &result.Username, &style, &seed)
	if err != nil {
		return result, err
	}
	result.AvatarURL = AvatarURL(style, seed, result.ID)
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'team.username',$3,$4)", p.ID, p.KeyID, team, JSON(map[string]string{"identity_id": user, "username": username}))
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
