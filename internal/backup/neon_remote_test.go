package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"
	"testing"
)

type neonMemoryObjects struct {
	values map[string][]byte
	list   func(string, string) ([]string, string, error)
}

func (o *neonMemoryObjects) Put(_ context.Context, key string, input io.Reader, limit int64) (int64, string, error) {
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || int64(len(data)) != limit {
		return 0, "", ErrInput
	}
	o.values[key] = data
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}
func (o *neonMemoryObjects) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	data, ok := o.values[key]
	if !ok {
		return nil, 0, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}
func (o *neonMemoryObjects) Delete(context.Context, string) error               { return nil }
func (o *neonMemoryObjects) Close()                                             {}
func (o *neonMemoryObjects) DeletePrefix(context.Context, string) (bool, error) { return false, nil }
func (o *neonMemoryObjects) ListPrefix(_ context.Context, prefix, token string) ([]string, string, error) {
	if o.list != nil {
		return o.list(prefix, token)
	}
	keys := []string{}
	for key := range o.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, "", nil
}

func TestNeonRemoteCaptureRestoreVerifiesExactInventory(t *testing.T) {
	source := &neonMemoryObjects{values: map[string][]byte{"source/index_part.json": []byte("index"), "source/layer-1": []byte("layer")}}
	archive, inventory, err := CaptureNeonRemotePrefix(context.Background(), source, "source/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	target := &neonMemoryObjects{values: map[string][]byte{}}
	if err = RestoreNeonRemotePrefix(context.Background(), target, "staging/operation/", archive, inventory, 1024); err != nil {
		t.Fatal(err)
	}
	if string(target.values["staging/operation/index_part.json"]) != "index" || string(target.values["staging/operation/layer-1"]) != "layer" {
		t.Fatal("remote inventory did not restore exactly")
	}
}

func TestNeonRemoteRestoreRefusesOccupiedPrefixAndChangedInventory(t *testing.T) {
	source := &neonMemoryObjects{values: map[string][]byte{"source/index": []byte("index")}}
	archive, inventory, err := CaptureNeonRemotePrefix(context.Background(), source, "source/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	target := &neonMemoryObjects{values: map[string][]byte{"staging/existing": []byte("foreign")}}
	if err = RestoreNeonRemotePrefix(context.Background(), target, "staging/", archive, inventory, 1024); err == nil {
		t.Fatal("occupied target prefix accepted")
	}
	_, _ = archive.Seek(0, io.SeekStart)
	delete(target.values, "staging/existing")
	inventory.SHA256 = strings.Repeat("0", 64)
	if err = RestoreNeonRemotePrefix(context.Background(), target, "staging/", archive, inventory, 1024); err == nil {
		t.Fatal("changed object inventory accepted")
	}
}

func TestNeonRemoteCaptureRejectsDuplicateAndCyclicListings(t *testing.T) {
	objects := &neonMemoryObjects{values: map[string][]byte{"source/a": []byte("a")}}
	objects.list = func(_ string, token string) ([]string, string, error) {
		if token == "" {
			return []string{"source/a"}, "again", nil
		}
		return []string{"source/a"}, "again", nil
	}
	if file, _, err := CaptureNeonRemotePrefix(context.Background(), objects, "source/", 1024); err == nil {
		if file != nil {
			file.Close()
		}
		t.Fatal("duplicate cyclic listing accepted")
	}
}

func TestNeonRemoteRestoreResumesMatchingOperationPrefix(t *testing.T) {
	source := &neonMemoryObjects{values: map[string][]byte{"source/a": []byte("a"), "source/b": []byte("b")}}
	archive, inventory, err := CaptureNeonRemotePrefix(context.Background(), source, "source/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	target := &neonMemoryObjects{values: map[string][]byte{"staging/op/a": []byte("a")}}
	if err = RestoreNeonRemotePrefix(context.Background(), target, "staging/op/", archive, inventory, 1024); err != nil {
		t.Fatal(err)
	}
	if string(target.values["staging/op/b"]) != "b" {
		t.Fatal("matching partial restore did not resume")
	}
}
