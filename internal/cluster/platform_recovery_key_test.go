package cluster

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/platformbackup"
)

func TestSupabaseRecoveryCapturesOnlyPersistedPgsodiumKey(t *testing.T) {
	want := []string{"tar", "-C", "/var/lib/postgresql/pgsodium-volume/keyring", "-cf", "-", "pgsodium_root.key"}
	command := supabaseDatabaseKeyCaptureCommand()
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("unexpected pgsodium capture command: %q", command)
	}
	if strings.Contains(strings.Join(command, " "), "/etc/postgresql-custom") || command[len(command)-1] == "." {
		t.Fatal("recovery captures native image configuration instead of only persisted key material")
	}
}

func databaseKeyArchive(t *testing.T, entries []struct {
	name string
	mode int64
	body string
	kind byte
}) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: entry.kind}); err != nil {
			t.Fatal(err)
		}
		if entry.kind == tar.TypeReg {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestSanitizeSupabaseDatabaseKeyTarRequiresExactPrivateKey(t *testing.T) {
	valid := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		entries []struct {
			name string
			mode int64
			body string
			kind byte
		}
	}{
		{"malformed", []struct {
			name string
			mode int64
			body string
			kind byte
		}{{"pgsodium_root.key", 0600, strings.Repeat("z", 64), tar.TypeReg}}},
		{"symlink", []struct {
			name string
			mode int64
			body string
			kind byte
		}{{"pgsodium_root.key", 0600, "", tar.TypeSymlink}}},
		{"excess entries", []struct {
			name string
			mode int64
			body string
			kind byte
		}{{"pgsodium_root.key", 0600, valid, tar.TypeReg}, {"supautils.conf", 0600, valid, tar.TypeReg}}},
		{"wrong mode", []struct {
			name string
			mode int64
			body string
			kind byte
		}{{"pgsodium_root.key", 0644, valid, tar.TypeReg}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := databaseKeyArchive(t, test.entries)
			if file, err := sanitizeSupabaseDatabaseKeyTar(bytes.NewReader(archive), int64(len(archive))); err == nil {
				name := file.Name()
				_ = file.Close()
				_ = os.Remove(name)
				t.Fatal("unsafe key archive was accepted")
			}
		})
	}
	archive := databaseKeyArchive(t, []struct {
		name string
		mode int64
		body string
		kind byte
	}{{"pgsodium_root.key", 0600, valid, tar.TypeReg}})
	file, err := sanitizeSupabaseDatabaseKeyTar(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { name := file.Name(); _ = file.Close(); _ = os.Remove(name) }()
	value, err := io.ReadAll(file)
	if err != nil || string(value) != valid {
		t.Fatal("validated archive did not yield the exact key")
	}
	if info, err := file.Stat(); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("validated recovery key staging file is not private")
	}
	if _, err = sanitizeSupabaseDatabaseKeyTar(bytes.NewReader(archive), int64(len(archive))+1); err != platformbackup.ErrInvalid {
		t.Fatal("truncated key archive was accepted")
	}
}

func TestSupabaseDatabaseKeyRestoreCommandAtomicallyPublishesPrivateKey(t *testing.T) {
	directory := t.TempDir()
	key := filepath.Join(directory, "pgsodium_root.key")
	if err := os.WriteFile(key, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	command := supabaseDatabaseKeyRestoreCommand()
	command[len(command)-1] = directory
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(strings.Repeat("b", 64))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore command failed: %v: %s", err, output)
	}
	value, err := os.ReadFile(key)
	if err != nil || string(value) != strings.Repeat("b", 64) {
		t.Fatal("restore did not publish the exact replacement key")
	}
	info, err := os.Lstat(key)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("restored key is not a private regular file")
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".pgsodium_root.key.restore.*"))
	if err != nil || len(matches) != 0 {
		t.Fatal("restore left temporary key material behind")
	}
}
