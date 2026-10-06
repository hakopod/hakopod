package api_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOracleHTTPApplicationExecKeepsCacheOutsideSource(t *testing.T) {
	temporary := t.TempDir()
	bin := t.TempDir()
	script := "#!/bin/sh\nset -eu\ntest \"$1\" = --cache-dir\ntest -d \"$2\"\nmkdir \"$2/discovery\"\nprintf '%s\\n' \"$2\"\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", temporary)
	var output bytes.Buffer
	if err := oracleHTTPApplicationExec(context.Background(), "config", "namespace", "service", "true", &output); err != nil {
		t.Fatal(err)
	}
	cache := strings.TrimSpace(output.String())
	if filepath.Dir(cache) != temporary || !strings.HasPrefix(filepath.Base(cache), "oracle-http-kubectl-") {
		t.Fatal("kubectl did not receive a cache under the runner temporary directory")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("kubectl cache was not removed after command completion")
	}
}
