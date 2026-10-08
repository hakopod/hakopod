package go_ora

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/network"
	"github.com/sijms/go-ora/v3/parameter_coder"
	"github.com/sijms/go-ora/v3/trace"
	oraTypes "github.com/sijms/go-ora/v3/types"
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
	if err := par.decodePrimValue(&Connection{session: session, tracer: trace.NilTracer()}, false); err != nil {
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
	if err := par.decodePrimValue(&Connection{session: session, tracer: trace.NilTracer()}, false); !errors.Is(err, network.ErrReadLimit) {
		t.Fatal("untyped NULL bypassed the receive limit")
	}
	if par.IsNull {
		t.Fatal("failed decoding claimed a confirmed NULL")
	}
}

func TestDecodePrimValueUntypedNullRejectsUnexpectedValue(t *testing.T) {
	session := nullTestSession([]byte{1, 42, 9})
	par := &ParameterInfo{}
	if err := par.decodePrimValue(&Connection{session: session, tracer: trace.NilTracer()}, false); err == nil || par.IsNull {
		t.Fatal("untyped NULL silently discarded a nonempty value")
	}
	if next, err := session.GetByte(); err != nil || next != 9 {
		t.Fatal("invalid NULL consumed the next protocol field")
	}
}

// Exercise the actual row-message decoder rather than only the parameter guard.
func TestStatementReadUntypedNullPreservesRow(t *testing.T) {
	session := nullTestSession([]byte{7, 0, 9})
	conn := &Connection{session: session, tracer: trace.NilTracer()}
	stmt := &defaultStmt{connection: conn, columns: []ParameterInfo{{getDataFromServer: true}}}
	result := &ResultSet{columnCount: 1}
	if err := stmt.read(result); err != nil {
		t.Fatal("untyped NULL row parsing failed")
	}
	if len(result.rows) != 1 || len(result.rows[0]) != 1 || result.rows[0][0] != nil {
		t.Fatal("untyped NULL row was lost")
	}
	if !stmt.columns[0].IsNull {
		t.Fatal("untyped NULL column was not marked null")
	}
}

func TestStatementReadUntypedNullRespectsColumnBitVector(t *testing.T) {
	session := nullTestSession([]byte{7, 0, 9})
	conn := &Connection{session: session, tracer: trace.NilTracer()}
	stmt := &defaultStmt{connection: conn, columns: []ParameterInfo{{getDataFromServer: false}, {getDataFromServer: true}}}
	result := &ResultSet{columnCount: 2}
	if err := stmt.read(result); err != nil {
		t.Fatal("compressed NULL row parsing failed")
	}
	if len(result.rows) != 1 || len(result.rows[0]) != 2 || result.rows[0][0] != nil || result.rows[0][1] != nil {
		t.Fatal("compressed NULL columns were lost")
	}
}

func TestStatementReadNullAndNumberColumnSequences(t *testing.T) {
	number := &parameter_coder.NumberParameter{}
	if number.Encode(int64(42), nil) != nil {
		t.Fatal("numeric synthetic encoding failed")
	}
	for _, scenario := range []struct {
		name   string
		types  []uint16
		values [][]byte
	}{
		{"untyped_then_number", []uint16{0, oraTypes.NUMBER}, [][]byte{nil, number.BValue}},
		{"number_then_untyped", []uint16{oraTypes.NUMBER, 0}, [][]byte{number.BValue, nil}},
		{"typed_null_then_number", []uint16{oraTypes.NUMBER, oraTypes.NUMBER}, [][]byte{nil, number.BValue}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// command.read consumes a row message (7), CLR column values, then end-of-call (9).
			encoded := network.NewSession(&configurations.ConnectionConfig{}, trace.NilTracer())
			output := &bytes.Buffer{}
			encoded.SaveState(&network.SessionState{InBuffer: &bytes.Buffer{}, OutBuffer: output})
			encoded.PutBytes(7)
			for _, value := range scenario.values {
				encoded.PutClr(value)
			}
			encoded.PutBytes(9)
			session := nullTestSession(output.Bytes())
			conn := &Connection{session: session, tracer: trace.NilTracer(), oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{oraTypes.NUMBER: &parameter_coder.NumberParameter{}}}
			stmt := &defaultStmt{connection: conn, columns: make([]ParameterInfo, len(scenario.types))}
			for i, typ := range scenario.types {
				stmt.columns[i].DataType = typ
				stmt.columns[i].getDataFromServer = true
			}
			result := &ResultSet{columnCount: len(scenario.types)}
			if err := stmt.read(result); err != nil {
				t.Fatal("NULL and NUMBER row parsing failed")
			}
			if len(result.rows) != 1 || len(result.rows[0]) != 2 {
				t.Fatal("NULL and NUMBER row count differs")
			}
			for i, value := range scenario.values {
				if (result.rows[0][i] == nil) != (value == nil) {
					t.Fatal("NULL position differs")
				}
			}
		})
	}
}
