package go_ora

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/converters"
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
		{"char_null_then_number", []uint16{oraTypes.CHAR, oraTypes.NUMBER}, [][]byte{nil, number.BValue}},
		{"number_then_char_null", []uint16{oraTypes.NUMBER, oraTypes.CHAR}, [][]byte{number.BValue, nil}},
		{"nchar_null_then_number", []uint16{oraTypes.NCHAR, oraTypes.NUMBER}, [][]byte{nil, number.BValue}},
		{"number_then_nchar_null", []uint16{oraTypes.NUMBER, oraTypes.NCHAR}, [][]byte{number.BValue, nil}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// command.read consumes a row message (7), CLR column values, then end-of-call (9).
			encoded := network.NewSession(&configurations.ConnectionConfig{}, trace.NilTracer())
			output := &bytes.Buffer{}
			encoded.SaveState(&network.SessionState{InBuffer: &bytes.Buffer{}, OutBuffer: output})
			encoded.PutBytes(7)
			for i, value := range scenario.values {
				if scenario.types[i] == oraTypes.CHAR || scenario.types[i] == oraTypes.NCHAR {
					continue
				}
				encoded.PutClr(value)
			}
			encoded.PutBytes(9)
			session := nullTestSession(output.Bytes())
			conn := &Connection{session: session, tracer: trace.NilTracer(), cStrConv: converters.NewStringConverter(873), nStrConv: converters.NewStringConverter(873), oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{oraTypes.NUMBER: &parameter_coder.NumberParameter{}, oraTypes.CHAR: &parameter_coder.StringParameter{}, oraTypes.NCHAR: &parameter_coder.StringParameter{}}}
			stmt := &defaultStmt{connection: conn, columns: make([]ParameterInfo, len(scenario.types))}
			for i, typ := range scenario.types {
				stmt.columns[i].DataType = typ
				stmt.columns[i].CharsetForm = 1
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

func TestStatementReadZeroLengthCharacterNull(t *testing.T) {
	for _, typ := range []uint16{oraTypes.CHAR, oraTypes.NCHAR} {
		t.Run(fmt.Sprintf("type_%d", typ), func(t *testing.T) {
			session := nullTestSession([]byte{7, 9})
			conn := &Connection{session: session, tracer: trace.NilTracer(), cStrConv: converters.NewStringConverter(873), nStrConv: converters.NewStringConverter(873), oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{typ: &parameter_coder.StringParameter{}}}
			stmt := &defaultStmt{connection: conn, columns: []ParameterInfo{{getDataFromServer: true}}}
			stmt.columns[0].DataType = typ
			stmt.columns[0].CharsetForm = 1
			result := &ResultSet{columnCount: 1}
			if err := stmt.read(result); err != nil {
				t.Fatal("zero-length character NULL row parsing failed")
			}
			if len(result.rows) != 1 || len(result.rows[0]) != 1 || result.rows[0][0] != nil {
				t.Fatal("zero-length character NULL row was lost")
			}
		})
	}
}

func TestBasicReadPositiveLengthCharacterNullRetainsReceiveLimit(t *testing.T) {
	for _, typ := range []uint16{oraTypes.CHAR, oraTypes.NCHAR} {
		session := nullTestSession([]byte{0xfe, 2, 1, 2, 0})
		session.SetReadLimit(1)
		basic := &parameter_coder.BasicParameter{DataType: typ, MaxLen: 1}
		if _, err := basic.BasicRead(session); !errors.Is(err, network.ErrReadLimit) {
			t.Fatal("positive-length character bypassed receive limit")
		}
	}
}
func TestBasicReadPositiveLengthCharacterNullPreservesFollowingValue(t *testing.T) {
	for _, typ := range []uint16{oraTypes.CHAR, oraTypes.NCHAR} {
		session := nullTestSession([]byte{0, 1, 42})
		basic := &parameter_coder.BasicParameter{DataType: typ, MaxLen: 1}
		value, err := basic.BasicRead(session)
		if err != nil || value != nil {
			t.Fatal("positive-length character NULL was not consumed")
		}
		next, err := session.GetClr()
		if err != nil || !bytes.Equal(next, []byte{42}) {
			t.Fatal("positive-length character corrupted following value")
		}
	}
}

func TestZeroLengthCharacterNullRemovesStaleValue(t *testing.T) {
	for _, typ := range []uint16{oraTypes.CHAR, oraTypes.NCHAR} {
		session := nullTestSession([]byte{9})
		conn := &Connection{session: session, connOption: &configurations.ConnectionConfig{}, cStrConv: converters.NewStringConverter(873), nStrConv: converters.NewStringConverter(873), oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{typ: &parameter_coder.StringParameter{}}}
		par := ParameterInfo{oPrimValue: "previous"}
		par.DataType = typ
		par.CharsetForm = 1
		par.BValue = []byte("previous")
		if par.decodeColumnValue(conn, false) != nil || !par.IsNull || par.oPrimValue != nil || par.BValue != nil {
			t.Fatal("character NULL retained stale value")
		}
	}
}

func TestBasicReadPositiveLengthCharacterFixedAndArrayNullBounds(t *testing.T) {
	for _, typ := range []uint16{oraTypes.CHAR, oraTypes.NCHAR} {
		for _, udt := range []bool{false, true} {
			session := nullTestSession([]byte{0xff, 1, 42})
			basic := &parameter_coder.BasicParameter{DataType: typ, MaxLen: 1, IsUDTPar: udt, IsArrayPar: !udt}
			value, err := basic.BasicRead(session)
			if err != nil || value != nil {
				t.Fatal("character sentinel was not consumed")
			}
			next, err := session.GetClr()
			if err != nil || !bytes.Equal(next, []byte{42}) {
				t.Fatal("character sentinel corrupted following value")
			}
			limited := nullTestSession([]byte{2, 1, 2})
			limited.SetReadLimit(1)
			if _, err = basic.BasicRead(limited); !errors.Is(err, network.ErrReadLimit) {
				t.Fatal("character fixed or array read bypassed receive limit")
			}
		}
	}
}

func TestZeroLengthCharacterFetchOverridesSeededCoder(t *testing.T) {
	for _, typ := range []uint16{oraTypes.CHAR, oraTypes.NCHAR} {
		coder := &parameter_coder.StringParameter{}
		coder.MaxLen = 32
		session := nullTestSession([]byte{9})
		conn := &Connection{session: session, oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{typ: coder}}
		par := &ParameterInfo{}
		par.DataType = typ
		if err := par.decodeColumnValue(conn, false); err != nil || !par.IsNull {
			t.Fatal("described null did not bypass seeded coder")
		}
		if next, err := session.GetByte(); err != nil || next != 9 {
			t.Fatal("described null consumed response")
		}
	}
}
func TestZeroLengthCharacterFetchBitVectorPreservesPriorValue(t *testing.T) {
	session := nullTestSession([]byte{7, 9})
	conn := &Connection{session: session, tracer: trace.NilTracer()}
	stmt := &defaultStmt{connection: conn, columns: []ParameterInfo{{oPrimValue: "previous", getDataFromServer: false}}}
	stmt.columns[0].DataType = oraTypes.CHAR
	result := &ResultSet{columnCount: 1}
	if err := stmt.read(result); err != nil || len(result.rows) != 1 || result.rows[0][0] != "previous" {
		t.Fatal("bitvector suppressed column changed")
	}
}

func TestDecodeParameterValuePositiveLengthCharacterOutputNull(t *testing.T) {
	session := nullTestSession([]byte{0, 1, 42})
	conn := &Connection{session: session, connOption: &configurations.ConnectionConfig{}, cStrConv: converters.NewStringConverter(873), nStrConv: converters.NewStringConverter(873), oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{oraTypes.CHAR: &parameter_coder.StringParameter{}}}
	par := &ParameterInfo{}
	par.DataType = oraTypes.CHAR
	par.MaxLen = 1
	par.Direction = Output
	par.CharsetForm = 1
	if err := par.decodeParameterValue(conn); err != nil || par.oPrimValue != nil {
		t.Fatal("output NULL decode failed")
	}
	if next, err := session.GetClr(); err != nil || !bytes.Equal(next, []byte{42}) {
		t.Fatal("output NULL consumed following field")
	}
}
func TestZeroLengthCharacterFetchGuardExclusions(t *testing.T) {
	for _, kind := range []string{"udt", "udt_parameter", "array_parameter", "array_size"} {
		t.Run(kind, func(t *testing.T) {
			coder := &parameter_coder.StringParameter{}
			coder.MaxLen = 32
			// A bounded read must be attempted on each excluded path.
			session := nullTestSession([]byte{2, 1, 2})
			session.SetReadLimit(1)
			conn := &Connection{session: session, connOption: &configurations.ConnectionConfig{}, cStrConv: converters.NewStringConverter(873), nStrConv: converters.NewStringConverter(873), oracleTypeCoder: map[uint16]parameter_coder.OracleParameterCoder{oraTypes.CHAR: coder}}
			par := &ParameterInfo{}
			par.DataType = oraTypes.CHAR
			par.CharsetForm = 1
			udt := false
			switch kind {
			case "udt":
				udt = true
			case "udt_parameter":
				par.IsUDTPar = true
			case "array_parameter":
				par.IsArrayPar = true
			case "array_size":
				par.ArraySize = 1
			}
			if err := par.decodeColumnValue(conn, udt); err == nil {
				t.Fatal("excluded path bypassed wire decoding")
			}
			if par.IsNull {
				t.Fatal("excluded path claimed described NULL")
			}
		})
	}
}
