// Package platformbackup defines the durable, encrypted compound archive used
// to recover a managed Supabase platform into a separate empty target.
package platformbackup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion          = 1
	Format                 = "hakopod-supabase-recovery-v1"
	MaxParts               = 6
	MaxManifestBytes       = 128 << 10
	MaxArchiveBytes  int64 = 64 << 30
)

var ErrInvalid = errors.New("invalid Supabase recovery artifact")
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var RequiredParts = []string{
	"database-encryption.tar",
	"database-roles.sql",
	"database.dump",
	"edge-functions.tar",
	"storage-objects.tar",
	"studio-snippets.tar",
}

type Part struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Claim struct {
	Component string `json:"component"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type Manifest struct {
	SchemaVersion       int               `json:"schema_version"`
	Format              string            `json:"format"`
	PlatformID          string            `json:"platform_id"`
	PlatformRevision    int64             `json:"platform_revision"`
	PlatformSpec        json.RawMessage   `json:"platform_spec"`
	Release             string            `json:"release"`
	Images              map[string]string `json:"images"`
	SourceNamespace     string            `json:"source_namespace"`
	SourceNamespaceUID  string            `json:"source_namespace_uid"`
	PVCs                []Claim           `json:"pvcs"`
	Parts               []Part            `json:"parts"`
	Verification        map[string]string `json:"verification"`
	CapturedAt          time.Time         `json:"captured_at"`
	FrozenAt            time.Time         `json:"frozen_at"`
	ThawedAt            time.Time         `json:"thawed_at"`
	Consistency         string            `json:"consistency"`
	DestinationID       string            `json:"destination_id"`
	EncryptionRecipient string            `json:"encryption_recipient"`
	ManifestSHA256      string            `json:"manifest_sha256"`
}

type Intent struct {
	Kind                   string `json:"kind"`
	Project                string `json:"project"`
	Environment            string `json:"environment"`
	SourcePlatformID       string `json:"source_platform_id"`
	TargetPlatformID       string `json:"target_platform_id,omitempty"`
	ArtifactID             string `json:"artifact_id,omitempty"`
	DestinationID          string `json:"destination_id,omitempty"`
	DestinationRevision    int64  `json:"destination_revision,omitempty"`
	ExpectedSourceRevision int64  `json:"expected_source_revision"`
	ExpectedTargetRevision int64  `json:"expected_target_revision,omitempty"`
}

func (i Intent) Validate() error {
	if i.Kind != "backup" && i.Kind != "restore" || i.Project == "" || i.Environment == "" || !idPattern.MatchString(i.SourcePlatformID) || i.ExpectedSourceRevision < 1 {
		return fmt.Errorf("%w: invalid recovery intent", ErrInvalid)
	}
	if i.Kind == "backup" {
		if !idPattern.MatchString(i.DestinationID) || i.DestinationRevision < 1 || i.TargetPlatformID != "" || i.ArtifactID != "" || i.ExpectedTargetRevision != 0 {
			return fmt.Errorf("%w: invalid backup intent", ErrInvalid)
		}
	} else if !idPattern.MatchString(i.TargetPlatformID) || i.TargetPlatformID == i.SourcePlatformID || !idPattern.MatchString(i.ArtifactID) || i.DestinationID != "" || i.DestinationRevision != 0 || i.ExpectedTargetRevision < 1 {
		return fmt.Errorf("%w: invalid restore intent", ErrInvalid)
	}
	return nil
}

type Review struct {
	ID                   string    `json:"id"`
	Intent               Intent    `json:"intent"`
	RequestHash          string    `json:"request_hash"`
	AuthorityFingerprint string    `json:"authority_fingerprint"`
	ExpiresAt            time.Time `json:"expires_at"`
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion || m.Format != Format || !idPattern.MatchString(m.PlatformID) || m.PlatformRevision < 1 {
		return fmt.Errorf("%w: unsupported identity or version", ErrInvalid)
	}
	if len(m.PlatformSpec) == 0 || len(m.PlatformSpec) > 64<<10 || !json.Valid(m.PlatformSpec) || strings.TrimSpace(m.Release) == "" {
		return fmt.Errorf("%w: platform contract is incomplete", ErrInvalid)
	}
	if m.SourceNamespace == "" || m.SourceNamespaceUID == "" || len(m.PVCs) < 1 || len(m.PVCs) > 16 || len(m.Images) < 1 || len(m.Images) > 32 {
		return fmt.Errorf("%w: source ownership inventory is incomplete", ErrInvalid)
	}
	for name, image := range m.Images {
		if name == "" || !strings.Contains(image, "@sha256:") || !digestPattern.MatchString(image[strings.LastIndex(image, "@sha256:")+8:]) {
			return fmt.Errorf("%w: image inventory is not digest pinned", ErrInvalid)
		}
	}
	seenClaims := map[string]bool{}
	for _, claim := range m.PVCs {
		key := claim.Kind + "/" + claim.Name
		if claim.Component == "" || claim.Kind != "pvc" || claim.Name == "" || claim.UID == "" || seenClaims[key] {
			return fmt.Errorf("%w: invalid PVC ownership inventory", ErrInvalid)
		}
		seenClaims[key] = true
	}
	if len(m.Parts) != len(RequiredParts) {
		return fmt.Errorf("%w: compound archive is incomplete", ErrInvalid)
	}
	for _, key := range []string{"database_roles", "database_security", "auth_metadata", "storage_metadata", "storage_bytes", "edge_functions", "studio_snippets"} {
		if !digestPattern.MatchString(m.Verification[key]) {
			return fmt.Errorf("%w: content verification evidence is incomplete", ErrInvalid)
		}
	}
	seenParts := map[string]bool{}
	var total int64
	for index, part := range m.Parts {
		if !requiredPart(part.Name) || seenParts[part.Name] || part.Bytes < 1 || part.Bytes > MaxArchiveBytes || !digestPattern.MatchString(part.SHA256) {
			return fmt.Errorf("%w: invalid archive part", ErrInvalid)
		}
		if part.Name != RequiredParts[index] {
			return fmt.Errorf("%w: archive part order is unsafe", ErrInvalid)
		}
		seenParts[part.Name] = true
		total += part.Bytes
		if total > MaxArchiveBytes {
			return fmt.Errorf("%w: archive exceeds bound", ErrInvalid)
		}
	}
	if m.CapturedAt.IsZero() || m.FrozenAt.IsZero() || m.ThawedAt.IsZero() || m.FrozenAt.After(m.CapturedAt) || m.CapturedAt.After(m.ThawedAt) || m.Consistency == "" || !idPattern.MatchString(m.DestinationID) || m.EncryptionRecipient == "" {
		return fmt.Errorf("%w: capture evidence is incomplete", ErrInvalid)
	}
	if m.ManifestSHA256 != "" && (!digestPattern.MatchString(m.ManifestSHA256) || m.ManifestSHA256 != m.Digest()) {
		return fmt.Errorf("%w: manifest digest mismatch", ErrInvalid)
	}
	return nil
}

func requiredPart(name string) bool {
	for _, required := range RequiredParts {
		if name == required {
			return true
		}
	}
	return false
}

func (m Manifest) canonical() []byte {
	m.ManifestSHA256 = ""
	// PostgreSQL jsonb may reorder fields and insert whitespace. Hash the
	// embedded spec by JSON value so a durable round trip preserves identity.
	decoder := json.NewDecoder(bytes.NewReader(m.PlatformSpec))
	decoder.UseNumber()
	var spec any
	if decoder.Decode(&spec) == nil {
		if canonical, err := json.Marshal(spec); err == nil {
			m.PlatformSpec = canonical
		}
	}

	m.Parts = append([]Part(nil), m.Parts...)
	m.PVCs = append([]Claim(nil), m.PVCs...)
	sort.Slice(m.PVCs, func(i, j int) bool { return m.PVCs[i].Name < m.PVCs[j].Name })
	b, _ := json.Marshal(m)
	return b
}

func (m Manifest) Digest() string {
	sum := sha256.Sum256(m.canonical())
	return hex.EncodeToString(sum[:])
}
