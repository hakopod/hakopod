package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type brokenReader struct{ remaining int }

func (r *brokenReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, errors.New("producer failed")
	}
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	return n, nil
}
func TestSequentialMultipartAndAbort(t *testing.T) {
	var active, maxActive, parts, complete, aborted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("official SigV4 signature missing")
		}
		n := active.Add(1)
		defer active.Add(-1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == "POST" && q.Has("uploads"):
			io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>")
		case r.Method == "PUT" && q.Has("partNumber"):
			parts.Add(1)
			w.Header().Set("ETag", `"part"`)
		case r.Method == "POST" && q.Has("uploadId"):
			complete.Add(1)
			io.WriteString(w, "<CompleteMultipartUploadResult><ETag>complete</ETag></CompleteMultipartUploadResult>")
		case r.Method == "DELETE" && q.Has("uploadId"):
			aborted.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected S3 request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	d, c, _ := testDestination(t)
	d.Endpoint = server.URL
	client := NewS3(d, c)
	defer client.Close()
	length := int64(PartSize + 123)
	n, digest, err := client.Put(context.Background(), "backup.age", io.LimitReader(zeroReader{}, length), length)
	hash := sha256.New()
	io.Copy(hash, io.LimitReader(zeroReader{}, length))
	if err != nil || n != length || digest != hex.EncodeToString(hash.Sum(nil)) || parts.Load() != 2 || complete.Load() != 1 || maxActive.Load() != 1 {
		t.Fatalf("multipart did not stream sequentially: %d %v parts=%d complete=%d concurrency=%d", n, err, parts.Load(), complete.Load(), maxActive.Load())
	}
	_, _, err = client.Put(context.Background(), "broken.age", &brokenReader{remaining: PartSize + 1}, int64(PartSize*2))
	if err == nil || aborted.Load() != 1 || complete.Load() != 1 {
		t.Fatalf("failed producer did not abort multipart: %v abort=%d complete=%d", err, aborted.Load(), complete.Load())
	}
}
