package api

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestSMTPDeliveryCancellationClosesStalledConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(accepted)
			close(closed)
			return
		}
		defer conn.Close()
		close(accepted)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var buf [1]byte
		_, _ = conn.Read(buf[:])
		close(closed)
	}()
	s := &Server{Auth: AuthConfig{SMTPAddress: listener.Addr().String(), SMTPFrom: "test@example.test", SMTPAllowDelivery: true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.sendAuthMail(ctx, "admin@example.test", "fixture", "fixture body") }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("SMTP fixture never accepted a connection")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled SMTP delivery succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP delivery ignored request cancellation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled SMTP connection remained open")
	}
}
