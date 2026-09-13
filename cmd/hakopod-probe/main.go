package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/hakopod/hakopod/internal/readinessprobe"
)

func main() {
	runtime.GOMAXPROCS(1)
	debug.SetMemoryLimit(24 << 20)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 2 && os.Args[1] == "install" {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot locate probe executable")
		}
		if err := copyFile(executable, "/probe/hakopod-probe", 32<<20); err != nil {
			return err
		}
		return copyFile("/etc/ssl/certs/ca-certificates.crt", "/probe/ca-certificates.crt", 1<<20)
	}
	var cfg readinessprobe.Config
	flag.StringVar(&cfg.Protocol, "protocol", "", "tcp, smtp or smtp_starttls")
	flag.IntVar(&cfg.Port, "port", 0, "Local listener port")
	flag.IntVar(&cfg.HTTPPort, "http-port", 0, "Optional local HTTP port")
	flag.StringVar(&cfg.HTTPPath, "http-path", "", "Optional HTTP readiness path")
	flag.StringVar(&cfg.ServerName, "tls-server-name", "", "Expected SMTP certificate hostname")
	flag.StringVar(&cfg.CAFile, "tls-ca-file", "/var/run/secrets/hakopod-probe/ca-certificates.crt", "PEM trust bundle")
	flag.DurationVar(&cfg.Timeout, "timeout", 3*time.Second, "Total check deadline")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected probe arguments")
	}
	return readinessprobe.Check(context.Background(), cfg)
}

func copyFile(source, destination string, limit int64) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("cannot read probe installation source")
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".probe-*")
	if err != nil {
		return fmt.Errorf("cannot create probe installation file")
	}
	defer os.Remove(out.Name())
	n, copyErr := io.Copy(out, io.LimitReader(in, limit+1))
	modeErr := out.Chmod(0555)
	closeErr := out.Close()
	if copyErr != nil || n > limit || modeErr != nil || closeErr != nil {
		return fmt.Errorf("cannot install probe within size limit")
	}
	if err := os.Rename(out.Name(), destination); err != nil {
		return fmt.Errorf("cannot finish probe installation")
	}
	return nil
}
