package platformbackup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type readClose struct{ io.Reader }

func (readClose) Close() error { return nil }

func fixtureManifest() (Manifest, map[string][]byte) {
	parts := map[string][]byte{}
	manifest := Manifest{SchemaVersion: 1, Format: Format, PlatformID: strings.Repeat("a", 32), PlatformRevision: 3, PlatformSpec: json.RawMessage(`{"kind":"supabase"}`), Release: "supabase-2026.09", Images: map[string]string{"postgres": "postgres@sha256:" + strings.Repeat("b", 64)}, SourceNamespace: "managed-platform-source", SourceNamespaceUID: "namespace-uid", PVCs: []Claim{{Component: "database", Kind: "pvc", Name: "database-data", UID: "pvc-uid"}}, Verification: map[string]string{}, CapturedAt: time.Unix(30, 0).UTC(), FrozenAt: time.Unix(20, 0).UTC(), ThawedAt: time.Unix(40, 0).UTC(), Consistency: "writes blocked, drained, claims reobserved", DestinationID: strings.Repeat("c", 32), EncryptionRecipient: "age1recipient"}
	for _, key := range []string{"database_roles", "database_security", "auth_metadata", "storage_metadata", "storage_bytes", "edge_functions", "studio_snippets"} {
		manifest.Verification[key] = strings.Repeat("d", 64)
	}
	for _, name := range RequiredParts {
		data := []byte("content-" + name)
		sum := sha256.Sum256(data)
		parts[name] = data
		manifest.Parts = append(manifest.Parts, Part{Name: name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))})
	}
	return manifest, parts
}

func TestCompoundArchiveRoundTrip(t *testing.T) {
	manifest, parts := fixtureManifest()
	var archive bytes.Buffer
	if err := WriteArchive(&archive, manifest, func(part Part) (io.ReadCloser, error) { return readClose{bytes.NewReader(parts[part.Name])}, nil }); err != nil {
		t.Fatal(err)
	}
	seen := map[string][]byte{}
	restored, err := ReadArchive(&archive, func(part Part, r io.Reader) error { seen[part.Name], _ = io.ReadAll(r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if restored.ManifestSHA256 == "" || restored.ManifestSHA256 != restored.Digest() || len(seen) != len(RequiredParts) {
		t.Fatal("archive evidence did not round trip")
	}
}

func TestArchiveRejectsTraversal(t *testing.T) {
	manifest, _ := fixtureManifest()
	manifest.ManifestSHA256 = manifest.Digest()
	data, _ := json.Marshal(manifest)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(data))})
	_, _ = tw.Write(data)
	_ = tw.WriteHeader(&tar.Header{Name: "parts/../database.dump", Mode: 0600, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if _, err := ReadArchive(&archive, func(Part, io.Reader) error { return nil }); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestManifestRejectsDigestMismatchAndMutableImage(t *testing.T) {
	manifest, _ := fixtureManifest()
	manifest.ManifestSHA256 = strings.Repeat("d", 64)
	if manifest.Validate() == nil {
		t.Fatal("manifest digest mismatch accepted")
	}
	manifest.ManifestSHA256 = ""
	manifest.Images["postgres"] = "postgres:latest"
	if manifest.Validate() == nil {
		t.Fatal("mutable image accepted")
	}
}

func TestManifestKeepsNeonAndSupabaseContractsSeparate(t *testing.T) {
	manifest, _ := fixtureManifest()
	manifest.Format = NeonFormat
	if manifest.Validate() == nil {
		t.Fatal("Supabase parts were accepted as a Neon archive")
	}
	manifest.PlatformSpec = json.RawMessage(`{"kind":"neon"}`)
	manifest.PVCs = nil
	manifest.Neon = &NeonIdentity{TenantID: strings.Repeat("1", 32), TimelineID: strings.Repeat("2", 32), TenantGeneration: 3, TimelineGeneration: 4, CommitLSN: "0/16B6C50", PageserverRemoteConsistentLSNs: map[string]string{"0": "0/16B6C50", "1": "0/16B6C50"}, SourceObjectPrefix: "source-platform", ObjectInventorySHA256: strings.Repeat("3", 64), ObjectCount: 3, ObjectBytes: 1024}
	manifest.Parts = nil
	manifest.Verification = map[string]string{}
	for _, key := range []string{"tenant_identity", "tenant_generation", "timeline_identity", "timeline_generation", "remote_storage"} {
		manifest.Verification[key] = strings.Repeat("e", 64)
	}
	for _, name := range NeonRequiredParts {
		manifest.Parts = append(manifest.Parts, Part{Name: name, SHA256: strings.Repeat("f", 64), Bytes: 1})
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid Neon manifest rejected: %v", err)
	}
	manifest.Format = Format
	if manifest.Validate() == nil {
		t.Fatal("Neon platform contract was accepted under the Supabase format")
	}
}
