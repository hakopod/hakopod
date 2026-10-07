package agent

import (
	"strings"
	"testing"
)

func TestEventSamplesPreserveCursorAndBounds(t *testing.T) {
	raw := []byte("id: 9007199254740993\ndata: {\"id\":9007199254740993}\n\nid: 9007199254740994\ndata: {\"id\":9007199254740994}\n\n")
	sample, err := ParseEventSample(raw, "0", 1)
	if err != nil || len(sample.Events) != 1 || sample.NextCursor != "9007199254740993" || !sample.Truncated {
		t.Fatal(sample, err)
	}
	if !strings.Contains(string(sample.Events[0]), "9007199254740993") {
		t.Fatal("precision lost")
	}
	if _, err := ParseEventSample([]byte("id: ../keys\ndata: {}\n\n"), "", 1); err == nil {
		t.Fatal("unsafe cursor accepted")
	}
}
