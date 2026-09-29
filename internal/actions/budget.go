package actions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	credentialBurst       = 10
	credentialRate        = 1.0 // Leave room below GitHub's usual hourly quota.
	credentialConcurrency = 2
	maxCredentialBudgets  = 4096
)

// RetryError carries a public retry deadline without retaining a credential or
// provider response body. Controllers persist the deadline; requests never sleep
// while occupying a reconciliation worker or dashboard connection.
type RetryError struct {
	At       time.Time
	Status   int
	Progress bool
	Local    bool
}

type requestProgressKey struct{}

// TrackRequests distinguishes work which used provider capacity from a locally
// deferred pool. The scheduler uses this to rotate a shared credential fairly.
func TrackRequests(ctx context.Context) (context.Context, func() bool) {
	progress := &atomic.Bool{}
	return context.WithValue(ctx, requestProgressKey{}, progress), progress.Load
}

func markRequest(ctx context.Context) {
	if progress, ok := ctx.Value(requestProgressKey{}).(*atomic.Bool); ok {
		progress.Store(true)
	}
}

func (e *RetryError) Unwrap() error {
	if e.Status != 0 {
		return &StatusError{Status: e.Status}
	}
	return nil
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("GitHub requests are paused until %s", e.At.UTC().Format(time.RFC3339))
}

type credentialBudget struct {
	tokens   float64
	updated  time.Time
	used     time.Time
	blocked  time.Time
	inflight int
	failures int
	status   int
}

// RequestBudget is shared by runner management and workflow readers in one
// control plane. It stores only credential hashes, has bounded state, and fails
// closed when every entry is active. Its zero value is ready for use.
type RequestBudget struct {
	mu               sync.Mutex
	entries          map[[32]byte]*credentialBudget
	now              func() time.Time
	inventoryMu      sync.Mutex
	inventories      map[inventoryKey]*runnerInventory
	inventoryRecords int
	groups           map[inventoryKey]*runnerGroup
}

// NewRequestBudget accepts a trusted clock for deterministic qualification.
// Production callers can use the zero value and the system clock.
func NewRequestBudget(now func() time.Time) RequestBudget { return RequestBudget{now: now} }

func (b *RequestBudget) time() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *RequestBudget) acquire(key [32]byte) (*credentialBudget, error) { return b.reserve(key, 1) }

func (b *RequestBudget) reserve(key [32]byte, cost int) (*credentialBudget, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.time()
	if b.entries == nil {
		b.entries = make(map[[32]byte]*credentialBudget)
	}
	v := b.entries[key]
	if v == nil {
		if len(b.entries) >= maxCredentialBudgets {
			for k, candidate := range b.entries {
				if candidate.inflight == 0 && !candidate.blocked.After(now) && now.Sub(candidate.used) >= time.Hour {
					delete(b.entries, k)
				}
			}
			if len(b.entries) >= maxCredentialBudgets {
				return nil, &RetryError{At: now.Add(time.Minute), Local: true}
			}
		}
		v = &credentialBudget{tokens: credentialBurst, updated: now}
		b.entries[key] = v
	}
	v.used = now
	v.tokens = math.Min(credentialBurst, v.tokens+math.Max(0, now.Sub(v.updated).Seconds())*credentialRate)
	v.updated = now
	if v.blocked.After(now) {
		return nil, &RetryError{At: v.blocked, Status: v.status}
	}
	if v.inflight >= credentialConcurrency {
		return nil, &RetryError{At: now.Add(time.Second), Local: true}
	}
	if v.tokens < float64(cost) {
		return nil, &RetryError{At: now.Add(time.Duration(math.Ceil((float64(cost) - v.tokens) / credentialRate * float64(time.Second)))), Local: true}
	}
	v.tokens -= float64(cost)
	v.inflight++
	return v, nil
}

func retryHeader(header http.Header, now time.Time) time.Time {
	until := time.Time{}
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 64); err == nil && seconds >= 0 {
		until = now.Add(time.Duration(min(seconds, 86400)) * time.Second)
	} else if date, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		until = date
	}
	if header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > now.Unix() {
			date := time.Unix(reset, 0).Add(time.Second)
			if date.After(until) {
				until = date
			}
		}
	}
	if until.After(now.Add(24 * time.Hour)) {
		return now.Add(24 * time.Hour)
	}
	return until
}

func (b *RequestBudget) observe(v *credentialBudget, status int, header http.Header) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.time()
	until := retryHeader(header, now)
	if status == 403 || status == 429 || status >= 500 || status == 0 {
		v.failures = min(v.failures+1, 10)
		base := time.Second
		if status == 403 || status == 429 {
			base = time.Minute
		}
		backoff := min(base*time.Duration(1<<uint(v.failures-1)), 15*time.Minute)
		if candidate := now.Add(backoff); candidate.After(until) {
			until = candidate
		}
	} else if status >= 200 && status < 400 {
		v.failures = 0
	}
	if until.After(v.blocked) {
		v.blocked = until
		v.status = status
	}
}

func (b *RequestBudget) release(v *credentialBudget) {
	b.mu.Lock()
	v.inflight--
	b.mu.Unlock()
}

type budgetBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *budgetBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func (c *Client) doAPI(req *http.Request) (*http.Response, error) {
	if c.budget == nil {
		markRequest(req.Context())
		return c.http.Do(req)
	}
	var v *credentialBudget
	var err error
	reservation := c.registration
	if reservation == nil {
		reservation = c.cleanup
	}
	if reservation == nil {
		reservation = c.workflow
	}
	if reservation == nil {
		reservation = c.lookup
	}
	reserved := reservation != nil && reservation.remaining > 0
	if reserved {
		v = reservation.state
		c.budget.mu.Lock()
		blocked := v.blocked.After(c.budget.time())
		deadline, status := v.blocked, v.status
		c.budget.mu.Unlock()
		if blocked {
			return nil, &RetryError{At: deadline, Status: status}
		}
		reservation.remaining--
	} else {
		v, err = c.budget.acquire(sha256.Sum256([]byte(c.token)))
	}
	if err != nil {
		return nil, err
	}
	markRequest(req.Context())
	res, err := c.http.Do(req)
	if err != nil {
		if req.Context().Err() == nil {
			c.budget.observe(v, 0, nil)
		}
		if !reserved {
			c.budget.release(v)
		}
		if req.Context().Err() == nil {
			return nil, c.retryError(0)
		}
		return nil, err
	}
	c.budget.observe(v, res.StatusCode, res.Header)
	if !reserved {
		res.Body = &budgetBody{ReadCloser: res.Body, release: func() { c.budget.release(v) }}
	}
	return res, nil
}

// retryError lets the caller schedule a provider rejection using the same
// deadline all other calls with this credential now observe.
func (c *Client) retryError(status int) error {
	if c.budget != nil {
		c.budget.mu.Lock()
		defer c.budget.mu.Unlock()
		if v := c.budget.entries[sha256.Sum256([]byte(c.token))]; v != nil && v.blocked.After(c.budget.time()) {
			return &RetryError{At: v.blocked, Status: status}
		}
	}
	return &StatusError{Status: status}
}

type registrationReservation struct {
	state     *credentialBudget
	remaining int
	target    Target
	group     int64
	release   func()
}

// PrepareRegistration reserves bounded request credits before the controller
// records an intent. A known local throttle must not manufacture ambiguous
// registrations. The permit is private to this short-lived client.
func (c *Client) PrepareRegistration(ctx context.Context, target Target) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !target.Valid() {
		return nil, fmt.Errorf("invalid runner registration scope")
	}
	if c.budget == nil {
		return func() {}, nil
	}
	if c.registration != nil || c.cleanup != nil || c.workflow != nil || c.lookup != nil {
		return nil, fmt.Errorf("runner registration already has a request reservation")
	}
	group := int64(1)
	if target.Organization != "" && target.RunnerGroupID == 0 {
		var err error
		group, err = c.defaultRunnerGroup(ctx, target.Organization)
		if err != nil {
			return nil, err
		}
	} else if target.Organization != "" {
		group = target.RunnerGroupID
	}
	reservation, err := c.reserveRequests(ctx, 1)
	if err != nil {
		return nil, err
	}
	reservation.target = target
	reservation.group = group
	c.registration = reservation
	return reservation.release, nil
}

func (c *Client) reserveRequests(ctx context.Context, cost int) (*registrationReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.registration != nil || c.cleanup != nil || c.workflow != nil || c.lookup != nil {
		return nil, errors.New("runner client already has a request reservation")
	}
	state, err := c.budget.reserve(sha256.Sum256([]byte(c.token)), cost)
	if err != nil {
		return nil, err
	}
	reservation := &registrationReservation{state: state, remaining: cost}
	var once sync.Once
	reservation.release = func() {
		once.Do(func() {
			c.budget.mu.Lock()
			state.tokens = math.Min(credentialBurst, state.tokens+float64(reservation.remaining))
			state.inflight--
			c.budget.mu.Unlock()
			if c.registration == reservation {
				c.registration = nil
			}
			if c.cleanup == reservation {
				c.cleanup = nil
			}
			if c.workflow == reservation {
				c.workflow = nil
			}
			if c.lookup == reservation {
				c.lookup = nil
			}
		})
	}
	return reservation, nil
}

// PrepareCleanup preserves enough credits to check busy state and delete the
// same runner. Otherwise repeated single-credit retries could never delete it.
func (c *Client) PrepareCleanup(ctx context.Context, target Target, id int64) (func(), error) {
	if !target.Valid() || id <= 0 {
		return nil, errors.New("invalid runner cleanup identity")
	}
	if c.budget == nil {
		return func() {}, nil
	}
	reservation, err := c.reserveRequests(ctx, 2)
	if err != nil {
		return nil, err
	}
	c.cleanup = reservation
	return reservation.release, nil
}

// PrepareWorkflowLogs reserves verification and the logs redirect together.
// Initial discovery remains bounded to five job pages; known IDs use one GET.
func (c *Client) PrepareWorkflowLogs(ctx context.Context, knownJob bool) (func(), error) {
	if c.budget == nil {
		return func() {}, nil
	}
	cost := 6
	if knownJob {
		cost = 2
	}
	reservation, err := c.reserveRequests(ctx, cost)
	if err != nil {
		return nil, err
	}
	c.workflow = reservation
	return reservation.release, nil
}
