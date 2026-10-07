// hakopod-myduck-storage copies a stopped MyDuck database. It is only run in an
// owned, isolated helper pod after the database process and its sessions drain.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

const archivePrefix = "HAKOPOD_MYDUCK_COLD_V1\n"
const dataDirectory = "/var/lib/myduck"
const restoreMarker = ".hakopod-restore-pending"
const maxManifestBytes = 4096

var databaseID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type fileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	SchemaVersion int         `json:"schema_version"`
	Version       string      `json:"version"`
	DatabaseID    string      `json:"database_id"`
	Revision      int64       `json:"revision"`
	Files         []fileEntry `json:"files"`
}

func main() {
	flags := flag.NewFlagSet("hakopod-myduck-storage", flag.ContinueOnError)
	mode := flags.String("mode", "", "hold, capture or restore")
	version := flags.String("version", "", "exact MyDuck version")
	id := flags.String("database-id", "", "resource identity")
	revision := flags.Int64("revision", 0, "resource revision")
	maximum := flags.Int64("max-bytes", 0, "maximum database bytes")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() != 0 {
		os.Exit(2)
	}
	if *mode == "hold" {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
		select {
		case <-stop:
		case <-time.After(30 * time.Minute):
		}
		return
	}
	if !databaseID.MatchString(*id) || *revision < 1 || len(*version) < 1 || len(*version) > 64 || *maximum < 1 || *maximum > 16<<40 {
		fmt.Fprintln(os.Stderr, "invalid managed storage request")
		os.Exit(2)
	}
	m := manifest{SchemaVersion: 1, Version: *version, DatabaseID: *id, Revision: *revision}
	var err error
	switch *mode {
	case "capture":
		err = capture(dataDirectory, m, *maximum, os.Stdout)
	case "restore":
		err = restore(dataDirectory, m, *maximum, os.Stdin)
	default:
		err = fmt.Errorf("unsupported storage operation")
	}
	if err != nil {
		// Errors contain neither database contents nor authentication material.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func regularFile(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("database file is not regular")
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, nil, fmt.Errorf("database file changed")
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		f.Close()
		return nil, nil, fmt.Errorf("database file has unexpected links")
	}
	return f, opened, nil
}

func lockDatabase(directory string) (*os.File, error) {
	f, _, err := regularFile(filepath.Join(directory, "app.db"))
	if err != nil {
		return nil, err
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 0}
	if err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		f.Close()
		return nil, fmt.Errorf("database still has an active writer")
	}
	return f, nil
}

func lockDirectoryOperation(directory string) (*os.File, error) {
	path := filepath.Join(directory, ".hakopod-storage-lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("invalid storage operation lock")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("storage operation lock has unexpected links")
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 0}
	if err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		f.Close()
		return nil, fmt.Errorf("another storage operation is active")
	}
	return f, nil
}

func capture(directory string, m manifest, maximum int64, out io.Writer) error {
	operation, err := lockDirectoryOperation(directory)
	if err != nil {
		return err
	}
	defer operation.Close()
	if _, err := os.Lstat(filepath.Join(directory, restoreMarker)); !os.IsNotExist(err) {
		return fmt.Errorf("database has an unfinished restore")
	}
	lock, err := lockDatabase(directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Lstat(filepath.Join(directory, restoreMarker)); !os.IsNotExist(err) {
		return fmt.Errorf("database has an unfinished restore")
	}
	var total int64
	files := []*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for _, name := range []string{"app.db", "app.db.wal"} {
		f, info, err := regularFile(filepath.Join(directory, name))
		if name == "app.db.wal" && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("database capture file is unavailable: %w", err)
		}
		files = append(files, f)
		if info.Size() < 1 || info.Size() > maximum-total {
			return fmt.Errorf("database capture exceeds its bound")
		}
		total += info.Size()
		h := sha256.New()
		if n, err := io.CopyN(h, f, info.Size()); err != nil || n != info.Size() {
			return fmt.Errorf("database capture checksum failed")
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		m.Files = append(m.Files, fileEntry{Name: name, Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	encoded, err := json.Marshal(m)
	if err != nil || len(encoded)+1 > maxManifestBytes {
		return fmt.Errorf("database manifest exceeds its bound")
	}
	if _, err = io.WriteString(out, archivePrefix); err != nil {
		return err
	}
	if _, err = out.Write(append(encoded, '\n')); err != nil {
		return err
	}
	for i, f := range files {
		h := sha256.New()
		if n, err := io.CopyN(io.MultiWriter(out, h), f, m.Files[i].Size); err != nil || n != m.Files[i].Size {
			return fmt.Errorf("database capture stream failed")
		}
		if hex.EncodeToString(h.Sum(nil)) != m.Files[i].SHA256 {
			return fmt.Errorf("database changed during capture")
		}
		var extra [1]byte
		if n, err := f.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
			return fmt.Errorf("database file grew during capture")
		}
	}
	return nil
}

func readManifest(in *bufio.Reader, target manifest, maximum int64) (manifest, error) {
	var m manifest
	prefix := make([]byte, len(archivePrefix))
	if _, err := io.ReadFull(in, prefix); err != nil || string(prefix) != archivePrefix {
		return m, fmt.Errorf("invalid MyDuck archive header")
	}
	line, err := in.ReadSlice('\n')
	if err != nil || len(line) > maxManifestBytes {
		return m, fmt.Errorf("invalid MyDuck archive manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid MyDuck archive manifest")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return m, fmt.Errorf("invalid trailing manifest data")
	}
	if m.SchemaVersion != 1 || m.Version != target.Version || !databaseID.MatchString(m.DatabaseID) || m.DatabaseID == target.DatabaseID || m.Revision < 1 || len(m.Files) < 1 || len(m.Files) > 2 {
		return m, fmt.Errorf("MyDuck restore requires a separate database with the same version")
	}
	var total int64
	for i, f := range m.Files {
		expected := "app.db"
		if i == 1 {
			expected = "app.db.wal"
		}
		digest, err := hex.DecodeString(f.SHA256)
		if f.Name != expected || f.Size < 1 || f.Size > maximum-total || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != f.SHA256 {
			return m, fmt.Errorf("invalid MyDuck archive file")
		}
		total += f.Size
	}
	return m, nil
}

func syncDirectory(directory string) error {
	f, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func restore(directory string, target manifest, maximum int64, input io.Reader) error {
	in := bufio.NewReaderSize(input, maxManifestBytes)
	m, err := readManifest(in, target, maximum)
	if err != nil {
		return err
	}
	operation, err := lockDirectoryOperation(directory)
	if err != nil {
		return err
	}
	defer operation.Close()
	lock, err := lockDatabase(directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Lstat(filepath.Join(directory, restoreMarker)); !os.IsNotExist(err) {
		return fmt.Errorf("target has an unfinished restore")
	}
	stage, err := os.MkdirTemp(directory, ".hakopod-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, entry := range m.Files {
		f, err := os.OpenFile(filepath.Join(stage, entry.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, copyErr := io.CopyN(io.MultiWriter(f, h), in, entry.Size)
		syncErr := f.Sync()
		closeErr := f.Close()
		if copyErr != nil || n != entry.Size || syncErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("MyDuck archive file checksum or staging failed")
		}
	}
	if _, err := in.ReadByte(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("MyDuck archive contains trailing bytes")
	}
	// Startup refuses this durable marker. A crash between the file replacements
	// therefore leaves a failed, isolated restore target, never mixed data online.
	marker, err := os.OpenFile(filepath.Join(directory, restoreMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(marker, "restore files are being replaced\n"); err != nil {
		marker.Close()
		return err
	}
	if err = marker.Sync(); err != nil {
		marker.Close()
		return err
	}
	if err = marker.Close(); err != nil {
		return err
	}
	if err = syncDirectory(directory); err != nil {
		return err
	}
	// The caller has already verified an empty, isolated target and drained its
	// process. Only these two fixed file names may be changed.
	if err = os.Remove(filepath.Join(directory, "app.db.wal")); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range m.Files {
		if err = os.Rename(filepath.Join(stage, entry.Name), filepath.Join(directory, entry.Name)); err != nil {
			return err
		}
	}
	if err = syncDirectory(directory); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(directory, restoreMarker)); err != nil {
		return err
	}
	return syncDirectory(directory)
}
