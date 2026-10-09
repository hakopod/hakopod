// Package platformbackup defines durable, encrypted compound archives used to
// recover managed platforms into separate empty targets.
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
	"strconv"
	"strings"
	"time"
)

const (
	SchemaVersion          = 1
	Format                 = "hakopod-supabase-recovery-v1"
	NeonFormat             = "hakopod-neon-recovery-v1"
	MaxParts               = 6
	MaxManifestBytes       = 128 << 10
	MaxArchiveBytes  int64 = 64 << 30
)

var ErrInvalid = errors.New("invalid managed platform recovery artifact")
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

var NeonRequiredParts = []string{
	"tenant.json",
	"timeline.json",
	"remote-storage.tar",
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

type NeonIdentity struct {
	TenantID                       string            `json:"tenant_id"`
	TimelineID                     string            `json:"timeline_id"`
	TenantGeneration               int64             `json:"tenant_generation"`
	TimelineGeneration             int64             `json:"timeline_generation"`
	CommitLSN                      string            `json:"commit_lsn"`
	PageserverRemoteConsistentLSNs map[string]string `json:"pageserver_remote_consistent_lsns"`
	SourceObjectPrefix             string            `json:"source_object_prefix"`
	ObjectInventorySHA256          string            `json:"object_inventory_sha256"`
	ObjectCount                    int64             `json:"object_count"`
	ObjectBytes                    int64             `json:"object_bytes"`
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
	Neon                *NeonIdentity     `json:"neon,omitempty"`
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
	ID                   string              `json:"id"`
	Intent               Intent              `json:"intent"`
	RequestHash          string              `json:"request_hash"`
	AuthorityFingerprint string              `json:"authority_fingerprint"`
	ExpiresAt            time.Time           `json:"expires_at"`
	Compatibility        CompatibilityReport `json:"compatibility"`
}

type CompatibilityCheck struct {
	Code     string `json:"code"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Evidence string `json:"evidence,omitempty"`
}
type CompatibilityReport struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Checks      []CompatibilityCheck `json:"checks"`
	Blocked     bool                 `json:"blocked"`
}

func RestoreCompatibility(m Manifest, targetKind string, now time.Time) CompatibilityReport {
	checks := []CompatibilityCheck{}
	add := func(code, status, message, evidence string) {
		checks = append(checks, CompatibilityCheck{Code: code, Status: status, Message: message, Evidence: evidence})
	}
	if m.CapturedAt.IsZero() {
		add("backup_age", "blocker", "The archive has no captured recovery point.", "")
	} else if m.CapturedAt.After(now) {
		add("backup_age", "blocker", "The archive recovery point is in the future.", m.CapturedAt.UTC().Format(time.RFC3339))
	} else {
		add("backup_age", "checked", "The archive recovery point is recorded.", m.CapturedAt.UTC().Format(time.RFC3339))
	}
	var source struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(m.PlatformSpec, &source)
	if source.Kind == targetKind && m.Release != "" && len(m.Images) > 0 {
		add("application_version", "checked", "The platform release and digest-pinned image inventory match this platform kind.", m.Release)
	} else {
		add("application_version", "blocker", "The platform kind, release or image inventory is incomplete.", "")
	}
	if m.EncryptionRecipient != "" {
		add("encryption_key", "checked", "The archive declares its required encryption recipient.", m.EncryptionRecipient)
	} else {
		add("encryption_key", "blocker", "The archive has no encryption recipient reference.", "")
	}
	add("dependencies", "checked", "The immutable platform specification and owned volume inventory are present.", fmt.Sprintf("%d images, %d claims", len(m.Images), len(m.PVCs)))
	add("related_recovery_points", "unknown", "No separate related application recovery set was declared for this platform archive.", "")
	blocked := false
	for _, check := range checks {
		if check.Status == "blocker" {
			blocked = true
		}
	}
	return CompatibilityReport{GeneratedAt: now.UTC(), Checks: checks, Blocked: blocked}
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion || (m.Format != Format && m.Format != NeonFormat) || !idPattern.MatchString(m.PlatformID) || m.PlatformRevision < 1 {
		return fmt.Errorf("%w: unsupported identity or version", ErrInvalid)
	}
	if len(m.PlatformSpec) == 0 || len(m.PlatformSpec) > 64<<10 || !json.Valid(m.PlatformSpec) || strings.TrimSpace(m.Release) == "" {
		return fmt.Errorf("%w: platform contract is incomplete", ErrInvalid)
	}
	var platform struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(m.PlatformSpec, &platform) != nil || platform.Kind == "" || platform.Kind == "supabase" && m.Format != Format || platform.Kind == "neon" && m.Format != NeonFormat || platform.Kind != "supabase" && platform.Kind != "neon" {
		return fmt.Errorf("%w: platform kind does not match archive format", ErrInvalid)
	}
	if m.SourceNamespace == "" || m.SourceNamespaceUID == "" || len(m.PVCs) > 16 || len(m.Images) < 1 || len(m.Images) > 32 {
		return fmt.Errorf("%w: source ownership inventory is incomplete", ErrInvalid)
	}
	if m.Format == Format && len(m.PVCs) < 1 {
		return fmt.Errorf("%w: Supabase PVC ownership inventory is incomplete", ErrInvalid)
	}
	if m.Format == Format && m.Neon != nil {
		return fmt.Errorf("%w: Supabase artifact contains Neon identity", ErrInvalid)
	}
	if m.Format == NeonFormat {
		if m.Neon == nil || !idPattern.MatchString(m.Neon.TenantID) || !idPattern.MatchString(m.Neon.TimelineID) || m.Neon.TenantGeneration < 1 || m.Neon.TimelineGeneration < 1 || !validLSN(m.Neon.CommitLSN) || len(m.Neon.PageserverRemoteConsistentLSNs) < 1 || len(m.Neon.PageserverRemoteConsistentLSNs) > 8 || m.Neon.SourceObjectPrefix == "" || strings.HasPrefix(m.Neon.SourceObjectPrefix, "/") || strings.Contains(m.Neon.SourceObjectPrefix, "..") || !digestPattern.MatchString(m.Neon.ObjectInventorySHA256) || m.Neon.ObjectCount < 1 || m.Neon.ObjectCount > 100000 || m.Neon.ObjectBytes < 1 || m.Neon.ObjectBytes > MaxArchiveBytes {
			return fmt.Errorf("%w: Neon recovery identity or object inventory is incomplete", ErrInvalid)
		}
		commit, valid := lsnValue(m.Neon.CommitLSN)
		if !valid || commit == 0 {
			return fmt.Errorf("%w: Neon recovery commit LSN is invalid", ErrInvalid)
		}
		for name, lsn := range m.Neon.PageserverRemoteConsistentLSNs {
			visible, valid := lsnValue(lsn)
			if name == "" || !valid || visible < commit {
				return fmt.Errorf("%w: Neon pageserver recovery evidence is invalid", ErrInvalid)
			}
		}
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
	requiredParts, verificationKeys := RequiredParts, []string{"database_roles", "database_security", "auth_metadata", "storage_metadata", "storage_bytes", "edge_functions", "studio_snippets"}
	if m.Format == NeonFormat {
		requiredParts = NeonRequiredParts
		verificationKeys = []string{"tenant_identity", "tenant_generation", "timeline_identity", "timeline_generation", "remote_storage"}
	}
	if len(m.Parts) != len(requiredParts) {
		return fmt.Errorf("%w: compound archive is incomplete", ErrInvalid)
	}
	for _, key := range verificationKeys {
		if !digestPattern.MatchString(m.Verification[key]) {
			return fmt.Errorf("%w: content verification evidence is incomplete", ErrInvalid)
		}
	}
	seenParts := map[string]bool{}
	var total int64
	for index, part := range m.Parts {
		if !containsPart(requiredParts, part.Name) || seenParts[part.Name] || part.Bytes < 1 || part.Bytes > MaxArchiveBytes || !digestPattern.MatchString(part.SHA256) {
			return fmt.Errorf("%w: invalid archive part", ErrInvalid)
		}
		if part.Name != requiredParts[index] {
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

func validLSN(value string) bool {
	_, valid := lsnValue(value)
	return valid
}

func lsnValue(value string) (uint64, bool) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(parts[0]) > 8 || len(parts[1]) > 8 {
		return 0, false
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' && c < 'A' || c > 'F' && c < 'a' || c > 'f' {
				return 0, false
			}
		}
	}
	high, highErr := strconv.ParseUint(parts[0], 16, 32)
	low, lowErr := strconv.ParseUint(parts[1], 16, 32)
	return high<<32 | low, highErr == nil && lowErr == nil
}

func requiredPart(name string) bool {
	return containsPart(RequiredParts, name) || containsPart(NeonRequiredParts, name)
}

func containsPart(parts []string, name string) bool {
	for _, required := range parts {
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
