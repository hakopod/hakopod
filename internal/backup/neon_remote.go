package backup

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

const MaxNeonRecoveryObjects = 100000

type NeonRemoteInventory struct {
	SHA256 string
	Count  int64
	Bytes  int64
}

// CaptureNeonRemotePrefix creates a deterministic archive only after the
// caller has fenced every Neon storage writer at a verified remote LSN.
func CaptureNeonRemotePrefix(ctx context.Context, objects ObjectStore, prefix string, maxBytes int64) (*os.File, NeonRemoteInventory, error) {
	var inventory NeonRemoteInventory
	if objects == nil || !safeNeonPrefix(prefix) || maxBytes < 1 || maxBytes > 64<<30 {
		return nil, inventory, fmt.Errorf("invalid Neon remote capture request")
	}
	keys, token := []string{}, ""
	tokens := map[string]bool{}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, inventory, err
		}
		page, next, err := objects.ListPrefix(ctx, prefix, token)
		if err != nil {
			return nil, inventory, err
		}
		for _, key := range page {
			if seen[key] {
				return nil, inventory, fmt.Errorf("Neon remote listing returned a duplicate key")
			}
			seen[key] = true
			keys = append(keys, key)
		}
		if len(keys) > MaxNeonRecoveryObjects {
			return nil, inventory, fmt.Errorf("Neon remote object count exceeds bound")
		}
		if next == "" {
			break
		}
		if next == token || tokens[next] {
			return nil, inventory, fmt.Errorf("Neon remote listing did not advance")
		}
		tokens[next] = true
		token = next
	}
	sort.Strings(keys)
	file, err := os.CreateTemp("", "hakopod-neon-remote-*.tar")
	if err != nil {
		return nil, inventory, err
	}
	fail := func(e error) (*os.File, NeonRemoteInventory, error) {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, NeonRemoteInventory{}, e
	}
	_ = file.Chmod(0600)
	tw := tar.NewWriter(file)
	index := sha256.New()
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if !strings.HasPrefix(key, prefix) || !safeNeonObjectName(strings.TrimPrefix(key, prefix)) {
			return fail(fmt.Errorf("Neon remote listing escaped its prefix"))
		}
		body, size, err := objects.Get(ctx, key)
		if err != nil || size < 1 || inventory.Bytes+size > maxBytes {
			if body != nil {
				_ = body.Close()
			}
			return fail(fmt.Errorf("read bounded Neon remote object: %w", err))
		}
		name := strings.TrimPrefix(key, prefix)
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size}); err != nil {
			_ = body.Close()
			return fail(err)
		}
		h := sha256.New()
		n, copyErr := io.CopyN(io.MultiWriter(tw, h), body, size)
		closeErr := body.Close()
		if copyErr != nil || n != size || closeErr != nil {
			return fail(fmt.Errorf("copy Neon remote object"))
		}
		_, _ = io.WriteString(index, name+"\x00"+strconv.FormatInt(size, 10)+"\x00"+hex.EncodeToString(h.Sum(nil))+"\n")
		inventory.Count++
		inventory.Bytes += size
	}
	if inventory.Count == 0 || tw.Close() != nil || file.Sync() != nil {
		return fail(fmt.Errorf("Neon remote prefix is empty or could not be archived"))
	}
	inventory.SHA256 = hex.EncodeToString(index.Sum(nil))
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return fail(err)
	}
	return file, inventory, nil
}

// RestoreNeonRemotePrefix refuses a nonempty destination and verifies every
// restored object against the immutable inventory digest.
func RestoreNeonRemotePrefix(ctx context.Context, objects ObjectStore, prefix string, input io.Reader, expected NeonRemoteInventory, maxBytes int64) error {
	if objects == nil || !safeNeonPrefix(prefix) || expected.Count < 1 || expected.Count > MaxNeonRecoveryObjects || expected.Bytes < 1 || expected.Bytes > maxBytes {
		return fmt.Errorf("invalid Neon remote restore request")
	}
	existingKeys, err := listNeonRemoteKeys(ctx, objects, prefix)
	if err != nil {
		return err
	}
	verified, err := os.CreateTemp("", "hakopod-neon-restore-*.tar")
	if err != nil {
		return err
	}
	defer func() { _ = verified.Close(); _ = os.Remove(verified.Name()) }()
	if _, err = io.Copy(verified, io.LimitReader(input, maxBytes+(64<<20)+1)); err != nil {
		return err
	}
	if stat, e := verified.Stat(); e != nil || stat.Size() > maxBytes+(64<<20) {
		return fmt.Errorf("Neon remote archive exceeds bound")
	}
	if _, err = verified.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tr := tar.NewReader(bufio.NewReader(verified))
	index := sha256.New()
	var count, total int64
	names := map[string]bool{}
	digests := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || len(header.Name) > 512 || !safeNeonObjectName(header.Name) || names[header.Name] || header.Size < 1 || total+header.Size > maxBytes || count >= MaxNeonRecoveryObjects {
			return fmt.Errorf("invalid Neon remote archive")
		}
		names[header.Name] = true
		h := sha256.New()
		if n, e := io.CopyN(h, tr, header.Size); e != nil || n != header.Size {
			return fmt.Errorf("invalid Neon remote archive object")
		}
		digest := hex.EncodeToString(h.Sum(nil))
		digests[header.Name] = digest
		_, _ = io.WriteString(index, header.Name+"\x00"+strconv.FormatInt(header.Size, 10)+"\x00"+digest+"\n")
		count++
		total += header.Size
	}
	if count != expected.Count || total != expected.Bytes || hex.EncodeToString(index.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("Neon remote inventory digest mismatch")
	}
	existing := map[string]bool{}
	for _, key := range existingKeys {
		name := strings.TrimPrefix(key, prefix)
		want, ok := digests[name]
		if !ok {
			return fmt.Errorf("Neon recovery staging prefix contains an unknown object")
		}
		body, size, getErr := objects.Get(ctx, key)
		if getErr != nil {
			return getErr
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(body, maxBytes+1))
		closeErr := body.Close()
		if copyErr != nil || closeErr != nil || n != size || n < 1 || n > maxBytes || hex.EncodeToString(h.Sum(nil)) != want {
			return fmt.Errorf("Neon recovery staging object changed")
		}
		existing[name] = true
	}
	if _, err = verified.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tr = tar.NewReader(bufio.NewReader(verified))
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		header, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read verified Neon remote archive")
		}
		if existing[header.Name] {
			if _, err = io.CopyN(io.Discard, tr, header.Size); err != nil {
				return err
			}
			continue
		}
		written, digest, putErr := objects.Put(ctx, prefix+header.Name, io.LimitReader(tr, header.Size), header.Size)
		if putErr != nil || written != header.Size || digest != digests[header.Name] {
			return fmt.Errorf("restore Neon remote object failed")
		}
	}
	observed, err := inventoryNeonRemotePrefix(ctx, objects, prefix, maxBytes)
	if err != nil || observed != expected {
		return fmt.Errorf("restored Neon remote inventory did not verify")
	}
	return nil
}

func listNeonRemoteKeys(ctx context.Context, objects ObjectStore, prefix string) ([]string, error) {
	keys, token := []string{}, ""
	seenKeys, seenTokens := map[string]bool{}, map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, next, err := objects.ListPrefix(ctx, prefix, token)
		if err != nil {
			return nil, err
		}
		for _, key := range page {
			if !strings.HasPrefix(key, prefix) || seenKeys[key] {
				return nil, fmt.Errorf("invalid Neon remote listing")
			}
			seenKeys[key] = true
			keys = append(keys, key)
			if len(keys) > MaxNeonRecoveryObjects {
				return nil, fmt.Errorf("Neon remote object count exceeds bound")
			}
		}
		if next == "" {
			break
		}
		if next == token || seenTokens[next] {
			return nil, fmt.Errorf("Neon remote listing did not advance")
		}
		seenTokens[next] = true
		token = next
	}
	sort.Strings(keys)
	return keys, nil
}

func inventoryNeonRemotePrefix(ctx context.Context, objects ObjectStore, prefix string, maxBytes int64) (NeonRemoteInventory, error) {
	file, inventory, err := CaptureNeonRemotePrefix(ctx, objects, prefix, maxBytes)
	if file != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	return inventory, err
}

func safeNeonPrefix(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && !strings.Contains(value, "..") && strings.HasSuffix(value, "/")
}
func safeNeonObjectName(value string) bool {
	return value != "" && value == path.Clean(value) && !path.IsAbs(value) && value != ".." && !strings.HasPrefix(value, "../")
}
