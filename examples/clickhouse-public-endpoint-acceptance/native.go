package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type nativeRejection struct{ check string }

func (e *nativeRejection) Error() string { return "native rejection" }

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("output bound")
	}
	return b.Buffer.Write(p)
}

// A one-connection transparent relay prevents clickhouse-client reconnecting
// after revocation. TLS remains end-to-end between the pinned client and server.
type nativeRelay struct {
	listener     net.Listener
	upstream     net.Conn
	downstream   net.Conn
	address      string
	version      chan uint16
	remoteClosed chan struct{}
	stop         atomic.Bool
	mu           sync.Mutex
	once         sync.Once
}

func newNativeRelay(ctx context.Context, o options) (*nativeRelay, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &nativeRelay{listener: listener, address: listener.Addr().String(), version: make(chan uint16, 1), remoteClosed: make(chan struct{}, 1)}
	go func() {
		local, err := listener.Accept()
		_ = listener.Close()
		if err != nil {
			return
		}
		r.mu.Lock()
		if r.stop.Load() {
			r.mu.Unlock()
			local.Close()
			return
		}
		r.downstream = local
		r.mu.Unlock()
		step, cancel := context.WithTimeout(ctx, 3*time.Second)
		remote, err := (&net.Dialer{}).DialContext(step, "tcp", o.address)
		cancel()
		if err != nil {
			local.Close()
			return
		}
		r.mu.Lock()
		if r.stop.Load() {
			r.mu.Unlock()
			remote.Close()
			local.Close()
			return
		}
		r.upstream = remote
		r.mu.Unlock()
		defer remote.Close()
		defer local.Close()
		go func() { _, _ = io.Copy(remote, local) }()
		tap := &serverHelloTap{destination: local, version: r.version}
		_, _ = io.Copy(tap, &remoteClosureReader{Reader: remote, closed: func() {
			if !r.stop.Load() {
				select {
				case r.remoteClosed <- struct{}{}:
				default:
				}
			}
		}})
	}()
	return r, nil
}

// Observe the upstream Read itself. A downstream Write error from io.Copy
// must never be attributed to the remote database closing its socket.
type remoteClosureReader struct {
	io.Reader
	closed func()
}

func (r *remoteClosureReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if closedRemotely(err) {
		r.closed()
	}
	return n, err
}
func (r *nativeRelay) Close() {
	r.once.Do(func() {
		r.stop.Store(true)
		r.listener.Close()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.upstream != nil {
			r.upstream.Close()
		}
		if r.downstream != nil {
			r.downstream.Close()
		}
	})
}

// ServerHello is plaintext even in TLS 1.3. Inspect only this bounded handshake
// to report the actual client session's version; no application bytes are saved.
type serverHelloTap struct {
	destination io.Writer
	pending     []byte
	done        bool
	version     chan uint16
}

func (t *serverHelloTap) Write(p []byte) (int, error) {
	if !t.done {
		if len(t.pending)+len(p) > 65536 {
			t.done = true
			t.pending = nil
		} else {
			t.pending = append(t.pending, p...)
			if version, complete := parseServerHello(t.pending); complete {
				t.done = true
				t.pending = nil
				if version != 0 {
					select {
					case t.version <- version:
					default:
					}
				}
			}
		}
	}
	return t.destination.Write(p)
}
func parseServerHello(wire []byte) (uint16, bool) {
	handshake := []byte{}
	for len(wire) >= 5 {
		length := int(binary.BigEndian.Uint16(wire[3:5]))
		if length > 18432 {
			return 0, true
		}
		if len(wire) < 5+length {
			return 0, false
		}
		if wire[0] != 22 {
			return 0, true
		}
		handshake = append(handshake, wire[5:5+length]...)
		wire = wire[5+length:]
		if len(handshake) < 4 {
			continue
		}
		if handshake[0] != 2 {
			return 0, true
		}
		size := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
		if size > 65532 {
			return 0, true
		}
		if len(handshake) < size+4 {
			continue
		}
		hello := handshake[4 : 4+size]
		if len(hello) < 38 {
			return 0, true
		}
		version := binary.BigEndian.Uint16(hello[:2])
		offset := 35 + int(hello[34])
		if offset+3 > len(hello) {
			return 0, true
		}
		offset += 3
		if offset == len(hello) {
			return version, true
		}
		if offset+2 > len(hello) {
			return 0, true
		}
		end := offset + 2 + int(binary.BigEndian.Uint16(hello[offset:offset+2]))
		offset += 2
		if end != len(hello) {
			return 0, true
		}
		for offset < end {
			if offset+4 > end {
				return 0, true
			}
			kind := binary.BigEndian.Uint16(hello[offset : offset+2])
			n := int(binary.BigEndian.Uint16(hello[offset+2 : offset+4]))
			offset += 4
			if offset+n > end {
				return 0, true
			}
			if kind == 43 {
				if n != 2 {
					return 0, true
				}
				version = binary.BigEndian.Uint16(hello[offset : offset+2])
			}
			offset += n
		}
		return version, true
	}
	return 0, false
}
func xmlText(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
func nativeCommand(ctx context.Context, o options, sql, check string, relay *nativeRelay) (*exec.Cmd, func(), error) {
	binary, err := exec.LookPath("clickhouse-client")
	if err != nil {
		return nil, nil, errors.New("pinned client unavailable")
	}
	directory, err := os.MkdirTemp("", "clickhouse-public-probe-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	ca := o.caBytes
	host := o.host
	password := o.password
	secure := "true"
	switch check {
	case "wrong-ca-rejected":
		ca, err = unrelatedCA()
	case "wrong-hostname-rejected":
		host = "wrong-hostname.invalid"
	case "bad-password-rejected":
		password = "invalid-public-probe-password"
	case "plaintext-rejected":
		secure = "false"
		password = "invalid-public-probe-password"
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	caPath := filepath.Join(directory, "ca.crt")
	if err = os.WriteFile(caPath, ca, 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	_, port, err := net.SplitHostPort(relay.address)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	config := fmt.Sprintf("<config><host>127.0.0.1</host><port>%s</port><tls-sni-override>%s</tls-sni-override><database>app</database><user>app</user><password>%s</password><secure>%s</secure><connect_timeout>3</connect_timeout><receive_timeout>5</receive_timeout><send_timeout>5</send_timeout><send_logs_level>none</send_logs_level><openSSL><client><caConfig>%s</caConfig><verificationMode>strict</verificationMode><extendedVerification>true</extendedVerification><loadDefaultCAFile>false</loadDefaultCAFile><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></config>", port, xmlText(host), xmlText(password), secure, xmlText(caPath))
	configPath := filepath.Join(directory, "client.xml")
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	command := exec.CommandContext(ctx, binary, "--config-file="+configPath, "--multiquery", "--query", sql)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CLICKHOUSE_PASSWORD=") {
			command.Env = append(command.Env, value)
		}
	}
	command.WaitDelay = time.Second
	return command, cleanup, nil
}

var nativeCode = regexp.MustCompile(`Code: (\d+)\.`)

func nativeFailure(check, stderr string) bool {
	match := nativeCode.FindStringSubmatch(stderr)
	if len(match) != 2 {
		return false
	}
	code := match[1]
	lower := strings.ToLower(stderr)
	switch check {
	case "bad-password-rejected":
		return code == "516"
	case "wrong-hostname-rejected":
		return (code == "210" || code == "344") && (strings.Contains(lower, "certificate") || strings.Contains(lower, "ssl")) && (strings.Contains(lower, "verify") || strings.Contains(lower, "verification") || strings.Contains(lower, "hostname"))
	case "wrong-ca-rejected":
		return (code == "210" || code == "344") && (strings.Contains(lower, "certificate") || strings.Contains(lower, "ssl")) && (strings.Contains(lower, "verify") || strings.Contains(lower, "verification") || strings.Contains(lower, "issuer"))
	case "plaintext-rejected":
		return (code == "32" || code == "210") && (strings.Contains(lower, "connection reset by peer") || strings.Contains(lower, "attempt to read after eof") || strings.Contains(lower, "unexpected eof") || strings.Contains(lower, "connection closed"))
	}
	return false
}
func nativeQuery(ctx context.Context, o options, sql, check string) (string, error) {
	// Optional native fingerprint evidence is deliberately a separate strict TLS
	// preflight. The actual client still checks its own CA and SNI identity.
	preflight, err := tlsOpen(ctx, o, "")
	if err != nil {
		return "", err
	}
	_ = preflight.Close()
	step, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	relay, err := newNativeRelay(step, o)
	if err != nil {
		return "", err
	}
	defer relay.Close()
	command, cleanup, err := nativeCommand(step, o, sql, check, relay)
	if err != nil {
		return "", err
	}
	defer cleanup()
	stdout, stderr := &boundedBuffer{limit: 64 << 10}, &boundedBuffer{limit: 16 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	if step.Err() != nil {
		return "", errors.New("native query deadline")
	}
	if err != nil {
		if nativeFailure(check, stderr.String()) {
			return "", &nativeRejection{check: check}
		}
		return "", errors.New("native client failed")
	}
	if check != "verified" {
		return "", nil
	}
	select {
	case version := <-relay.version:
		if _, err = tlsVersion(version); err != nil {
			return "", err
		}
	default:
		return "", errors.New("native session TLS version missing")
	}
	return stdout.String(), nil
}
func nativeRevocation(ctx context.Context, o options) error {
	step, cancel := context.WithTimeout(ctx, o.revoke+10*time.Second)
	defer cancel()
	relay, err := newNativeRelay(step, o)
	if err != nil {
		return err
	}
	defer relay.Close()
	seconds := int(o.revoke.Seconds()) + 15
	sql := "SELECT currentUser(), currentDatabase() FORMAT TSV; SELECT sleepEachRow(1) FROM numbers(" + strconv.Itoa(seconds) + ") SETTINGS max_block_size=1, max_threads=1, max_execution_time=" + strconv.Itoa(seconds+5) + " FORMAT TSV"
	command, cleanup, err := nativeCommand(step, o, sql, "verified", relay)
	if err != nil {
		return err
	}
	defer cleanup()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &boundedBuffer{limit: 16 << 10}
	command.Stderr = stderr
	if err = command.Start(); err != nil {
		return err
	}
	defer func() { cancel(); _ = command.Process.Kill(); _ = command.Wait() }()
	lines := make(chan string, 2)
	scanDone := make(chan error, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		scan.Buffer(make([]byte, 128), 256)
		count := 0
		for scan.Scan() {
			count++
			if count > seconds+2 {
				scanDone <- errors.New("native row bound")
				return
			}
			select {
			case lines <- scan.Text():
			case <-step.Done():
				scanDone <- step.Err()
				return
			}
		}
		scanDone <- scan.Err()
	}()
	select {
	case line := <-lines:
		if line != "app\tapp" {
			return errors.New("native session identity")
		}
	case <-time.After(6 * time.Second):
		return errors.New("native identity timeout")
	case <-step.Done():
		return step.Err()
	}
	var version uint16
	select {
	case version = <-relay.version:
	case <-time.After(time.Second):
		return errors.New("native TLS version missing")
	}
	text, err := tlsVersion(version)
	if err != nil {
		return err
	}
	fmt.Printf("READY tls=%s transport=native\n", text)
	revokeAt := time.Now().Add(o.revoke)
	deadline := time.NewTimer(o.revoke)
	defer deadline.Stop()
	for {
		select {
		case <-step.Done():
			return step.Err()
		case <-deadline.C:
			return errors.New("native session remained open")
		case line := <-lines:
			if line != "0" {
				return errors.New("native heartbeat invalid")
			}
		case scanErr := <-scanDone:
			if scanErr != nil {
				return errors.New("native output failed")
			}
			err = command.Wait()
			if err == nil || step.Err() != nil || time.Now().After(revokeAt) {
				return errors.New("native query completed without revocation")
			}
			if !nativeFailure("plaintext-rejected", stderr.String()) {
				return errors.New("native failure was not session closure")
			}
			select {
			case <-relay.remoteClosed:
				return nil
			case <-time.After(time.Second):
				return errors.New("native upstream closure unproved")
			}
		}
	}
}
