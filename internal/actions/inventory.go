package actions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	maxInventoryTargets = 128
	maxInventoryRecords = 10000
	inventoryTTL        = 30 * time.Second
	inventoryScanLimit  = 45 * time.Second
)

type inventoryKey struct {
	credential [32]byte
	scope      string
}
type runnerInventory struct {
	runners     map[int64]Runner
	pending     map[int64]Runner
	observed    time.Time
	started     time.Time
	nextPage    int
	inflight    bool
	directUntil time.Time
}

// pruneInventories only evicts expired, inactive data. Running requests retain
// their entry until they finish, and both partial and complete records count
// toward the shared memory bound.
func (b *RequestBudget) pruneInventories(now time.Time) {
	for key, entry := range b.inventories {
		if entry.inflight {
			continue
		}
		if entry.pending != nil && now.Sub(entry.started) > inventoryScanLimit {
			b.inventoryRecords -= len(entry.pending)
			entry.pending = nil
			entry.directUntil = now.Add(inventoryTTL)
		}
		if entry.runners != nil && now.Sub(entry.observed) >= inventoryTTL {
			b.inventoryRecords -= len(entry.runners)
			entry.runners = nil
		}
		if entry.pending == nil && entry.runners == nil && !entry.directUntil.After(now) {
			delete(b.inventories, key)
		}
	}
}

// inventoryRunner publishes only complete snapshots. Each reconciliation does
// at most one 100-runner page; interrupted scans resume at the next page under
// the same scoped budget. An absent runner still needs a fresh identity lookup.
func (b *RequestBudget) inventoryRunner(ctx context.Context, c *Client, target Target, id int64, name string) (Runner, error) {
	fresh := func() (Runner, error) {
		if name != "" {
			return c.GetKnownFresh(ctx, target, id, name)
		}
		return c.GetFresh(ctx, target, id)
	}
	base, err := runnerBase(target)
	if err != nil || id <= 0 {
		return Runner{}, errors.New("invalid runner identity or scope")
	}
	key := inventoryKey{credential: sha256.Sum256([]byte(c.token)), scope: base}
	b.inventoryMu.Lock()
	now := b.time()
	if b.inventories == nil {
		b.inventories = map[inventoryKey]*runnerInventory{}
	}
	b.pruneInventories(now)
	entry := b.inventories[key]
	if entry != nil && entry.directUntil.After(now) {
		b.inventoryMu.Unlock()
		return fresh()
	}
	if entry != nil && entry.runners != nil && now.Sub(entry.observed) < inventoryTTL {
		runner, found := entry.runners[id]
		b.inventoryMu.Unlock()
		if found {
			return runner, nil
		}
		return fresh()
	}
	if entry == nil {
		if len(b.inventories) >= maxInventoryTargets {
			b.inventoryMu.Unlock()
			return fresh()
		}
		entry = &runnerInventory{pending: map[int64]Runner{}, started: now, nextPage: 1}
		b.inventories[key] = entry
	}
	if entry.inflight {
		b.inventoryMu.Unlock()
		return Runner{}, &RetryError{At: now.Add(time.Second), Local: true}
	}
	if entry.pending == nil {
		entry.pending = map[int64]Runner{}
		entry.started = now
		entry.nextPage = 1
	}
	page := entry.nextPage
	entry.inflight = true
	b.inventoryMu.Unlock()
	var result struct {
		Runners []Runner `json:"runners"`
		Total   int      `json:"total_count"`
	}
	err = c.request(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", base, page), nil, &result)
	b.inventoryMu.Lock()
	entry.inflight = false
	now = b.time()
	if err != nil {
		b.inventoryMu.Unlock()
		var status *StatusError
		if errors.As(err, &status) && status.Status == 404 {
			return Runner{}, &StatusError{Status: 404, Scope: true}
		}
		return Runner{}, err
	}
	if now.Sub(entry.started) > inventoryScanLimit {
		b.inventoryRecords -= len(entry.pending)
		entry.pending = nil
		entry.directUntil = now.Add(inventoryTTL)
		b.inventoryMu.Unlock()
		return fresh()
	}
	if result.Total > maxInventoryRecords || page > maxInventoryRecords/100 {
		b.inventoryRecords -= len(entry.pending)
		entry.pending = nil
		entry.directUntil = now.Add(inventoryTTL)
		b.inventoryMu.Unlock()
		return fresh()
	}
	if len(result.Runners) > 100 || result.Total < 0 || result.Total < len(result.Runners) {
		b.inventoryRecords -= len(entry.pending)
		delete(b.inventories, key)
		b.inventoryMu.Unlock()
		return Runner{}, errors.New("runner inventory exceeds its bounded response or fleet capacity")
	}
	for _, runner := range result.Runners {
		if runner.ID <= 0 || len(runner.Name) > 256 || len(runner.Status) > 32 {
			b.inventoryRecords -= len(entry.pending)
			delete(b.inventories, key)
			b.inventoryMu.Unlock()
			return Runner{}, errors.New("GitHub returned an invalid runner inventory")
		}
		if _, exists := entry.pending[runner.ID]; !exists {
			if b.inventoryRecords >= maxInventoryRecords {
				b.inventoryRecords -= len(entry.pending)
				entry.pending = nil
				entry.directUntil = now.Add(inventoryTTL)
				b.inventoryMu.Unlock()
				return fresh()
			}
			b.inventoryRecords++
		}
		runner.ObservedAt = now
		entry.pending[runner.ID] = runner
	}
	if len(result.Runners) < 100 || page*100 >= result.Total {
		entry.runners = entry.pending
		entry.pending = nil
		entry.observed = now
		runner, found := entry.runners[id]
		b.inventoryMu.Unlock()
		if found {
			return runner, nil
		}
		return fresh()
	}
	entry.nextPage++
	b.inventoryMu.Unlock()
	return Runner{}, &RetryError{At: now.Add(time.Second), Local: true}
}
