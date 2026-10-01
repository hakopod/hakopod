package platformbackup

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
)

// WriteArchive writes a deterministic compound stream. Each source is read
// once and must have been captured while writes were quiesced.
func WriteArchive(out io.Writer, manifest Manifest, open func(Part) (io.ReadCloser, error)) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	manifest.ManifestSHA256 = manifest.Digest()
	tw := tar.NewWriter(out)
	data, _ := json.Marshal(manifest)
	if len(data) > MaxManifestBytes {
		return fmt.Errorf("%w: manifest exceeds bound", ErrInvalid)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(data)), ModTime: manifest.CapturedAt}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	for _, part := range manifest.Parts {
		r, err := open(part)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: "parts/" + part.Name, Mode: 0600, Size: part.Bytes, ModTime: manifest.CapturedAt}); err == nil {
			h := sha256.New()
			n, copyErr := io.CopyN(io.MultiWriter(tw, h), r, part.Bytes)
			err = copyErr
			if err == nil && n != part.Bytes {
				err = fmt.Errorf("%w: short archive part", ErrInvalid)
			}
			if err == nil && hex.EncodeToString(h.Sum(nil)) != part.SHA256 {
				err = fmt.Errorf("%w: part digest mismatch", ErrInvalid)
			}
		}
		closeErr := r.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return tw.Close()
}

// ReadArchive rejects links, traversal, duplicates, trailing entries and any
// stream that differs from the immutable manifest.
func ReadArchive(in io.Reader, consume func(Part, io.Reader) error) (Manifest, error) {
	return ReadArchiveWithManifest(in, nil, consume)
}

func ReadArchiveWithManifest(in io.Reader, admit func(Manifest) error, consume func(Part, io.Reader) error) (Manifest, error) {
	tr := tar.NewReader(io.LimitReader(in, MaxArchiveBytes+MaxManifestBytes+(1<<20)))
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size < 2 || h.Size > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest must be first", ErrInvalid)
	}
	var manifest Manifest
	manifestBytes, readErr := io.ReadAll(io.LimitReader(tr, h.Size))
	if readErr != nil || int64(len(manifestBytes)) != h.Size || !json.Valid(manifestBytes) || json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Validate() != nil {
		return Manifest{}, fmt.Errorf("%w: invalid manifest", ErrInvalid)
	}
	if admit != nil {
		if err = admit(manifest); err != nil {
			return Manifest{}, err
		}
	}
	parts := map[string]Part{}
	for _, part := range manifest.Parts {
		parts[part.Name] = part
	}
	seen := map[string]bool{}
	for index := range manifest.Parts {
		h, err = tr.Next()
		if err != nil || h.Typeflag != tar.TypeReg || path.Clean(h.Name) != h.Name || path.Dir(h.Name) != "parts" {
			return Manifest{}, fmt.Errorf("%w: invalid archive entry", ErrInvalid)
		}
		name := path.Base(h.Name)
		part, ok := parts[name]
		if !ok || seen[name] || h.Size != part.Bytes {
			return Manifest{}, fmt.Errorf("%w: unexpected archive entry", ErrInvalid)
		}
		if name != manifest.Parts[index].Name {
			return Manifest{}, fmt.Errorf("%w: archive order differs from manifest", ErrInvalid)
		}
		hash := sha256.New()
		limited := io.LimitReader(tr, part.Bytes)
		if err = consume(part, io.TeeReader(limited, hash)); err != nil {
			return Manifest{}, err
		}
		if _, err = io.Copy(io.Discard, limited); err != nil || hex.EncodeToString(hash.Sum(nil)) != part.SHA256 {
			return Manifest{}, fmt.Errorf("%w: archive part digest mismatch", ErrInvalid)
		}
		seen[name] = true
	}
	if _, err = tr.Next(); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing archive entry", ErrInvalid)
	}
	return manifest, nil
}
