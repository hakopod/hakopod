package backup

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// testObjects lives in backup_test.go and must satisfy the widened ObjectStore.
// The service tests it serves only exercise single objects, so these stubs stay
// empty on purpose.
func (o *testObjects) ListPrefix(context.Context, string, string) ([]string, string, error) {
	return nil, "", nil
}
func (o *testObjects) DeletePrefix(context.Context, string) (bool, error) { return false, nil }

// fakeBucket answers only the two requests these tests need: a paged
// ListObjectsV2 and a batched DeleteObjects. It is deliberately not a general
// S3 implementation.
type fakeBucket struct {
	mu          sync.Mutex
	keys        map[string]bool
	refuse      bool
	listCalls   int
	deleteCalls int
}

func (b *fakeBucket) sorted() []string {
	names := make([]string, 0, len(b.keys))
	for key := range b.keys {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

func (b *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	query := r.URL.Query()
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	switch {
	case r.Method == "GET" && query.Get("list-type") == "2":
		b.listCalls++
		limit, _ := strconv.Atoi(query.Get("max-keys"))
		prefix, after := query.Get("prefix"), query.Get("continuation-token")
		var page []string
		truncated := false
		for _, key := range b.sorted() {
			if !strings.HasPrefix(key, prefix) || key <= after {
				continue
			}
			if len(page) == limit {
				truncated = true
				break
			}
			page = append(page, key)
		}
		var out strings.Builder
		out.WriteString("<ListBucketResult><IsTruncated>" + strconv.FormatBool(truncated) + "</IsTruncated>")
		for _, key := range page {
			out.WriteString("<Contents><Key>" + key + "</Key></Contents>")
		}
		if truncated {
			out.WriteString("<NextContinuationToken>" + page[len(page)-1] + "</NextContinuationToken>")
		}
		out.WriteString("</ListBucketResult>")
		io.WriteString(w, out.String())
	case r.Method == "POST" && query.Has("delete"):
		b.deleteCalls++
		var request struct {
			Object []struct{ Key string } `xml:"Object"`
		}
		if err := xml.Unmarshal(body, &request); err != nil {
			w.WriteHeader(400)
			return
		}
		var out strings.Builder
		out.WriteString("<DeleteResult>")
		for _, object := range request.Object {
			if b.refuse {
				out.WriteString("<Error><Key>" + object.Key + "</Key><Code>AccessDenied</Code></Error>")
				continue
			}
			delete(b.keys, object.Key)
		}
		out.WriteString("</DeleteResult>")
		io.WriteString(w, out.String())
	default:
		w.WriteHeader(400)
	}
}

func newFakeBucket(t *testing.T, keys ...string) (*fakeBucket, *S3) {
	t.Helper()
	bucket := &fakeBucket{keys: map[string]bool{}}
	for _, key := range keys {
		bucket.keys[key] = true
	}
	server := httptest.NewServer(bucket)
	t.Cleanup(server.Close)
	d, c, _ := testDestination(t)
	d.Endpoint = server.URL
	client := NewS3(d, c)
	t.Cleanup(client.Close)
	return bucket, client
}

func TestEnginePrefixKeepsExistingObjectKeys(t *testing.T) {
	d, _, _ := testDestination(t)
	id := strings.Repeat("b", 32)
	if got := ObjectKey(d, id); got != "test/hakopod/"+id+".age" {
		t.Fatalf("dump artifact key moved: %s", got)
	}
	if got := EnginePrefix(d, id); got != "test/hakopod/"+id+"/" {
		t.Fatalf("engine prefix wrong: %s", got)
	}
	bare := d
	bare.Prefix = ""
	if got := ObjectKey(bare, id); got != "hakopod/"+id+".age" {
		t.Fatalf("dump artifact key moved without a destination prefix: %s", got)
	}
	if strings.HasPrefix(ObjectKey(d, id), EnginePrefix(d, id)) {
		t.Fatal("engine prefix contains the single-object key for the same artifact")
	}
}

func TestListPrefixPagesBeyondOnePage(t *testing.T) {
	const total = 2300
	prefix := EnginePrefix(func() Destination { d, _, _ := testDestination(t); return d }(), strings.Repeat("c", 32))
	keys := make([]string, 0, total+1)
	for i := 0; i < total; i++ {
		keys = append(keys, fmt.Sprintf("%spart-%05d", prefix, i))
	}
	keys = append(keys, "test/hakopod/other.age")
	_, client := newFakeBucket(t, keys...)

	seen, token, pages := map[string]bool{}, "", 0
	for {
		page, next, err := client.ListPrefix(context.Background(), prefix, token)
		if err != nil {
			t.Fatalf("listing failed on page %d: %v", pages, err)
		}
		pages++
		if len(page) > PrefixPageSize {
			t.Fatalf("page %d returned %d keys, over the %d bound", pages, len(page), PrefixPageSize)
		}
		if next != "" && len(page) != PrefixPageSize {
			t.Fatalf("page %d is short at %d keys but claims more follow", pages, len(page))
		}
		for _, key := range page {
			if seen[key] {
				t.Fatalf("key listed twice: %s", key)
			}
			seen[key] = true
		}
		if token = next; token == "" {
			break
		}
		if pages > 10 {
			t.Fatal("listing did not terminate")
		}
	}
	if pages != 3 || len(seen) != total {
		t.Fatalf("expected %d keys over 3 pages, got %d keys over %d pages", total, len(seen), pages)
	}
	if seen["test/hakopod/other.age"] {
		t.Fatal("listing returned a key outside the prefix")
	}
}

func TestDeletePrefixCompletesAndResumesAfterInterruption(t *testing.T) {
	const total = MaxPrefixDeletePerCall + 2000
	prefix := EnginePrefix(func() Destination { d, _, _ := testDestination(t); return d }(), strings.Repeat("d", 32))
	keys := make([]string, 0, total+1)
	for i := 0; i < total; i++ {
		keys = append(keys, fmt.Sprintf("%spart-%05d", prefix, i))
	}
	keys = append(keys, "test/hakopod/keep-me.age")
	bucket, client := newFakeBucket(t, keys...)

	// The first call spends its per-call budget and must report that work
	// remains: reporting completion here is what would leave a paid-for tree in
	// the bucket while the database row says the artifact is gone.
	remaining, err := client.DeletePrefix(context.Background(), prefix)
	if err != nil || !remaining {
		t.Fatalf("interrupted deletion reported remaining=%v err=%v", remaining, err)
	}
	bucket.mu.Lock()
	left := len(bucket.keys)
	bucket.mu.Unlock()
	if left != total+1-MaxPrefixDeletePerCall {
		t.Fatalf("first call deleted %d objects, expected %d", total+1-left, MaxPrefixDeletePerCall)
	}

	remaining, err = client.DeletePrefix(context.Background(), prefix)
	if err != nil || remaining {
		t.Fatalf("second deletion did not finish: remaining=%v err=%v", remaining, err)
	}
	bucket.mu.Lock()
	survivors := bucket.sorted()
	bucket.mu.Unlock()
	if len(survivors) != 1 || survivors[0] != "test/hakopod/keep-me.age" {
		t.Fatalf("deletion left the wrong objects: %v", survivors)
	}
	// A third call on an empty prefix is safe and still reports completion.
	if remaining, err = client.DeletePrefix(context.Background(), prefix); err != nil || remaining {
		t.Fatalf("repeat deletion of an empty prefix: remaining=%v err=%v", remaining, err)
	}
}

func TestDeletePrefixReportsRefusedObjectsAsRemaining(t *testing.T) {
	prefix := EnginePrefix(func() Destination { d, _, _ := testDestination(t); return d }(), strings.Repeat("e", 32))
	bucket, client := newFakeBucket(t, prefix+"part-0", prefix+"part-1")
	bucket.refuse = true
	remaining, err := client.DeletePrefix(context.Background(), prefix)
	if err == nil || !remaining {
		t.Fatalf("refused deletes reported remaining=%v err=%v", remaining, err)
	}
	bucket.refuse = false
	if remaining, err = client.DeletePrefix(context.Background(), prefix); err != nil || remaining {
		t.Fatalf("retry after refusal did not finish: remaining=%v err=%v", remaining, err)
	}
	if _, err = client.DeletePrefix(context.Background(), ""); err == nil {
		t.Fatal("an empty prefix was accepted for deletion")
	}
	if _, _, err = client.ListPrefix(context.Background(), "", ""); err == nil {
		t.Fatal("an empty prefix was accepted for listing")
	}
}
