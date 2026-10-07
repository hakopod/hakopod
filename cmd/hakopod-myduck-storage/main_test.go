package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storageFixture(t *testing.T) (string, manifest) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "app.db"), []byte("database bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "app.db.wal"), []byte("wal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "mysql.bin"), []byte("must never be archived"), 0600); err != nil {
		t.Fatal(err)
	}
	return directory, manifest{SchemaVersion: 1, Version: "test-version", DatabaseID: strings.Repeat("a", 32), Revision: 7}
}

func TestStorageLockChild(t *testing.T) {
	mode := os.Getenv("HAKOPOD_STORAGE_TEST_LOCK")
	if mode == "" {
		return
	}
	directory := os.Getenv("HAKOPOD_STORAGE_TEST_DIRECTORY")
	var lock *os.File
	var err error
	if mode == "database" {
		lock, err = lockDatabase(directory)
	} else {
		lock, err = lockDirectoryOperation(directory)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Println("locked")
	time.Sleep(30 * time.Second)
}

func TestStorageRejectsActiveWritersAndConcurrentOperations(t *testing.T) {
	for _, mode := range []string{"database", "operation"} {
		t.Run(mode, func(t *testing.T) {
			directory, m := storageFixture(t)
			command := exec.Command(os.Args[0], "-test.run=^TestStorageLockChild$")
			command.Env = append(os.Environ(), "HAKOPOD_STORAGE_TEST_LOCK="+mode, "HAKOPOD_STORAGE_TEST_DIRECTORY="+directory)
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
			select {
			case line := <-ready:
				if line != "locked\n" {
					t.Fatal("child did not acquire lock")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child lock timed out")
			}
			var archive bytes.Buffer
			if err := capture(directory, m, 1024, &archive); err == nil {
				t.Fatal("capture ran while a writer or operation held its lock")
			}
			if archive.Len() != 0 {
				t.Fatal("capture leaked bytes before acquiring its locks")
			}
		})
	}
}

func TestStorageRoundTripExcludesCredentials(t *testing.T) {
	source, m := storageFixture(t)
	var archive bytes.Buffer
	if err := capture(source, m, 1024, &archive); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Bytes(), []byte("must never be archived")) || bytes.Contains(archive.Bytes(), []byte("mysql.bin")) {
		t.Fatal("archive contains credentials")
	}
	target, dest := storageFixture(t)
	dest.DatabaseID = strings.Repeat("b", 32)
	if err := os.WriteFile(filepath.Join(target, "app.db"), []byte("empty target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restore(target, dest, 1024, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.db", "app.db.wal"} {
		want, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("restored %s differs", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, restoreMarker)); !os.IsNotExist(err) {
		t.Fatal("successful restore kept its marker")
	}
	credentials, err := os.ReadFile(filepath.Join(target, "mysql.bin"))
	if err != nil || string(credentials) != "must never be archived" {
		t.Fatal("storage helper altered target credentials")
	}
}

func TestStorageRejectsCorruptArchivesBeforeChangingTarget(t *testing.T) {
	source, m := storageFixture(t)
	var archive bytes.Buffer
	if err := capture(source, m, 1024, &archive); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	cases := map[string][]byte{
		"truncated":      append([]byte(nil), data[:len(data)-1]...),
		"trailing":       append(append([]byte(nil), data...), 0),
		"wrong version":  bytes.Replace(data, []byte("test-version"), []byte("other-version"), 1),
		"path traversal": bytes.Replace(data, []byte(`"app.db"`), []byte(`"../app.db"`), 1),
		"duplicate file": bytes.Replace(data, []byte(`"app.db.wal"`), []byte(`"app.db"`), 1),
		"unknown field":  bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1),
	}
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 1
	cases["checksum"] = corrupt
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			target, dest := storageFixture(t)
			dest.DatabaseID = strings.Repeat("b", 32)
			before, _ := os.ReadFile(filepath.Join(target, "app.db"))
			if err := restore(target, dest, 1024, bytes.NewReader(input)); err == nil {
				t.Fatal("accepted invalid archive")
			}
			after, _ := os.ReadFile(filepath.Join(target, "app.db"))
			if !bytes.Equal(before, after) {
				t.Fatal("invalid archive changed target")
			}
			if _, err := os.Stat(filepath.Join(target, restoreMarker)); !os.IsNotExist(err) {
				t.Fatal("invalid archive entered commit phase")
			}
		})
	}
}

func TestStorageRejectsUnsafeFilesAndUnfinishedRestore(t *testing.T) {
	for _, name := range []string{"symlink", "hardlink", "unfinished", "bound", "same target"} {
		t.Run(name, func(t *testing.T) {
			directory, m := storageFixture(t)
			limit := int64(1024)
			switch name {
			case "symlink":
				if err := os.Remove(filepath.Join(directory, "app.db.wal")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("mysql.bin", filepath.Join(directory, "app.db.wal")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(directory, "app.db"), filepath.Join(directory, "extra.db")); err != nil {
					t.Fatal(err)
				}
			case "unfinished":
				if err := os.WriteFile(filepath.Join(directory, restoreMarker), []byte("pending"), 0600); err != nil {
					t.Fatal(err)
				}
			case "bound":
				limit = 4
			}
			var archive bytes.Buffer
			err := capture(directory, m, limit, &archive)
			if name == "same target" {
				if err != nil {
					t.Fatal(err)
				}
				err = restore(directory, m, limit, bytes.NewReader(archive.Bytes()))
			}
			if err == nil {
				t.Fatal("unsafe operation was accepted")
			}
		})
	}
}

func TestStorageRejectsManifestOverflowAndOversizedFiles(t *testing.T) {
	target, m := storageFixture(t)
	m.DatabaseID = strings.Repeat("b", 32)
	overflow := archivePrefix + strings.Repeat(" ", maxManifestBytes) + "\n"
	if err := restore(target, m, 1024, strings.NewReader(overflow)); err == nil {
		t.Fatal("accepted unbounded manifest")
	}
	a := manifest{SchemaVersion: 1, Version: m.Version, DatabaseID: strings.Repeat("a", 32), Revision: 1, Files: []fileEntry{{Name: "app.db", Size: 1025, SHA256: strings.Repeat("0", 64)}}}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(target, m, 1024, strings.NewReader(archivePrefix+string(raw)+"\n")); err == nil {
		t.Fatal("accepted oversized file")
	}
}
