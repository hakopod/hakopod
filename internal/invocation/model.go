// Package invocation defines bounded, predefined job execution contracts.
package invocation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

const InputPath = "/run/hakopod/invocation/input.json"
const MaxInputBytes = 128 << 10
const MaxLogBytes = 64 << 10
const MaxOwnerPending = 4
const Retention = 7 * 24 * time.Hour

const (
	Queued    = "queued"
	Starting  = "starting"
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// Record never exposes input, credentials, lease tokens or the frozen source.
type Record struct {
	Image                                   string           `json:"image"`
	CorrelationID                           string           `json:"correlation_id"`
	ID                                      string           `json:"id"`
	ApplicationID                           string           `json:"application_id"`
	Service                                 string           `json:"service"`
	Revision                                int64            `json:"revision"`
	Status                                  string           `json:"status"`
	Message                                 string           `json:"message"`
	CancelRequested                         bool             `json:"cancel_requested"`
	CleanupPending                          bool             `json:"cleanup_pending"`
	InputBytes                              int              `json:"input_bytes"`
	LogTruncated                            bool             `json:"log_truncated"`
	ExitCode                                *int32           `json:"exit_code,omitempty"`
	CreatedAt                               time.Time        `json:"created_at"`
	StartedAt                               *time.Time       `json:"started_at,omitempty"`
	FinishedAt                              *time.Time       `json:"finished_at,omitempty"`
	ExpiresAt                               time.Time        `json:"expires_at"`
	Project, Environment, ApplicationName   string           `json:"-"`
	IdentityID, KeyID, OwnerHash, InputHash string           `json:"-"`
	DeploymentID, NamespaceUID, RuntimeUID  string           `json:"-"`
	LeaseToken                              string           `json:"-"`
	LeaseUntil                              time.Time        `json:"-"`
	EncryptedInput, EncryptedLogs           []byte           `json:"-"`
	Source                                  spec.Application `json:"-"`
}

func (r Record) Terminal() bool {
	return r.Status == Succeeded || r.Status == Failed || r.Status == Cancelled
}

// RuntimeState contains only bounded metadata and a bounded log snapshot.
type RuntimeState struct {
	NamespaceUID, RuntimeUID, Status, Message string
	ExitCode                                  *int32
	Log                                       []byte
	LogTruncated                              bool
}

type CreateRequest struct {
	ExpectedImage    string                     `json:"expected_image"`
	CorrelationID    string                     `json:"correlation_id"`
	ExpectedRevision int64                      `json:"expected_revision"`
	OwnerScope       string                     `json:"owner_scope"`
	Inputs           map[string]json.RawMessage `json:"inputs"`
}

func OwnerHash(scope string) (string, error) {
	if len(scope) < 1 || len(scope) > 128 || !regexp.MustCompile(`^[A-Za-z0-9._:-]+$`).MatchString(scope) {
		return "", fmt.Errorf("owner_scope must contain 1–128 ASCII letters, digits, periods, underscores, colons or hyphens")
	}
	value := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(value[:]), nil
}

var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func ValidateCorrelationID(value string) error {
	if len(value) < 1 || len(value) > 160 || !correlationPattern.MatchString(value) {
		return fmt.Errorf("correlation_id must contain 1–160 ASCII letters, digits, periods, underscores, colons or hyphens and start with a letter or digit")
	}
	return nil
}
