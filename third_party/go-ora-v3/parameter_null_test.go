package go_ora

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/network"
	"github.com/sijms/go-ora/v3/trace"
)

func nullTestSession(data []byte) *network.Session {
	session := network.NewSession(&configurations.ConnectionConfig{}, trace.NilTracer())
	session.SaveState(&network.SessionState{InBuffer: bytes.NewBuffer(data), OutBuffer: &bytes.Buffer{}})
	return session
}

func TestDecodePrimValueUntypedNullPreservesFollowingValue(t *testing.T) {
	session := nullTestSession([]byte{0, 1, 42})
	par := &ParameterInfo{oPrimValue: "previous value"}
	// No coder is registered. The real decoder must consume the NULL itself.
	if err := par.decodePrimValue(&Connection{session: session}, false); err != nil {
		t.Fatal("untyped NULL did not decode")
	}
	if !par.IsNull || par.oPrimValue != nil || par.BValue != nil {
		t.Fatal("untyped NULL did not replace the prior value")
	}
	value, err := session.GetClr()
	if err != nil || !bytes.Equal(value, []byte{42}) {
		t.Fatal("untyped NULL corrupted the next value")
	}
}

func TestDecodePrimValueUntypedNullRetainsReceiveLimit(t *testing.T) {
	session := nullTestSession([]byte{0xfe, 2, 1, 2, 0})
	session.SetReadLimit(1)
	par := &ParameterInfo{}
	if err := par.decodePrimValue(&Connection{session: session}, false); !errors.Is(err, network.ErrReadLimit) {
		t.Fatal("untyped NULL bypassed the receive limit")
	}
	if par.IsNull {
		t.Fatal("failed decoding claimed a confirmed NULL")
	}
}

func TestDecodePrimValueUntypedNullRejectsUnexpectedValue(t *testing.T) {
	session := nullTestSession([]byte{1, 42, 9})
	par := &ParameterInfo{}
	if err := par.decodePrimValue(&Connection{session: session}, false); err == nil || par.IsNull {
		t.Fatal("untyped NULL silently discarded a nonempty value")
	}
	if next, err := session.GetByte(); err != nil || next != 9 {
		t.Fatal("invalid NULL consumed the next protocol field")
	}
}
