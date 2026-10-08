package go_ora

import (
	"bytes"
	"errors"
	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/network"
	"github.com/sijms/go-ora/v3/trace"
	"testing"
)

func TestLobReadLimitRejectsCumulativeBuffer(t *testing.T) {
	session := network.NewSession(&configurations.ConnectionConfig{}, trace.NilTracer())
	session.SaveState(&network.SessionState{InBuffer: bytes.NewBuffer([]byte{3, 1, 2, 3}), OutBuffer: &bytes.Buffer{}})
	session.SetReadLimit(4)
	lob := LobStream{conn: &Connection{session: session}}
	lob.data.Write([]byte{1, 2, 3})
	if err := lob.readData(); !errors.Is(err, network.ErrReadLimit) {
		t.Fatal(err)
	}
	if lob.data.Len() != 3 {
		t.Fatal("oversized LOB appended")
	}
}
