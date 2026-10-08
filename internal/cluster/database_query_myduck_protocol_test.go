package cluster

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
)

// This protocol fixture verifies dispatch only. Native acceptance verifies MyDuck.
func TestMyDuckQueryUsesExtendedTextProtocolWithoutParameters(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		backend := pgproto3.NewBackend(conn, conn)
		if _, e = backend.ReceiveStartupMessage(); e != nil {
			done <- e
			return
		}
		backend.Send(&pgproto3.AuthenticationOk{})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if e = backend.Flush(); e != nil {
			done <- e
			return
		}
		parsed, bound := false, false
		for {
			message, e := backend.Receive()
			if e != nil {
				done <- e
				return
			}
			switch m := message.(type) {
			case *pgproto3.Query:
				done <- fmt.Errorf("simple query dispatch")
				return
			case *pgproto3.Parse:
				parsed = true
				backend.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Describe:
				backend.Send(&pgproto3.ParameterDescription{})
				backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 20, DataTypeSize: 8, TypeModifier: -1}}})
			case *pgproto3.Bind:
				if !parsed || len(m.Parameters) != 0 || len(m.ResultFormatCodes) != 1 || m.ResultFormatCodes[0] != 0 {
					done <- fmt.Errorf("dispatch lacks extended text bind")
					return
				}
				bound = true
				backend.Send(&pgproto3.BindComplete{})
			case *pgproto3.Execute:
				backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("9007199254740993")}})
				backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			case *pgproto3.Sync:
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if e = backend.Flush(); e != nil {
					done <- e
					return
				}
				if bound {
					done <- nil
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, "postgres://postgres@"+listener.Addr().String()+"/app?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer closeQueryConnection(conn)
	rows, err := conn.Query(ctx, "SELECT 9007199254740993", myduckQueryArguments(nil)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() || len(rows.RawValues()) != 1 || string(rows.RawValues()[0]) != "9007199254740993" {
		t.Fatal("text result changed")
	}
	rows.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
