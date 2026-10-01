package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("size bound")
	}
	return b, nil
}
func closedRemotely(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
func tlsVersion(v uint16) (string, error) {
	switch v {
	case tls.VersionTLS12:
		return "TLSv1.2", nil
	case tls.VersionTLS13:
		return "TLSv1.3", nil
	}
	return "", errors.New("unsupported TLS")
}

// Requests are serialized on this exact connection; there is no redirect or
// reconnect path. Bound the complete wire response as well as its body.
func httpsQuery(ctx context.Context, conn net.Conn, o options, sql, password string) (string, int, error) {
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return "", 0, err
	}
	request, err := http.NewRequest(http.MethodPost, "https://"+net.JoinHostPort(o.host, strconv.Itoa(o.port))+"/?database=app&wait_end_of_query=1&max_result_bytes=65536&result_overflow_mode=throw", strings.NewReader(sql))
	if err != nil {
		return "", 0, errors.New("request")
	}
	if password != "" {
		request.SetBasicAuth("app", password)
	}
	if err = request.Write(conn); err != nil {
		return "", 0, err
	}
	wire := &io.LimitedReader{R: conn, N: 96 << 10}
	response, err := http.ReadResponse(bufio.NewReaderSize(wire, 4096), request)
	if wire.N == 0 {
		return "", 0, errors.New("response header bound")
	}
	if err != nil {
		return "", 0, err
	}
	body, err := readBounded(response.Body, 64<<10)
	// Do not drain an unbounded or malformed body through Close.
	if err != nil || wire.N == 0 {
		return "", 0, errors.New("response body invalid or oversized")
	}
	_ = response.Body.Close()
	if response.Close {
		return "", 0, errors.New("server does not preserve HTTP connection")
	}
	code, _ := strconv.Atoi(response.Header.Get("X-ClickHouse-Exception-Code"))
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return "", 0, errors.New("redirect refused")
	}
	if response.StatusCode != http.StatusOK && code == 0 {
		return "", 0, errors.New("HTTP failure")
	}
	return string(body), code, nil
}
func httpsRevocation(ctx context.Context, o options) error {
	conn, err := tlsOpen(ctx, o, "")
	if err != nil {
		return err
	}
	defer conn.Close()
	out, code, err := httpsQuery(ctx, conn, o, "SELECT currentUser(), currentDatabase() FORMAT TSV", o.password)
	if err != nil || code != 0 || strings.TrimSpace(out) != "app\tapp" {
		return errors.New("session authentication")
	}
	version, err := tlsVersion(conn.ConnectionState().Version)
	if err != nil {
		return err
	}
	fmt.Printf("READY tls=%s transport=https\n", version)
	revCtx, cancel := context.WithTimeout(ctx, o.revoke)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-revCtx.Done():
			return errors.New("session remained open within revocation bound")
		case <-ticker.C:
			out, code, err = httpsQuery(revCtx, conn, o, "SELECT currentUser(), currentDatabase() FORMAT TSV", o.password)
			if revCtx.Err() != nil {
				return revCtx.Err()
			}
			if closedRemotely(err) {
				return nil
			}
			if err != nil || code != 0 || strings.TrimSpace(out) != "app\tapp" {
				return errors.New("session failed without remote closure")
			}
		}
	}
}
