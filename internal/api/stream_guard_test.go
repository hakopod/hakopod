package api

import (
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
