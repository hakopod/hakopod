package agent

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/operations"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
)

// AuditExport carries one bounded canonical CSV page, preserving its resume cursor.
type AuditExport struct {
	CSV        string `json:"csv"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func DecodeAuditExport(res *http.Response, out *AuditExport) error {
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "text/csv" {
		return errors.New("audit export did not return CSV")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("audit export exceeds 1 MiB; use audit history pagination")
	}
	cursor := res.Header.Get("X-Hakopod-Next-Cursor")
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 {
			return errors.New("invalid audit export cursor")
		}
	}
	out.CSV = string(data)
	out.NextCursor = cursor
	return nil
}
func (s *Server) auditExport(ctx context.Context, identity, before string) (any, error) {
	if !s.options.Installation || !s.options.AllowAdmin || s.cfg.Project != "" || s.cfg.Environment != "" {
		return nil, errors.New("audit export requires installation administration opt-in")
	}
	if len(identity) != 32 {
		return nil, errors.New("invalid audit identity")
	}
	for _, c := range identity {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return nil, errors.New("invalid audit identity")
		}
	}
	q := url.Values{"identity_id": {identity}}
	if before != "" {
		n, err := strconv.ParseInt(before, 10, 64)
		if err != nil || n < 1 {
			return nil, errors.New("invalid audit cursor")
		}
		q.Set("before", before)
	}
	if err := operations.RequireInstallation(ctx, operations.RequestFunc(s.client), false); err != nil {
		return nil, err
	}
	var out AuditExport
	err := s.client(ctx, "GET", "/audit/export?"+q.Encode(), nil, "", &out)
	return out, err
}
