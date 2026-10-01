package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	mode := flag.String("mode", "probe", "probe or hold")
	check := flag.String("check", "verified", "verified, plaintext-rejected, wrong-hostname-rejected, wrong-ca-rejected or unreachable")
	host := flag.String("host", "", "reviewed public database hostname")
	port := flag.Uint("port", 0, "reviewed public database port")
	caPath := flag.String("ca", "", "downloaded public CA path")
	purpose := flag.String("purpose", "read_write", "reviewed endpoint purpose")
	revocation := flag.Duration("expect-revocation-within", 0, "keep a real session open and require it to be closed within this duration")
	flag.Parse()
	if *mode == "hold" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
		return
	}
	if *mode != "probe" || !oneOf(*check, "verified", "plaintext-rejected", "wrong-hostname-rejected", "wrong-ca-rejected", "unreachable") {
		fmt.Fprintln(os.Stderr, "provide a supported --mode and --check")
		os.Exit(2)
	}
	if *host == "" || *port < 1 || *port > 65535 || *caPath == "" || os.Getenv("PGPASSWORD") == "" {
		fmt.Fprintln(os.Stderr, "provide --host, --port, --ca and PGPASSWORD")
		os.Exit(2)
	}
	caPEM, err := os.ReadFile(*caPath)
	if err != nil {
		fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		fatal(fmt.Errorf("CA file contains no certificate"))
	}
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://app@%s:%d/app", *host, *port))
	if err != nil {
		fatal(err)
	}
	config.Password = os.Getenv("PGPASSWORD")
	config.ConnectTimeout = 5 * time.Second
	config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: *host}
	config.Fallbacks = nil
	connect := func(candidate *pgx.ConnConfig) (*pgx.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return pgx.ConnectConfig(ctx, candidate)
	}
	connection, err := connect(config)
	if *check == "unreachable" {
		if err == nil {
			connection.Close(context.Background())
			fatal(fmt.Errorf("unlisted source reached the reviewed PostgreSQL endpoint"))
		}
		fmt.Println("PASS unreachable")
		return
	}
	if err != nil {
		fatal(fmt.Errorf("verified PostgreSQL TLS connection failed"))
	}
	defer connection.Close(context.Background())
	var tlsVersion string
	var recovery bool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = connection.QueryRow(ctx, "SELECT version, pg_is_in_recovery() FROM pg_stat_ssl WHERE pid=pg_backend_pid()").Scan(&tlsVersion, &recovery)
	cancel()
	if err != nil || tlsVersion != "TLSv1.2" && tlsVersion != "TLSv1.3" {
		fatal(fmt.Errorf("native TLS evidence unavailable"))
	}
	wantRecovery := *purpose == "read_only" || *purpose == "pooled_read_only"
	if recovery != wantRecovery {
		fatal(fmt.Errorf("endpoint served the wrong PostgreSQL role"))
	}
	negative := config.Copy()
	switch *check {
	case "plaintext-rejected":
		negative.TLSConfig = nil
		negative.Fallbacks = nil
	case "wrong-hostname-rejected":
		negative.TLSConfig = config.TLSConfig.Clone()
		negative.TLSConfig.ServerName = "wrong-hostname.invalid"
	case "wrong-ca-rejected":
		negative.TLSConfig = config.TLSConfig.Clone()
		negative.TLSConfig.RootCAs = x509.NewCertPool()
	}
	if *check != "verified" {
		refused, refusedErr := connect(negative)
		if refusedErr == nil {
			refused.Close(context.Background())
			fatal(fmt.Errorf("the requested negative TLS check unexpectedly connected"))
		}
		fmt.Println("PASS " + *check)
		return
	}
	fmt.Printf("READY tls=%s recovery=%t\n", tlsVersion, recovery)
	if *revocation <= 0 {
		return
	}
	deadline := time.Now().Add(*revocation)
	for time.Now().Before(deadline) {
		probe, stop := context.WithTimeout(context.Background(), 2*time.Second)
		err = connection.Ping(probe)
		stop()
		if err != nil {
			fmt.Println("REVOKED existing PostgreSQL session closed")
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	fatal(fmt.Errorf("existing PostgreSQL session remained open after the revocation deadline"))
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
