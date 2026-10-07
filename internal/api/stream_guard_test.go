package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStreamGuardRechecksTrustedRuntimeAuthority(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	owner, err := db.SetupOwner(ctx, "Stream operator", "stream@example.test", "disposable-password-123", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "INSERT INTO personal_workspaces(identity_id,project) VALUES($1,'demo')", owner.ID); err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(ctx, owner.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	app := store.Application{Project: "demo", Environment: "development", Name: "app"}
	for _, scenario := range []string{"callback-revoked", "permission-narrowed"} {
		t.Run(scenario, func(t *testing.T) {
			var revoked atomic.Bool
			checked := make(chan struct{}, 1)
			scope := RuntimeScope{Identity: p.ID, Project: app.Project, Environment: app.Environment,
				Permissions: []string{"deployments:read", "deployments:write", "pods:exec"},
				Authorize: func(context.Context) error {
					select {
					case checked <- struct{}{}:
					default:
					}
					if revoked.Load() {
						return errors.New("fixture product authority revoked")
					}
					return nil
				},
			}
			if scenario == "permission-narrowed" {
				scope.Permissions = []string{"deployments:read", "pods:exec"}
			}
			stream, cancel := context.WithCancel(context.WithValue(ctx, runtimeScopeKey{}, scope))
			defer cancel()
			go s.guardStream(stream, cancel, p.KeyID, app, "deployments:write", "pods:exec")
			if scenario == "callback-revoked" {
				select {
				case <-checked:
				case <-time.After(6 * time.Second):
					t.Fatal("stream did not check product authority")
				}
				if stream.Err() != nil {
					t.Fatal("valid product authority was rejected")
				}
				revoked.Store(true)
			}
			select {
			case <-stream.Done():
			case <-time.After(6 * time.Second):
				t.Fatal("stream retained revoked or excluded product authority")
			}
			current, err := db.KeyPrincipal(ctx, p.KeyID)
			if err != nil || !current.Allows("deployments:write", app.Project, app.Environment, app.Name) {
				t.Fatal("test changed the underlying principal instead of product authority")
			}
		})
	}
}

func TestStreamGuardFailsClosedWhenPoolIsExhausted(t *testing.T) {
	db := sourceDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	connections := []*pgxpool.Conn{}
	defer func() {
		for _, connection := range connections {
			connection.Release()
		}
	}()
	for i := int32(0); i < db.Pool.Config().MaxConns; i++ {
		connection, err := db.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	stream, closeStream := context.WithCancel(ctx)
	defer closeStream()
	s := &Server{Store: db}
	started := time.Now()
	go s.guardStream(ctx, closeStream, "unavailable-authority", store.Application{Project: "demo", Environment: "development", Name: "app"}, "logs:read")
	select {
	case <-stream.Done():
		if time.Since(started) > 6*time.Second {
			t.Fatal("database pool wait exceeded the stream authority deadline")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("stream survived without authority verification")
	}
}
