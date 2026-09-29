package actions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type runnerGroup struct {
	id       int64
	page     int
	until    time.Time
	inflight bool
}

// Resolve groups before persisting a registration intent. Pagination survives
// local request deferrals; the eventual JIT POST reserves its own credit.
func (b *RequestBudget) defaultGroup(ctx context.Context, c *Client, organization string) (int64, error) {
	key := inventoryKey{credential: sha256.Sum256([]byte(c.token)), scope: organization}
	b.inventoryMu.Lock()
	now := b.time()
	if b.groups == nil {
		b.groups = map[inventoryKey]*runnerGroup{}
	}
	for key, group := range b.groups {
		if !group.inflight && !group.until.After(now) {
			delete(b.groups, key)
		}
	}
	group := b.groups[key]
	if group != nil && group.id > 0 {
		id := group.id
		b.inventoryMu.Unlock()
		return id, nil
	}
	if group == nil {
		if len(b.groups) >= maxInventoryTargets {
			b.inventoryMu.Unlock()
			return 0, &RetryError{At: now.Add(time.Minute), Local: true}
		}
		group = &runnerGroup{page: 1, until: now.Add(5 * time.Minute)}
		b.groups[key] = group
	}
	if group.inflight {
		b.inventoryMu.Unlock()
		return 0, &RetryError{At: now.Add(time.Second), Local: true}
	}
	group.inflight = true
	page := group.page
	b.inventoryMu.Unlock()
	var result struct {
		Total  int `json:"total_count"`
		Groups []struct {
			ID      int64 `json:"id"`
			Default bool  `json:"default"`
		} `json:"runner_groups"`
	}
	err := c.request(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s/actions/runner-groups?per_page=100&page=%d", url.PathEscape(organization), page), nil, &result)
	b.inventoryMu.Lock()
	defer b.inventoryMu.Unlock()
	group.inflight = false
	if err != nil {
		return 0, err
	}
	if result.Total > 1000 || len(result.Groups) > 100 {
		delete(b.groups, key)
		return 0, errors.New("runner group inventory exceeds the supported bound; select a runner group ID")
	}
	for _, item := range result.Groups {
		if item.Default && item.ID > 0 {
			group.id = item.ID
			group.until = b.time().Add(5 * time.Minute)
			return group.id, nil
		}
	}
	if len(result.Groups) < 100 || page >= 10 {
		delete(b.groups, key)
		return 0, errors.New("GitHub default runner group was not found; select a runner group ID")
	}
	group.page++
	return 0, &RetryError{At: b.time().Add(time.Second), Local: true}
}
