package advisory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func osvServer(t *testing.T, batch string, vulns map[string]string) (OSV, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		file := ""
		if r.URL.Path == "/v1/querybatch" {
			file = batch
		} else if id, ok := strings.CutPrefix(r.URL.Path, "/v1/vulns/"); ok {
			file = vulns[id]
		}
		if file == "" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return OSV{Client: srv.Client(), BaseURL: srv.URL}, calls
}

var osvFixtures = map[string]string{
	"GO-2024-2598":        "osv_vuln_go.json",
	"BIT-node-2022-32212": "osv_vuln_node.json",
}

func TestOSVConvertsRangesAndSeverity(t *testing.T) {
	o, _ := osvServer(t, "osv_querybatch.json", osvFixtures)
	goQ, nodeQ := RuntimeQuery{"go", "1.22.0"}, RuntimeQuery{"node", "18.0.0"}
	got, err := o.Query(context.Background(), []RuntimeQuery{goQ, nodeQ})
	if err != nil {
		t.Fatal(err)
	}
	g := got[goQ][0]
	if g.ID != "GO-2024-2598" || g.Source != SourceOSV || g.Action != ActionUpdate || g.Severity != Moderate ||
		g.URL != "https://osv.dev/vulnerability/GO-2024-2598" || g.Revision != 1709676898 || !reflect.DeepEqual(g.Aliases, []string{"BIT-golang-2024-24783", "CVE-2024-24783"}) || g.PublishedAt.IsZero() {
		t.Fatalf("go advisory: %+v", g)
	}
	wantGo := []Range{{Introduced: "0", Fixed: "1.21.8"}, {Introduced: "1.22.0-0", Fixed: "1.22.1"}}
	if rt := g.Targets[0].Runtime; rt.Runtime != "go" || !reflect.DeepEqual(rt.Ranges, wantGo) {
		t.Fatalf("go ranges: %+v", rt)
	}
	n := got[nodeQ][0]
	if n.Severity != High || n.Title != "A OS Command Injection vulnerability exists in Node.js versions <14.20.0, <16.20.0, <18.5.0 due to an insufficient IsAll…" || n.Revision != 1709723058 {
		t.Fatalf("node advisory: %+v", n)
	}
	wantNode := []Range{{Introduced: "14.0.0", Fixed: "14.14.1"}, {Introduced: "14.15.0", Fixed: "14.20.1"}, {Introduced: "16.0.0", Fixed: "16.12.1"}, {Introduced: "16.13.0", Fixed: "16.17.1"}, {Introduced: "18.0.0", Fixed: "18.5.0"}}
	if !reflect.DeepEqual(n.Targets[0].Runtime.Ranges, wantNode) {
		t.Fatalf("node ranges: %+v", n.Targets[0].Runtime.Ranges)
	}
}

func TestOSVBatchLimit(t *testing.T) {
	o, calls := osvServer(t, "osv_querybatch.json", osvFixtures)
	if _, err := o.Query(context.Background(), make([]RuntimeQuery, 33)); err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestOSVUncoveredRuntimeSkipped(t *testing.T) {
	o, calls := osvServer(t, "osv_querybatch.json", osvFixtures)
	got, err := o.Query(context.Background(), []RuntimeQuery{{"cobol", "1.0"}})
	if err != nil || len(got) != 0 || calls.Load() != 0 {
		t.Fatalf("got=%v err=%v calls=%d", got, err, calls.Load())
	}
	if _, _, ok := o.Covered("cobol"); ok {
		t.Fatal("cobol covered")
	}
}

func TestOSVWithdrawnSkipped(t *testing.T) {
	o, _ := osvServer(t, "osv_querybatch_withdrawn.json", map[string]string{"GO-2024-WITHDRAWN": "osv_vuln_withdrawn.json"})
	got, err := o.Query(context.Background(), []RuntimeQuery{{"go", "1.22.0"}})
	if err != nil || len(got[RuntimeQuery{"go", "1.22.0"}]) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestOSVOversizeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"vulns":[]}],"pad":"` + strings.Repeat("x", osvMaxBody) + `"}`))
	}))
	defer srv.Close()
	_, err := OSV{Client: srv.Client(), BaseURL: srv.URL}.Query(context.Background(), []RuntimeQuery{{"go", "1.22.0"}})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v", err)
	}
}

func TestOSVPairEventsLastAffected(t *testing.T) {
	got := pairEvents([]osvEvent{{Introduced: "0"}, {LastAffected: "1.1"}, {Introduced: "2.0"}})
	if !reflect.DeepEqual(got, []Range{{Introduced: "0", LastAffected: "1.1"}, {Introduced: "2.0"}}) {
		t.Fatalf("%+v", got)
	}
}

// Pages are served until the token runs out; the 4th page is never fetched.
func TestOSVPagingTruncated(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/querybatch" {
			b, _ := os.ReadFile("testdata/osv_vuln_go.json")
			w.Write(b)
			return
		}
		var req struct {
			Queries []struct {
				PageToken string `json:"page_token"`
			} `json:"queries"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		pages = append(pages, req.Queries[0].PageToken)
		fmt.Fprintf(w, `{"results":[{"vulns":[{"id":"GO-2024-2598"}],"next_page_token":"p%d"}]}`, len(pages))
	}))
	defer srv.Close()
	q := RuntimeQuery{"go", "1.22.0"}
	got, err := OSV{Client: srv.Client(), BaseURL: srv.URL}.Query(context.Background(), []RuntimeQuery{q})
	if !errors.Is(err, ErrOSVTruncated) || !reflect.DeepEqual(pages, []string{"", "p1", "p2"}) || len(got[q]) != 3 {
		t.Fatalf("err=%v pages=%v n=%d", err, pages, len(got[q]))
	}
}

func TestOSVHydrationCapTruncated(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/querybatch" {
			gets.Add(1)
			b, _ := os.ReadFile("testdata/osv_vuln_go.json")
			w.Write(b)
			return
		}
		var ids []string
		for i := 0; i < osvMaxVulns+5; i++ {
			ids = append(ids, fmt.Sprintf(`{"id":"GO-%d"}`, i))
		}
		fmt.Fprintf(w, `{"results":[{"vulns":[%s]}]}`, strings.Join(ids, ","))
	}))
	defer srv.Close()
	q := RuntimeQuery{"go", "1.22.0"}
	got, err := OSV{Client: srv.Client(), BaseURL: srv.URL}.Query(context.Background(), []RuntimeQuery{q})
	if !errors.Is(err, ErrOSVTruncated) || gets.Load() != osvMaxVulns || len(got[q]) != osvMaxVulns {
		t.Fatalf("err=%v gets=%d n=%d", err, gets.Load(), len(got[q]))
	}
}

func TestOSVTitleFallbackAndCoverage(t *testing.T) {
	v := osvVuln{ID: "BIT-x", Aliases: []string{"BIT-x-min", "CVE-1"}}
	v.Affected = make([]struct {
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
	}, 1)
	v.Affected[0].Package.Name, v.Affected[0].Package.Ecosystem = "node", "Bitnami"
	v.Affected[0].Ranges = append(v.Affected[0].Ranges, struct {
		Type   string     `json:"type"`
		Events []osvEvent `json:"events"`
	}{"SEMVER", []osvEvent{{Introduced: "0"}}})
	if a, ok := v.advisory("node"); !ok || a.Title != "CVE-1 in node" {
		t.Fatalf("%+v", a)
	}
	v.Details = "First thing. Second."
	if a, _ := v.advisory("node"); a.Title != "First thing" {
		t.Fatalf("%q", a.Title)
	}
	if _, _, ok := (OSV{}).Covered("bun"); ok {
		t.Fatal("bun covered")
	}
	t.Setenv("HAKOPOD_OSV_URL", "http://fake")
	if NewOSV().BaseURL != "http://fake" {
		t.Fatal("env base url ignored")
	}
}
