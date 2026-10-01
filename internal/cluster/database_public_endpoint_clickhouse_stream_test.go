package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// These synthetic transports distinguish database EOF from a broken exec bridge.
func TestClickHousePublicStreamPreservesRemoteEOF(t *testing.T) {
	conn := clickhousePublicProbeStream(context.Background(), func(ctx context.Context, stream io.ReadWriter) error {
		_, err := io.WriteString(stream, "complete response")
		return err
	})
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != "complete response" {
		t.Fatalf("remote completion lost bytes or EOF: %q, %v", data, err)
	}
	if n, err := conn.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("remote completion was not EOF: %d, %v", n, err)
	}
}

func TestClickHousePublicStreamExecutorFailureIsNotRemoteEOF(t *testing.T) {
	conn := clickhousePublicProbeStream(context.Background(), func(ctx context.Context, stream io.ReadWriter) error {
		if _, err := io.WriteString(stream, "partial"); err != nil {
			return err
		}
		return errors.New("synthetic exec failure")
	})
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if string(data) != "partial" || err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("exec failure impersonated remote completion: %q, %v", data, err)
	}
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected bridge closure, got %v", err)
	}
}

func TestClickHousePublicStreamCancellationAndCloseUnblockBothDirections(t *testing.T) {
	for _, action := range []string{"cancel", "close"} {
		for _, direction := range []string{"caller-read", "caller-write", "peer-read", "peer-write"} {
			t.Run(action+"/"+direction, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started := make(chan struct{})
				finished := make(chan error, 1)
				executorDone := make(chan struct{})
				conn := clickhousePublicProbeStream(ctx, func(step context.Context, stream io.ReadWriter) error {
					defer close(executorDone)
					if direction == "peer-read" || direction == "peer-write" {
						close(started)
						var err error
						if direction == "peer-read" {
							_, err = stream.Read(make([]byte, 1))
						} else {
							_, err = stream.Write([]byte("x"))
						}
						finished <- err
						return err
					}
					<-step.Done()
					return step.Err()
				})
				defer conn.Close()
				if direction == "caller-read" || direction == "caller-write" {
					go func() {
						close(started)
						var err error
						if direction == "caller-read" {
							_, err = conn.Read(make([]byte, 1))
						} else {
							_, err = conn.Write([]byte("x"))
						}
						finished <- err
					}()
				}
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("transport did not start")
				}
				if action == "cancel" {
					cancel()
				} else if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-finished:
					if err == nil {
						t.Fatal("blocked transport unexpectedly succeeded")
					}
					if direction == "caller-read" && errors.Is(err, io.EOF) {
						t.Fatal("local interruption impersonated remote EOF")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("interruption did not unblock transport")
				}
				select {
				case <-executorDone:
				case <-time.After(2 * time.Second):
					t.Fatal("interruption left executor running")
				}
			})
		}
	}
}

func TestClickHousePublicTCPRelayExitsOnServerEOFWithOpenStdin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native ClickHouse relay uses the Linux runtime's Bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("native relay requires bash", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err == nil {
			_, err = io.WriteString(conn, "remote response\n")
		}
		serverDone <- err
	}()
	stdin, heldOpen, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	defer heldOpen.Close()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bash, "-c", clickhousePublicTCPRelay, "synthetic-clickhouse-relay", host, port)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	// Also bound exec's pipe drains if an incorrectly retained cat child survives.
	command.WaitDelay = 250 * time.Millisecond
	err = command.Run()
	if ctx.Err() != nil {
		t.Fatal("relay waited for stdin after the server closed", ctx.Err())
	}
	if err != nil {
		t.Fatalf("relay failed: %v; %s", err, stderr.String())
	}
	if stdout.String() != "remote response\n" {
		t.Fatalf("relay lost server output: %q", stdout.String())
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish")
	}
}
