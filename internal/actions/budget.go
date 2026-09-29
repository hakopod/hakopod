package actions

import (
	"context"
	"crypto/sha256"
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
	mu      sync.Mutex
	entries map[[32]byte]*credentialBudget
	now     func() time.Time
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
				return nil, &RetryError{At: now.Add(time.Minute)}
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
		return nil, &RetryError{At: now.Add(time.Second)}
	}
	if v.tokens < float64(cost) {
		return nil, &RetryError{At: now.Add(time.Duration(math.Ceil((float64(cost) - v.tokens) / credentialRate * float64(time.Second))))}
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
	reserved := c.registration != nil && c.registration.remaining > 0
	if reserved {
		v = c.registration.state
		c.budget.mu.Lock()
		blocked := v.blocked.After(c.budget.time())
		deadline, status := v.blocked, v.status
		c.budget.mu.Unlock()
		if blocked {
			return nil, &RetryError{At: deadline, Status: status}
		}
		c.registration.remaining--
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
	if c.registration != nil {
		return nil, fmt.Errorf("runner registration already has a request reservation")
	}
	cost := 1
	if target.Organization != "" && target.RunnerGroupID == 0 {
		cost = 2
	}
	state, err := c.budget.reserve(sha256.Sum256([]byte(c.token)), cost)
	if err != nil {
		return nil, err
	}
	reservation := &registrationReservation{state: state, remaining: cost}
	c.registration = reservation
	var once sync.Once
	return func() {
		once.Do(func() {
			c.budget.mu.Lock()
			state.tokens = math.Min(credentialBurst, state.tokens+float64(reservation.remaining))
			state.inflight--
			c.budget.mu.Unlock()
			c.registration = nil
		})
	}, nil
}
