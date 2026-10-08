// Package sandbox defines owner-scoped, persistent execution sessions.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
)

const (
	Starting       = "starting"
	Ready          = "ready"
	Closing        = "closing"
	Closed         = "closed"
	MaxSessions    = spec.MaxSandboxSessions
	MaxHistory     = 1000
	MaxInputBytes  = 48 << 20
	MaxOutputBytes = 52 << 20
	CallTimeout    = 150 * time.Second
	Retention      = 24 * time.Hour
)

// Record excludes authority, frozen source, leases and Kubernetes identifiers.
type Record struct {
	ID                                                  string           `json:"id"`
	ApplicationID                                       string           `json:"application_id"`
	Service                                             string           `json:"service"`
	Revision                                            int64            `json:"revision"`
	Image                                               string           `json:"image"`
	Generation                                          string           `json:"generation"`
	Status                                              string           `json:"status"`
	Message                                             string           `json:"message"`
	CleanupPending                                      bool             `json:"cleanup_pending"`
	CreatedAt                                           time.Time        `json:"created_at"`
	ExpiresAt                                           time.Time        `json:"expires_at"`
	IdleUntil                                           time.Time        `json:"idle_until"`
	ClosedAt                                            *time.Time       `json:"closed_at,omitempty"`
	IdentityID, KeyID, OwnerHash, RuntimeHash           string           `json:"-"`
	Project, Environment, ApplicationName, DeploymentID string           `json:"-"`
	NamespaceUID, PodUID, ContainerID, ImageID          string           `json:"-"`
	CallToken, CallRequestID, CallKeyID                 string           `json:"-"`
	CallUntil                                           time.Time        `json:"-"`
	LeaseToken                                          string           `json:"-"`
	LeaseUntil                                          time.Time        `json:"-"`
	Source                                              spec.Application `json:"-"`
}

type CreateRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	ExpectedImage    string `json:"expected_image"`
	RuntimeKey       string `json:"runtime_key"`
}

type RuntimeState struct {
	NamespaceUID, PodUID, ContainerID, ImageID string
	Ready                                      bool
}

// Runtime accepts only immutable server-owned templates. Calls never select a command.
type Runtime interface {
	StartSession(context.Context, Record, func(context.Context) error) (RuntimeState, error)
	ObserveSession(context.Context, Record) (RuntimeState, error)
	CallSession(context.Context, Record, io.Reader, io.Writer) error
	CleanupSession(context.Context, Record) (bool, error)
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var imagePattern = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

func HashKey(value string) (string, error) {
	if !keyPattern.MatchString(value) {
		return "", fmt.Errorf("identifier must contain 1-128 ASCII letters, digits, periods, underscores, colons or hyphens and start with a letter or digit")
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]), nil
}
func ValidID(value string) bool { return idPattern.MatchString(value) }
func ValidImage(value string) bool {
	return len(value) <= 512 && !strings.ContainsAny(value, " \t\n\r\x00\\?#") && !strings.Contains(value, "://") && !strings.HasPrefix(value, "/") && !strings.Contains(value, "..") && strings.Count(value, "@") == 1 && strings.IndexByte(value, '@') > 0 && imagePattern.MatchString(value)
}
func AllowsIdentity(template *spec.SandboxSession, identity string) bool {
	if template == nil {
		return false
	}
	for _, allowed := range template.AllowedIdentities {
		if allowed == identity {
			return true
		}
	}
	return false
}
