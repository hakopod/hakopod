package go_ora

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/network"
	"github.com/sijms/go-ora/v3/trace"
)

func TestDriverDiagnosticsConsumeWarningWithoutPrinting(t *testing.T) {
	const warning = "synthetic private identifier and value"
	packet := []byte{1, 1, 1, byte(len(warning)), 0, byte(len(warning))}
	packet = append(packet, warning...)
	newSession := func(data []byte) *network.Session {
		session := network.NewSession(&configurations.ConnectionConfig{}, trace.NilTracer())
		session.SaveState(&network.SessionState{InBuffer: bytes.NewBuffer(data), OutBuffer: &bytes.Buffer{}})
		return session
	}
	control := newSession(packet)
	if decoded, err := network.NewWarningObject(control); err != nil || decoded == nil {
		t.Fatal("synthetic warning positive control did not decode")
	}
	packet = append(packet, 42)
	session := newSession(packet)
	conn := &Connection{session: session, tracer: trace.NilTracer()}
	output, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = output, output
	defer func() { os.Stdout, os.Stderr = stdout, stderr }()
	if err := conn.ProcessTCCResponse(15); err != nil {
		t.Fatal("warning response failed")
	}
	if next, err := session.GetByte(); err != nil || next != 42 {
		t.Fatal("warning response did not consume the expected bytes")
	}
	info, err := output.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatal("driver printed a private warning without trace configuration")
	}
}
