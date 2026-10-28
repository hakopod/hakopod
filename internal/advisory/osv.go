package advisory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrOSVTruncated is returned (wrapped) with partial results when a page or
// hydration cap was hit.
var ErrOSVTruncated = errors.New("osv: results truncated")

const (
	osvMaxQueries  = 32
	osvMaxVulns    = 200
	osvMaxPages    = 3
	osvMaxBody     = 1 << 20
	osvMessageMax  = 600
	osvDefaultBase = "https://api.osv.dev"
)

// osvCoverage lists runtimes OSV answers by runtime version with SEMVER
// ranges (checked live 2026-10-25). Bitnami's vulndb tracks upstream
// runtime releases; Go's own database tracks the toolchain as "stdlib".
var osvCoverage = map[string][2]string{
	"go":     {"Go", "stdlib"},
	"node":   {"Bitnami", "node"},
	"python": {"Bitnami", "python"},
	"php":    {"Bitnami", "php"},
	"ruby":   {"Bitnami", "ruby"},
	"java":   {"Bitnami", "java"},
}

// OSV queries api.osv.dev. Client must carry a timeout; NewOSV sets one.
type OSV struct {
	Client  *http.Client
	BaseURL string
}

type RuntimeQuery struct {
	Runtime, Version string
}

// NewOSV uses HAKOPOD_OSV_URL as the base URL when set (tests, mirrors).
func NewOSV() OSV {
	return OSV{
		BaseURL: cmp.Or(os.Getenv("HAKOPOD_OSV_URL"), osvDefaultBase),
		Client: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Host != via[0].URL.Host {
					return errors.New("osv: redirect to another host refused")
				}
				if len(via) >= 3 {
					return errors.New("osv: too many redirects")
				}
				return nil
			},
		},
	}
}

func (o OSV) Covered(runtime string) (ecosystem, pkg string, ok bool) {
	c, ok := osvCoverage[runtime]
	return c[0], c[1], ok
}

type osvEvent struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}

type osvVuln struct {
	ID               string    `json:"id"`
	Aliases          []string  `json:"aliases"`
	Summary          string    `json:"summary"`
	Details          string    `json:"details"`
	Modified         time.Time `json:"modified"`
	Published        time.Time `json:"published"`
	Withdrawn        string    `json:"withdrawn"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Affected []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		Ranges []struct {
			Type   string     `json:"type"`
			Events []osvEvent `json:"events"`
		} `json:"ranges"`
		DatabaseSpecific struct {
			Severity string `json:"severity"`
		} `json:"database_specific"`
	} `json:"affected"`
}

// Query returns advisories per query. Uncovered runtimes are skipped.
func (o OSV) Query(ctx context.Context, queries []RuntimeQuery) (map[RuntimeQuery][]Advisory, error) {
	if len(queries) > osvMaxQueries {
		return nil, fmt.Errorf("osv: %d queries exceeds limit of %d", len(queries), osvMaxQueries)
	}
	type pkgQ struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Version   string `json:"version"`
		PageToken string `json:"page_token,omitempty"`
	}
	var sent []RuntimeQuery
	var pq []pkgQ
	for _, q := range queries {
		eco, name, ok := o.Covered(q.Runtime)
		if !ok || q.Version == "" {
			continue
		}
		var p pkgQ
		p.Package.Ecosystem, p.Package.Name, p.Version = eco, name, q.Version
		pq = append(pq, p)
		sent = append(sent, q)
	}
	out := map[RuntimeQuery][]Advisory{}
	ids := make([][]string, len(sent))
	var truncated []string
	pending := make([]int, len(sent)) // indexes into sent still to fetch
	for i := range pending {
		pending[i] = i
	}
	for page := 0; len(pending) > 0; page++ {
		if page == osvMaxPages {
			truncated = append(truncated, fmt.Sprintf("%d queries past %d pages", len(pending), osvMaxPages))
			break
		}
		var body struct {
			Queries []pkgQ `json:"queries"`
		}
		for _, i := range pending {
			body.Queries = append(body.Queries, pq[i])
		}
		buf, _ := json.Marshal(body)
		var batch struct {
			Results []struct {
				Vulns []struct {
					ID string `json:"id"`
				} `json:"vulns"`
				NextPageToken string `json:"next_page_token"`
			} `json:"results"`
		}
		if err := o.do(ctx, http.MethodPost, "/v1/querybatch", buf, &batch); err != nil {
			return nil, err
		}
		if len(batch.Results) != len(pending) {
			return nil, fmt.Errorf("osv: %d results for %d queries", len(batch.Results), len(pending))
		}
		var next []int
		for j, r := range batch.Results {
			i := pending[j]
			for _, v := range r.Vulns {
				ids[i] = append(ids[i], v.ID)
			}
			if r.NextPageToken != "" {
				pq[i].PageToken = r.NextPageToken
				next = append(next, i)
			}
		}
		pending = next
	}
	// Pick distinct ids in query order up to the cap, then hydrate 4 at a time.
	cache := map[string]*osvVuln{}
	var order []string
	skipped := 0
	for i := range sent {
		for _, id := range ids[i] {
			if _, seen := cache[id]; seen {
				continue
			}
			if len(order) >= osvMaxVulns {
				skipped++
				cache[id] = nil
				continue
			}
			cache[id] = &osvVuln{}
			order = append(order, id)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan string)
	errs := make(chan error, len(order))
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				if err := o.do(ctx, http.MethodGet, "/v1/vulns/"+url.PathEscape(id), nil, cache[id]); err != nil {
					errs <- err
					cancel()
				}
			}
		}()
	}
feed:
	for _, id := range order {
		select {
		case work <- id:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, q := range sent {
		for _, id := range ids[i] {
			if v := cache[id]; v != nil {
				if a, ok := v.advisory(q.Runtime); ok {
					out[q] = append(out[q], a)
				}
			}
		}
	}
	if skipped > 0 {
		truncated = append(truncated, fmt.Sprintf("%d vulns past hydration cap %d", skipped, osvMaxVulns))
	}
	if len(truncated) > 0 {
		return out, fmt.Errorf("%w: %s", ErrOSVTruncated, strings.Join(truncated, "; "))
	}
	return out, nil
}

func (o OSV) do(ctx context.Context, method, path string, body []byte, into any) error {
	base := o.BaseURL
	if base == "" {
		base = osvDefaultBase
	}
	client := o.Client
	if client == nil {
		client = NewOSV().Client
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("osv: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("osv: %s %s: status %d", method, path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, osvMaxBody+1))
	if err != nil {
		return fmt.Errorf("osv: %w", err)
	}
	if len(data) > osvMaxBody {
		return fmt.Errorf("osv: %s response exceeds %d bytes", path, osvMaxBody)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("osv: decode %s: %w", path, err)
	}
	return nil
}

func (v *osvVuln) advisory(runtime string) (Advisory, bool) {
	eco, name, _ := OSV{}.Covered(runtime)
	if v.Withdrawn != "" {
		return Advisory{}, false
	}
	sev := v.DatabaseSpecific.Severity
	var ranges []Range
	for _, a := range v.Affected {
		if a.Package.Ecosystem != eco || a.Package.Name != name {
			continue
		}
		if sev == "" {
			sev = a.DatabaseSpecific.Severity
		}
		for _, r := range a.Ranges {
			if r.Type == "SEMVER" || r.Type == "ECOSYSTEM" {
				ranges = append(ranges, pairEvents(r.Events)...)
			}
		}
	}
	if len(ranges) == 0 {
		return Advisory{}, false
	}
	title := v.Summary
	if title == "" {
		title, _, _ = strings.Cut(strings.TrimSpace(v.Details), ". ")
		title = strings.TrimSuffix(strings.SplitN(title, "\n", 2)[0], ".")
		if r := []rune(title); len(r) > 120 {
			title = string(r[:120]) + "…"
		}
	}
	if title == "" {
		name := v.ID
		if len(v.Aliases) > 0 {
			name = v.Aliases[0]
		}
		for _, a := range v.Aliases {
			if strings.HasPrefix(a, "CVE-") {
				name = a
				break
			}
		}
		title = name + " in " + runtime
	}
	aliases := v.Aliases
	if len(aliases) > 20 {
		aliases = aliases[:20]
	}
	rev := v.Published
	if rev.IsZero() {
		rev = v.Modified
	}
	msg := v.Details
	if r := []rune(msg); len(r) > osvMessageMax {
		msg = string(r[:osvMessageMax]) + "…"
	}
	return Advisory{
		ID:          v.ID,
		Aliases:     aliases,
		Revision:    rev.Unix(),
		Source:      SourceOSV,
		Title:       title,
		Message:     msg,
		Severity:    osvSeverity(sev),
		URL:         "https://osv.dev/vulnerability/" + url.PathEscape(v.ID),
		PublishedAt: v.Published,
		Action:      ActionUpdate,
		Targets:     []Target{{Runtime: &RuntimeTarget{Runtime: runtime, Ranges: ranges}}},
	}, true
}

// pairEvents pairs each introduced with the following fixed or last_affected.
func pairEvents(events []osvEvent) []Range {
	var out []Range
	var open *Range
	for _, e := range events {
		switch {
		case e.Introduced != "":
			if open != nil {
				out = append(out, *open)
			}
			open = &Range{Introduced: e.Introduced}
		case e.Fixed != "" && open != nil:
			open.Fixed = e.Fixed
			out = append(out, *open)
			open = nil
		case e.LastAffected != "" && open != nil:
			open.LastAffected = e.LastAffected
			out = append(out, *open)
			open = nil
		}
	}
	if open != nil {
		out = append(out, *open)
	}
	return out
}

func osvSeverity(s string) Severity {
	switch strings.ToUpper(s) {
	case "CRITICAL":
		return Critical
	case "HIGH":
		return High
	case "LOW":
		return Low
	}
	return Moderate
}
