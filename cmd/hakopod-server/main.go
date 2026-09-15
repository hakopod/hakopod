package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"k8s.io/apimachinery/pkg/util/validation"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	runtime "github.com/hakopod/hakopod/internal/management"
	"github.com/hakopod/hakopod/internal/serverlogs"
	"github.com/hakopod/hakopod/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hakopod-server:", err)
		os.Exit(1)
	}
}
func run() error {
	if err := loadOperatorConfig(); err != nil {
		return err
	}
	deploymentMode, err := cluster.ParseDeploymentMode(os.Getenv("HAKOPOD_DEPLOYMENT_MODE"))
	if err != nil {
		return err
	}
	var processLogs *serverlogs.Buffer
	if deploymentMode == cluster.DeploymentSelfHosted {
		processLogs = serverlogs.New()
		previousLogger := slog.Default()
		defer slog.SetDefault(previousLogger)
		slog.SetDefault(slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, processLogs), nil)))
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(192 << 20)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dbURL, err := secretSetting("HAKOPOD_DATABASE_URL")
	if err != nil {
		return err
	}
	if dbURL == "" {
		return fmt.Errorf("HAKOPOD_DATABASE_URL or HAKOPOD_DATABASE_URL_FILE is required")
	}
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: check HAKOPOD_DATABASE_URL and database health")
	}
	defer db.Close()
	db.ShowcaseEnabled = true
	if err = db.Migrate(ctx); err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
		name := fs.String("name", "operator", "administrator name")
		keyFile := fs.String("key-file", "", "write the one-time bootstrap key to this restricted file")
		if err = fs.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *keyFile == "" {
			return fmt.Errorf("--key-file is required; the bootstrap credential is never printed in logs")
		}
		if err = os.MkdirAll(filepath.Dir(*keyFile), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(*keyFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		raw, err := db.Bootstrap(ctx, *name)
		if err != nil {
			f.Close()
			os.Remove(*keyFile)
			return err
		}
		_, err = f.WriteString(raw + "\n")
		if err == nil {
			err = f.Sync()
		}
		f.Close()
		if err != nil {
			return fmt.Errorf("bootstrap credential file write failed; recover administrator through PostgreSQL before retrying")
		}
		fmt.Printf("Administrator created. Bootstrap key saved to %s (0600; expires in 7 days).\n", *keyFile)
		return nil
	}
	rollout := 120 * time.Second
	if raw := os.Getenv("HAKOPOD_ROLLOUT_TIMEOUT"); raw != "" {
		rollout, err = time.ParseDuration(raw)
		if err != nil || rollout < 15*time.Second || rollout > 15*time.Minute {
			return fmt.Errorf("HAKOPOD_ROLLOUT_TIMEOUT must be 15s–15m")
		}
	}
	port := 0
	if value := os.Getenv("HAKOPOD_PUBLIC_PORT"); value != "" {
		port, err = strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("HAKOPOD_PUBLIC_PORT must be 1–65535")
		}
	}
	domain := os.Getenv("HAKOPOD_APP_DOMAIN")
	httpsPort := 0
	if value := os.Getenv("HAKOPOD_PUBLIC_HTTPS_PORT"); value != "" {
		httpsPort, err = strconv.Atoi(value)
		if err != nil || httpsPort < 1 || httpsPort > 65535 {
			return fmt.Errorf("HAKOPOD_PUBLIC_HTTPS_PORT must be 1–65535")
		}
	}
	if domain == "" || len(domain) > 190 || len(validation.IsDNS1123Subdomain(domain)) > 0 {
		return fmt.Errorf("HAKOPOD_APP_DOMAIN must be an operator-owned DNS domain (local-up configures a development domain)")
	}
	awsIdentities, err := cluster.ReadAWSIdentityBindingsFile(os.Getenv("HAKOPOD_AWS_IDENTITIES_FILE"))
	if err != nil {
		return err
	}
	ingress := env("HAKOPOD_INGRESS_CLASS", "haproxy")
	publicTCPPorts, err := cluster.ParsePublicTCPPorts(os.Getenv("HAKOPOD_PUBLIC_TCP_PORTS"))
	if err != nil {
		return err
	}
	kube, err := cluster.New(os.Getenv("HAKOPOD_KUBECONFIG"), cluster.Options{ApprovedDomains: db.ApprovedDomains, ReadinessProbeImage: os.Getenv("HAKOPOD_READINESS_PROBE_IMAGE"), DeploymentMode: deploymentMode, PublicTCPPorts: publicTCPPorts, DedicatedPublicTCPNode: os.Getenv("HAKOPOD_DEDICATED_TCP_NODE"), AWSIdentityBindings: awsIdentities, AppDomain: domain, IngressClass: ingress, RolloutTimeout: rollout, PublicPort: port, PublicHTTPSPort: httpsPort, TLSIssuer: os.Getenv("HAKOPOD_TLS_ISSUER"), RegistrySecretName: db.RegistrySecretName, VirtualNetworks: db.ResolveVirtualNetworks, SupervisorURL: os.Getenv("HAKOPOD_K3S_SUPERVISOR_URL"), ProxyNamespace: env("HAKOPOD_HAPROXY_NAMESPACE", "haproxy-controller"), ProxyConfigMap: env("HAKOPOD_HAPROXY_CONFIGMAP", "hakopod-ingress-kubernetes-ingress"), ProxyRelease: env("HAKOPOD_HAPROXY_RELEASE", "hakopod-ingress")})
	if err != nil {
		return fmt.Errorf("initialize Kubernetes client: %w", err)
	}
	if err := kube.ValidatePublicTCPInstallation(ctx); err != nil {
		return err
	}
	listen := env("HAKOPOD_LISTEN", "127.0.0.1:8080")
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	cert, key := os.Getenv("HAKOPOD_TLS_CERT"), os.Getenv("HAKOPOD_TLS_KEY")
	if (ip == nil || !ip.IsLoopback()) && cert == "" && os.Getenv("HAKOPOD_TRUST_PROXY") != "true" {
		return fmt.Errorf("non-loopback API requires TLS or explicit HAKOPOD_TRUST_PROXY=true behind an HTTPS reverse proxy")
	}
	if (cert == "") != (key == "") {
		return fmt.Errorf("set both HAKOPOD_TLS_CERT and HAKOPOD_TLS_KEY")
	}
	identityConfig, err := authConfig()
	if err != nil {
		return err
	}
	management := &api.Server{Store: db, Cluster: kube, Auth: identityConfig, ProcessLogs: processLogs}
	if err = management.ConfigureBuildRegistry(ctx, os.Getenv("HAKOPOD_BUILD_REGISTRY")); err != nil {
		return err
	}
	management.ConfigureBackups(api.BackupConfig{DatabaseURL: dbURL, PGDumpPath: env("HAKOPOD_PG_DUMP_PATH", "pg_dump"), StateDir: env("HAKOPOD_BACKUP_STATE_DIR", "/var/lib/hakopod/backups"), MaxBytes: 8 << 30, ManagedPostgres: os.Getenv("HAKOPOD_MANAGED_POSTGRES") == "true"})
	handler, wait := runtime.Start(ctx, management, domain, rollout)
	srv := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	serverErr := make(chan error, 1)
	go func() {
		if cert != "" {
			serverErr <- srv.ListenAndServeTLS(cert, key)
		} else {
			serverErr <- srv.ListenAndServe()
		}
	}()
	slog.Info("Hakopod management API ready", "listen", listen, "workers", 2, "memory_target", "192MiB")
	select {
	case <-ctx.Done():
	case err = <-serverErr:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	management.CloseTerminals()
	_ = srv.Shutdown(shutdownCtx)
	wait()
	if err != nil && !strings.Contains(err.Error(), "Server closed") {
		return err
	}
	return nil
}
func env(k, fallback string) string {
	if value := os.Getenv(k); value != "" {
		return value
	}
	return fallback
}
