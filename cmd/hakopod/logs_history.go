package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	maxLiveSince    = 24 * time.Hour
	maxHistorySince = 30 * 24 * time.Hour
	logPageLines    = 500
	logPageCap      = 50
)

var (
	spanPattern   = regexp.MustCompile(`^([0-9]{1,6})([smhd])$`)
	podUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// parseSpan reads a Go duration such as 90s or 1h30m, or a day count such as 3d.
func parseSpan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, errors.New("duration must be positive")
		}
		return d, nil
	}
	m := spanPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a duration; use a number with s, m, h or d, such as 90m, 2h or 3d", s)
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	if n < 1 {
		return 0, errors.New("duration must be positive")
	}
	return time.Duration(n) * unit, nil
}

// parseWhen reads an RFC 3339 time, or a relative duration counted back from now.
func parseWhen(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	d, err := parseSpan(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a time; use RFC 3339 or a relative duration such as 2h or 3d", s)
	}
	return now.Add(-d).UTC(), nil
}

type historyOptions struct {
	Service, Pod, Query, From, To, Since string
	Limit                                int
}

// historyBody builds the first POST /logs/query body. The cursor is added per page.
func historyBody(o historyOptions, now time.Time) (map[string]any, error) {
	since := time.Hour
	if o.Since != "" {
		d, err := parseSpan(o.Since)
		if err != nil {
			return nil, err
		}
		since = d
	}
	if since > maxHistorySince {
		return nil, errors.New("--since is at most 30d with --history")
	}
	from := now.Add(-since).UTC()
	if o.From != "" {
		t, err := parseWhen(o.From, now)
		if err != nil {
			return nil, err
		}
		from = t
	}
	body := map[string]any{"service": o.Service, "query": o.Query, "history": true, "from": from.Format(time.RFC3339)}
	if o.To != "" {
		to, err := parseWhen(o.To, now)
		if err != nil {
			return nil, err
		}
		if !to.After(from) {
			return nil, errors.New("--to must be later than --from")
		}
		body["to"] = to.Format(time.RFC3339)
	}
	if o.Pod != "" {
		if podUIDPattern.MatchString(o.Pod) {
			body["pod_uid"] = o.Pod
		} else {
			body["pod"] = o.Pod
		}
	}
	return body, nil
}

type historyPage struct {
	Entries []struct {
		Timestamp time.Time `json:"timestamp"`
		Pod       string    `json:"pod"`
		Restart   int       `json:"restart"`
		Message   string    `json:"message"`
	} `json:"entries"`
	NextCursor string `json:"next_cursor"`
	History    *struct {
		Available   bool   `json:"available"`
		Message     string `json:"message"`
		ScanLimited bool   `json:"scan_limited"`
		Gaps        []struct {
			PodUID  string `json:"pod_uid"`
			PodName string `json:"pod_name"`
			Seconds int    `json:"seconds"`
			Note    string `json:"note"`
		} `json:"gaps"`
	} `json:"history"`
}

// liveOptions are the settings only the live path uses.
type liveOptions struct {
	Container string
	Tail      int64
	Now       time.Time
}

// printLiveFallback repeats an unavailable history request as the live query
// the non-history path sends. Live logs have no cursor and end now.
func printLiveFallback(ctx context.Context, c *client, appID string, history map[string]any, message string, live liveOptions, out, errOut io.Writer) error {
	since := int64(3600)
	if f, ok := history["from"].(string); ok {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			since = int64(live.Now.Sub(t).Seconds())
		}
	}
	since = min(max(since, 1), 86400)
	pod, _ := history["pod"].(string)
	note := message + " Showing live logs from current pods instead (up to 24h)."
	if history["pod_uid"] != nil {
		note += " Pod UIDs only apply to stored history; showing all pods."
	}
	fmt.Fprintln(errOut, strings.TrimSpace(note))
	var result any
	if err := c.request(ctx, "POST", "/applications/"+appID+"/logs/query", map[string]any{"service": history["service"], "pod": pod, "container": live.Container, "query": history["query"], "tail": live.Tail, "since_seconds": since, "previous": false}, "", &result); err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(result)
}

// printHistoryLogs follows next_cursor until limit lines are printed or no cursor remains.
func printHistoryLogs(ctx context.Context, c *client, appID string, body map[string]any, limit int, live liveOptions, out, errOut io.Writer) error {
	printed, seenGaps := 0, map[string]bool{}
	cursor := ""
	for pages := 1; ; pages++ {
		page := min(limit-printed, logPageLines)
		req := map[string]any{"limit": page}
		for k, v := range body {
			req[k] = v
		}
		if cursor != "" {
			req["cursor"] = cursor
		}
		var res historyPage
		if err := c.request(ctx, "POST", "/applications/"+appID+"/logs/query", req, "", &res); err != nil {
			return err
		}
		if h := res.History; h != nil {
			if !h.Available && pages == 1 {
				return printLiveFallback(ctx, c, appID, body, h.Message, live, out, errOut)
			}
			if !h.Available {
				msg := "Log history is not available on this installation."
				if h.Message != "" {
					msg += " " + h.Message
				}
				fmt.Fprintln(errOut, msg)
			}
			for _, g := range h.Gaps {
				key := g.PodUID + "|" + g.Note
				if seenGaps[key] {
					continue
				}
				seenGaps[key] = true
				fmt.Fprintf(errOut, "Gap in logs for pod %s: up to %d seconds missing. %s\n", g.PodName, g.Seconds, g.Note)
			}
			if h.ScanLimited {
				fmt.Fprintln(errOut, "The search stopped before the end of the time window; narrow --from and --to to see more.")
			}
		}
		for _, e := range res.Entries {
			fmt.Fprintf(out, "%s %s r%d %s\n", e.Timestamp.UTC().Format(time.RFC3339Nano), e.Pod, e.Restart, e.Message)
		}
		printed += len(res.Entries)
		cursor = res.NextCursor
		if cursor == "" {
			return nil
		}
		if printed >= limit {
			fmt.Fprintf(errOut, "Stopped after %d lines; more are available. Raise --limit or narrow the time window.\n", printed)
			return nil
		}
		if pages >= logPageCap {
			fmt.Fprintf(errOut, "Stopped after %d pages; narrow --since or --query to see more.\n", logPageCap)
			return nil
		}
	}
}

type logSourceList struct {
	Items []struct {
		PodName    string     `json:"pod_name"`
		Service    string     `json:"service"`
		Revision   int64      `json:"revision"`
		StartedAt  time.Time  `json:"started_at"`
		EndedAt    *time.Time `json:"ended_at"`
		EndReason  string     `json:"end_reason"`
		GapSeconds int        `json:"gap_seconds"`
		Current    bool       `json:"current"`
	} `json:"items"`
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

// printLogPods lists current and past pods as an aligned table.
func printLogPods(ctx context.Context, c *client, appID, since string, now time.Time, out, errOut io.Writer) error {
	if since == "" {
		since = "7d"
	}
	d, err := parseSpan(since)
	if err != nil {
		return err
	}
	if d > maxHistorySince {
		return errors.New("--since is at most 30d")
	}
	q := url.Values{"from": {now.Add(-d).UTC().Format(time.RFC3339)}, "limit": {"500"}}
	var res logSourceList
	if err := c.request(ctx, "GET", "/applications/"+appID+"/log-sources?"+q.Encode(), nil, "", &res); err != nil {
		return err
	}
	if !res.Available {
		msg := "Log history is not available on this installation."
		if res.Message != "" {
			msg += " " + res.Message
		}
		fmt.Fprintln(errOut, msg+" Past pods are only listed when log history is enabled.")
	}
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "POD\tSERVICE\tREVISION\tSTARTED\tENDED\tREASON\tGAP")
	for _, p := range res.Items {
		ended, reason, gap := "running", p.EndReason, "-"
		if p.EndedAt != nil {
			ended = p.EndedAt.UTC().Format(time.RFC3339)
		} else if !p.Current {
			ended = "unknown"
		}
		if reason == "" {
			reason = "-"
		}
		if p.GapSeconds > 0 {
			gap = (time.Duration(p.GapSeconds) * time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", p.PodName, p.Service, p.Revision, p.StartedAt.UTC().Format(time.RFC3339), ended, reason, gap)
	}
	return w.Flush()
}
