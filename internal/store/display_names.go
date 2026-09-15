package store

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func DisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 80 {
		return "", fmt.Errorf("%w: use a name containing 1–80 characters", ErrInput)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", ErrInput
		}
	}
	return value, nil
}
func (s *Store) RenameApplication(ctx context.Context, p Principal, id, service, name string, revision int64) (Application, error) {
	name, err := DisplayName(name)
	if err != nil {
		return Application{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Application{}, err
	}
	defer tx.Rollback(ctx)
	a, err := scanApp(tx.QueryRow(ctx, "SELECT "+appCols+" FROM applications WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return a, err
	}
	if !p.CanManageApplication(a.Project, a.Environment, a.Name) {
		return a, ErrForbidden
	}
	if a.MetadataRevision != revision {
		return a, ErrConflict
	}
	if service != "" {
		if _, ok := a.Spec.Services[service]; !ok {
			return a, ErrInput
		}
		if a.ServiceDisplayNames == nil {
			a.ServiceDisplayNames = map[string]string{}
		}
		a.ServiceDisplayNames[service] = name
	} else {
		a.DisplayName = name
	}
	_, err = tx.Exec(ctx, "UPDATE applications SET display_name=$2,service_display_names=$3,metadata_revision=metadata_revision+1 WHERE id=$1", id, a.DisplayName, JSON(a.ServiceDisplayNames))
	if err != nil {
		return a, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'application.rename',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]string{"service": service, "display_name": name}))
	if err != nil {
		return a, err
	}
	a.MetadataRevision++
	return a, tx.Commit(ctx)
}
func (s *Store) RenameProject(ctx context.Context, p Principal, id, name string, revision int64) error {
	name, err := DisplayName(name)
	if err != nil {
		return err
	}
	if !p.CanManageProject(id) && !p.CanManageApplication(id, p.Environment, "") {
		return ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "UPDATE projects SET display_name=$2,metadata_revision=metadata_revision+1 WHERE name=$1 AND metadata_revision=$3", id, name, revision)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'project.rename',$3,$4)", p.ID, p.KeyID, id, JSON(map[string]string{"display_name": name}))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
