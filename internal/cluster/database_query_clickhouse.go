package cluster

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) queryClickHouseSQL(ctx context.Context, d database.Resource, q database.QueryRequest, check func(context.Context) error) (database.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result := database.QueryResult{ReadOnly: q.IsReadOnly(), Columns: []database.QueryColumn{}, Rows: [][]*string{}, Outcome: "read"}
	if !q.IsReadOnly() && q.ExecutionMode != "nontransactional" {
		return result, &database.QueryError{Code: "database_query_nontransactional_mode_required", Outcome: "not_started"}
	}
	kind, err := queryStatementKind(q.SQL)
	if err != nil {
		return result, err
	}
	if q.IsReadOnly() && kind != "read" {
		return result, &database.QueryError{Code: "database_query_read_only", Outcome: "not_started"}
	}
	observation, err := c.ObserveDatabase(ctx, d)
	if err != nil || observation.Status != "ready" || len(observation.Members) == 0 {
		return result, queryUnavailable()
	}
	member := observation.Members[0]
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return result, queryUnavailable()
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || !postgresQueryCredentialOwned(secret, d, ns) {
		return result, queryUnavailable()
	}
	host := clickhouseMemberHost(d, member)
	identity, err := c.clickhouseTLSConfig(ctx, d, host, false)
	if err != nil {
		return result, queryUnavailable()
	}
	transport := &http.Transport{TLSClientConfig: identity, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DialContext: func(step context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(host, "8443") {
			return nil, queryUnavailable()
		}
		if err := step.Err(); err != nil {
			return nil, err
		}
		return c.clickhouseStream(ctx, d, member, 8443, false)
	}}
	defer transport.CloseIdleConnections()
	query := url.Values{"database": {"app"}, "wait_end_of_query": {"1"}, "max_execution_time": {"15"}, "max_result_rows": {"1001"}, "max_result_bytes": {"1048576"}, "result_overflow_mode": {"throw"}, "default_format": {"JSONCompact"}, "query_id": {store.NewID()}}
	if q.IsReadOnly() {
		query.Set("readonly", "1")
	}
	for i, p := range q.Parameters {
		value, err := clickHouseParameter(p)
		if err != nil {
			return result, err
		}
		query.Set("param_p"+strconv.Itoa(i+1), value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+net.JoinHostPort(host, "8443")+"/?"+query.Encode(), strings.NewReader(q.SQL))
	if err != nil {
		return result, queryUnavailable()
	}
	request.SetBasicAuth("app", string(secret.Data["password"]))
	verify := func(step context.Context) error {
		if err := check(step); err != nil {
			return err
		}
		current, e := c.kube.CoreV1().Secrets(ns.Name).Get(step, "database-credentials", metav1.GetOptions{})
		if e != nil || current.UID != secret.UID || current.ResourceVersion != secret.ResourceVersion {
			return queryUnavailable()
		}
		_, _, e = c.databaseExecTarget(step, d, member)
		return e
	}
	if err = verify(ctx); err != nil {
		return result, queryUnavailable()
	}
	response, err := (&http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return result, &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(q.MaxBytes)+1))
	if err != nil || len(body) > q.MaxBytes {
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: "unknown"}
	}
	if response.StatusCode != http.StatusOK {
		return result, &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
	}
	if err = verify(ctx); err != nil {
		return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: "unknown"}
	}
	if kind != "read" {
		result.Outcome = "applied"
		return result, nil
	}
	var output struct {
		Meta []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"meta"`
		Data                   [][]json.RawMessage `json:"data"`
		RowsBeforeLimitAtLeast int64               `json:"rows_before_limit_at_least"`
	}
	if json.Unmarshal(body, &output) != nil {
		return result, &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
	}
	for _, col := range output.Meta {
		result.Columns = append(result.Columns, database.QueryColumn{Name: col.Name, TypeName: col.Type})
	}
	if err = verify(ctx); err != nil {
		return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: "read"}
	}
	for _, values := range output.Data {
		row := make([]*string, len(values))
		for i, value := range values {
			if string(value) == "null" {
				continue
			}
			text := string(value)
			if len(value) > 0 && value[0] == '"' {
				if json.Unmarshal(value, &text) != nil {
					return result, queryUnavailable()
				}
			}
			row[i] = &text
		}
		result.Rows = append(result.Rows, row)
	}
	if len(result.Rows) > q.MaxRows {
		result.Rows = result.Rows[:q.MaxRows]
		result.Truncated = true
	}
	return result, nil
}
func clickHouseParameter(value any) (string, error) {
	switch x := value.(type) {
	case nil:
		return "\\N", nil
	case string:
		return x, nil
	case json.Number:
		return string(x), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	}
	return "", &database.QueryError{Code: "database_query_parameter_unsupported", Outcome: "not_started"}
}
