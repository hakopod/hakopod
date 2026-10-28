// Package advisory matches running applications against published security
// advisories. Two sources feed it: the Hakopod-signed advisory feed and, where
// it covers a runtime, OSV.dev. Matching only reads evidence Hakopod already
// holds (resolved image digests and image configuration environment); it never
// executes anything inside a workload.
package advisory

import "time"

// FeedFormat is the only signed feed format this build accepts.
const FeedFormat = 1

// SignatureDomain prefixes the signed payload so the key cannot be replayed
// against another Hakopod artifact.
const SignatureDomain = "hakopod-security-advisories-v1\n"

// SignedFeed is the published file: payload is the exact JSON text that was
// signed, signature is base64 Ed25519 over SignatureDomain+payload.
type SignedFeed struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// Feed is the verified payload.
type Feed struct {
	Format     int        `json:"format"`
	Revision   int64      `json:"revision"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Advisories []Advisory `json:"advisories"`
}

type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Moderate Severity = "moderate"
	Low      Severity = "low"
)

// Action says what Hakopod may offer. Advisories never carry commands.
type Action string

const (
	// ActionUpdate re-resolves mutable image tags (image services) or rebuilds
	// the same commit (source-built services).
	ActionUpdate Action = "update"
	// ActionManual means the operator has to change configuration.
	ActionManual Action = "manual"
)

// Source records where an advisory came from.
type Source string

const (
	SourceHakopod Source = "hakopod"
	SourceOSV     Source = "osv"
)

type Advisory struct {
	ID          string    `json:"id"`
	Revision    int64     `json:"revision"`
	Source      Source    `json:"source"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	Severity    Severity  `json:"severity"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Action      Action    `json:"action"`
	// Aliases are other identifiers for the same issue (CVE ids). A Hakopod
	// advisory and an OSV advisory sharing an alias produce one finding.
	Aliases []string `json:"aliases,omitempty"`
	Targets []Target `json:"targets"`
}

// Target is exactly one of an image or a runtime target.
type Target struct {
	Image   *ImageTarget   `json:"image,omitempty"`
	Runtime *RuntimeTarget `json:"runtime,omitempty"`
}

// ImageTarget names an exact, normalised repository (docker.io/library/node),
// never a pattern. A tag match is only ever "possible"; tags are not versions.
type ImageTarget struct {
	Repository      string   `json:"repository"`
	AffectedDigests []string `json:"affected_digests,omitempty"`
	AffectedTags    []string `json:"affected_tags,omitempty"`
	FixedDigests    []string `json:"fixed_digests,omitempty"`
}

// RuntimeTarget uses the OSV range model: a version is affected when it is
// >= some Introduced and < the Fixed of the same range, or <= LastAffected
// when the range is closed that way. Neither set means unfixed.
type RuntimeTarget struct {
	Runtime string  `json:"runtime"`
	Ranges  []Range `json:"ranges"`
}

type Range struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
}

// Runtimes this build can read from image configuration. Keys are runtime
// names used in feeds; values are the image environment variable.
var RuntimeEnv = map[string]string{
	"node":   "NODE_VERSION",
	"python": "PYTHON_VERSION",
	"go":     "GOLANG_VERSION",
	"php":    "PHP_VERSION",
	"ruby":   "RUBY_VERSION",
	"java":   "JAVA_VERSION",
}

// ServiceKind decides which fix applies.
type ServiceKind string

const (
	KindImage  ServiceKind = "image"  // spec image, re-resolve tag
	KindSource ServiceKind = "source" // built by a Hakopod build config
)

// ServiceEvidence is what Hakopod knows about one service of the running
// revision. Runtimes is empty and Unknown is true when the image config
// carried no recognised version variable.
type ServiceEvidence struct {
	Service    string            `json:"service"`
	Kind       ServiceKind       `json:"kind"`
	Image      string            `json:"image"`   // as configured (tag form)
	Digest     string            `json:"digest"`  // resolved sha256
	Mutable    bool              `json:"mutable"` // configured without a digest
	Runtimes   map[string]string `json:"runtimes,omitempty"`
	Unknown    bool              `json:"unknown"`
	BaseImage  string            `json:"base_image,omitempty"`  // source builds: FROM line
	BasePinned bool              `json:"base_pinned,omitempty"` // FROM carries a digest (Hakopod-generated Dockerfiles)
	// Reason says why Unknown is true (for example the registry could not be
	// reached). It is shown to operators and never contains credentials.
	Reason string `json:"reason,omitempty"`
}

// Inventory is cached per application and running deployment.
type Inventory struct {
	ApplicationID string            `json:"application_id"`
	DeploymentID  string            `json:"deployment_id"`
	Revision      int64             `json:"revision"`
	Services      []ServiceEvidence `json:"services"`
	// Incomplete lists checks that did not finish, such as truncated OSV
	// results. An incomplete inventory is never presented as clean.
	Incomplete []string  `json:"incomplete,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

type Confidence string

const (
	Confirmed Confidence = "confirmed"
	Possible  Confidence = "possible"
)

// FixKind is what the one-click update would do for this finding.
type FixKind string

const (
	FixReresolve FixKind = "reresolve" // new revision, image tag resolved again
	FixRebuild   FixKind = "rebuild"   // same commit rebuilt, base tag pulled again
	FixManual    FixKind = "manual"    // explained by FixReason
)

type Finding struct {
	AdvisoryID       string     `json:"advisory_id"`
	AdvisoryRevision int64      `json:"advisory_revision"`
	Source           Source     `json:"source"`
	Severity         Severity   `json:"severity"`
	Title            string     `json:"title"`
	URL              string     `json:"url"`
	Service          string     `json:"service"`
	Confidence       Confidence `json:"confidence"`
	Evidence         string     `json:"evidence"` // e.g. "node 22.3.0", "docker.io/library/node@sha256:..."
	Fix              FixKind    `json:"fix"`
	FixReason        string     `json:"fix_reason,omitempty"`
	// AutoEligible: confirmed digest match on an image service with fixed
	// digests listed. Only the signed feed can make this true.
	AutoEligible bool     `json:"auto_eligible"`
	FixedDigests []string `json:"fixed_digests,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
}
