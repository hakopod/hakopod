package database

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Oracle Database Free is proprietary, free-to-use software. It is not the
// Enterprise edition and does not provide the Data Guard deployment contract.
// Use the full image: Free Lite omits RMAN, Data Pump and wallet tooling.
const OracleFreeImage = "container-registry.oracle.com/database/free:23.26.3.0@sha256:f988b0c04c4c386cd306a2a914c0d7a9702d83acc31b064a28ad8eb6278a8fba"

// Reserve space for Oracle's system tablespaces, redo, undo and temporary data.
// Keep APP below Free's user-data limit even when a larger volume is allocated.
func OracleFreeQuotaGiB(storageGiB int64) int64 {
	return max(0, min(10, storageGiB-8))
}

// OracleConfig describes edition and image entitlement independently of runtime
// availability. A declaration does not establish native acceptance or licensing
// compliance. The registry reference is resolved in the resource's own scope.
type OracleConfig struct {
	Edition            string `json:"edition" toml:"edition"`
	Image              string `json:"image,omitempty" toml:"image"`
	RegistryCredential string `json:"registry_credential,omitempty" toml:"registry_credential"`
	LicenseConfirmed   bool   `json:"license_confirmed,omitempty" toml:"license_confirmed"`
}

// OracleSwitchoverReview binds a one-time, graceful Data Guard role change to
// the exact healthy topology that the user reviewed. The durable API operation
// stores this value and supplies its lease check before each cluster mutation.
type OracleSwitchoverReview struct {
	RequestID           string    `json:"request_id"`
	DatabaseID          string    `json:"database_id"`
	Project             string    `json:"project"`
	Environment         string    `json:"environment"`
	Revision            int64     `json:"revision"`
	BrokerUID           string    `json:"broker_uid"`
	TopologyFingerprint string    `json:"topology_fingerprint"`
	Primary             string    `json:"primary"`
	Target              string    `json:"target"`
	TargetController    string    `json:"target_controller"`
	TargetUniqueName    string    `json:"target_unique_name"`
	MemberUIDs          []string  `json:"member_uids"`
	ExpiresAt           time.Time `json:"expires_at"`
}

var ErrOracleSwitchoverFailed = errors.New("Oracle switchover failed; routing remains closed for recovery review")
var ErrOracleSwitchoverReviewChanged = errors.New("Oracle switchover review changed before submission")

func (o OracleConfig) Validate(version, mode string) error {
	if mode != "standalone" && mode != "cluster" {
		return fmt.Errorf("Oracle deployment mode must be standalone or cluster")
	}
	switch o.Edition {
	case "free":
		if version != "23.26" || mode != "standalone" {
			return fmt.Errorf("Oracle Database Free requires version 23.26 and standalone mode; Data Guard requires Enterprise edition")
		}
		if o.Image != "" || o.RegistryCredential != "" || o.LicenseConfirmed {
			return fmt.Errorf("Oracle Database Free uses the platform's pinned image; customer images and license declarations belong to Enterprise edition")
		}
	case "enterprise":
		if version != "19" && version != "23.26" {
			return fmt.Errorf("Oracle Enterprise image version must be 19 or 23.26")
		}
		if !o.LicenseConfirmed {
			return fmt.Errorf("confirm that your Oracle license covers this deployment and its selected options")
		}
		if err := validateOracleImage(o.Image); err != nil {
			return err
		}
		if o.RegistryCredential != "" && (!namePattern.MatchString(o.RegistryCredential) || len(o.RegistryCredential) > 40) {
			return fmt.Errorf("Oracle registry credentials must use a project and environment scoped reference")
		}
	default:
		return fmt.Errorf("Oracle edition must be free or enterprise")
	}
	return nil
}

func (o OracleConfig) ContainerImage() string {
	if o.Edition == "free" {
		return OracleFreeImage
	}
	return o.Image
}

var oracleImagePart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var oracleImageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var oracleImageDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validateOracleImage(image string) error {
	invalid := fmt.Errorf("Oracle Enterprise requires an explicit registry and an immutable OCI image with a sha256 digest; credentials must use a separate scoped reference")
	if len(image) > 512 || strings.Count(image, "@") != 1 || strings.ContainsAny(image, " \t\r\n\x00\\?#") || strings.Contains(image, "://") {
		return invalid
	}
	name, digest, ok := strings.Cut(image, "@sha256:")
	if !ok || !oracleImageDigest.MatchString(digest) {
		return invalid
	}
	if last := strings.LastIndex(name, ":"); last > strings.LastIndex(name, "/") {
		if !oracleImageTag.MatchString(name[last+1:]) {
			return invalid
		}
		name = name[:last]
	}
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return invalid
	}
	host := parts[0]
	if at := strings.IndexByte(host, ':'); at >= 0 {
		port, err := strconv.Atoi(host[at+1:])
		if err != nil || port < 1 || port > 65535 {
			return invalid
		}
		host = host[:at]
	}
	if len(validation.IsDNS1123Subdomain(host)) != 0 || (!strings.Contains(parts[0], ".") && !strings.Contains(parts[0], ":") && host != "localhost") {
		return invalid
	}
	for _, part := range parts[1:] {
		if !oracleImagePart.MatchString(part) {
			return invalid
		}
	}
	return nil
}
