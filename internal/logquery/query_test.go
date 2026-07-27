package logquery

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStructuredMetadataLimitsPreserveSearchableMessage(t *testing.T) {
	deep := strings.Repeat(`{"nested":`, 1000) + `0` + strings.Repeat(`}`, 1000)
	fields := []string{`"level":"error"`}
	for i := 0; i < 200; i++ {
		fields = append(fields, fmt.Sprintf(`"field%d":%d`, i, i))
	}
	for _, message := range []string{deep, "{" + strings.Join(fields, ",") + "}", `{"message":"` + strings.Repeat("x", 64<<10) + `"}`} {
		e := ParseEntry("2026-09-12T12:00:00Z "+message, "api", "pod-1", "app")
		if !e.MetadataOmitted || len(e.Fields) != 0 || e.Message != message || e.Timestamp.IsZero() || e.Severity != "DEFAULT" {
			t.Fatal("oversized structured metadata changed the original message or retained its object graph")
		}
		match, err := Compile(`message CONTAINS '"'`)
		if err != nil || !match(e) {
			t.Fatal("omitted structured metadata must not make the original message unsearchable")
		}
	}
	valid := strings.Repeat(`{"nested":`, 7) + `{"level":"info"}` + strings.Repeat(`}`, 7)
	if e := ParseEntry(valid, "api", "pod-1", "app"); e.MetadataOmitted || len(e.Fields) == 0 {
		t.Fatal("supported nested metadata was omitted")
	}
	for _, invalid := range []string{`{"level":"error"} trailing`, `{"level":"error"}{"next":1}`, `{"level":"error"`} {
		if e := ParseEntry(invalid, "api", "pod-1", "app"); len(e.Fields) != 0 || e.Severity != "DEFAULT" {
			t.Fatal("a JSON prefix was mistaken for a complete structured log object")
		}
	}
}

func TestStructuredBudgetCountsMetadataAndRejectsNonfiniteNumbers(t *testing.T) {
	e := ParseEntry(`{"value":503,"nested":{"array":[1,2,"text"]}}`, "api", "pod-1", "app")
	if e.RetainedBytes() <= len(e.Message)+512 {
		t.Fatal("retained budget omitted structured metadata")
	}
	for _, actual := range []any{"NaN", "Infinity", "-Inf", json.Number("1e9999"), "not-a-number"} {
		for _, operator := range []string{"=", "!=", ">", "<", ">=", "<="} {
			match, err := Compile("json.value " + operator + " 500")
			if err != nil || match(Entry{Fields: map[string]any{"value": actual}}) {
				t.Errorf("nonfinite/non-numeric value %v matched a numeric comparison %s: %v", actual, operator, err)
			}
		}
	}
	for _, expected := range []string{"NaN", "Inf", "Infinity", "1e9999"} {
		match, err := Compile("json.value = " + expected)
		if err != nil || match(e) {
			t.Errorf("nonfinite predicate matched a finite value: %s %v", expected, err)
		}
	}
	match, err := Compile(`json.value = 'NaN'`)
	if err != nil || !match(Entry{Fields: map[string]any{"value": "NaN"}}) {
		t.Fatal("explicit text comparison for NaN should still work")
	}
}

func TestStructuredPredicatesAndPrecedence(t *testing.T) {
	e := ParseEntry(`2026-09-12T12:00:00Z {"level":"error","message":"Upstream TIMEOUT","status":503,"request":{"method":"POST"}}`, "api", "api-123", "app")
	for _, query := range []string{
		`severity >= WARNING AND message ILIKE '%timeout%'`,
		`WHERE json.status >= 500 AND json.request.method = 'POST'`,
		`pod IN ('web-1', 'api-123') AND NOT severity < ERROR`,
		`json.missing IS NULL AND json.status IS NOT NULL`,
		`service = web OR service = api AND container = app`,
		`timestamp >= '2026-09-12T11:00:00Z' AND timestamp < '2026-09-13T00:00:00Z'`,
		`message CONTAINS 'Upstream'`,
	} {
		p, err := Compile(query)
		if err != nil {
			t.Errorf("%s: %v", query, err)
			continue
		}
		if !p(e) {
			t.Errorf("did not match %s", query)
		}
	}
	for _, query := range []string{`severity < INFO`, `json.status < 500`, `message LIKE '%timeout%'`, `pod NOT IN ('api-123')`, `(service=web OR service=api) AND container=missing`, `json.missing != 'value'`} {
		p, err := Compile(query)
		if err != nil || p(e) {
			t.Errorf("unexpected match %s: %v", query, err)
		}
	}
}
func TestQueryBoundsAndInvalidGrammar(t *testing.T) {
	for _, query := range []string{`SELECT * FROM logs`, `message = 'oops`, `message =~ 'x'`, `level = error`, `message = 'x'; DROP TABLE logs`, `message=`, `(service=api`, strings.Repeat("NOT ", 20) + "service=api", strings.Repeat(" ", 4097), strings.Repeat("service=api OR ", 80) + "service=api"} {
		if _, err := Compile(query); err == nil {
			t.Errorf("accepted invalid query %q", query)
		}
	}
	p, err := Compile(`message = 'it''s ready'`)
	if err != nil || !p(Entry{Message: "it's ready"}) {
		t.Fatal("SQL quoted apostrophe failed", err)
	}
}
func TestPlainLogsHaveNoInventedSeverityOrTime(t *testing.T) {
	e := ParseEntry("ERROR nothing structured", "s", "p", "app")
	if e.Severity != "DEFAULT" || !e.Timestamp.IsZero() || len(e.Fields) != 0 {
		t.Fatal("invented metadata", e)
	}
}
func FuzzCompile(f *testing.F) {
	for _, s := range []string{"", "severity >= ERROR", "json.status IN (200,500)", "(NOT message ILIKE '%timeout%')"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := Compile(s)
		if err == nil {
			p(Entry{Message: "hello", Severity: "INFO", Fields: map[string]any{"status": 500}})
		}
	})
}
