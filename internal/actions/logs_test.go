package actions

import (
	"strings"
	"testing"
)

func TestArchivedLogsMaskBeforeTruncationWithoutMutatingSource(t *testing.T) {
	original := []LogLine{{Number: 1, Text: "earlier private-value"}, {Number: 2, Text: "::add-mask::private-value"}, {Number: 3, Text: "PASSWORD=raw-value"}}
	masked := MaskLogLines(original)
	if original[0].Text != "earlier private-value" || masked[0].Number != 1 || strings.Contains(masked[0].Text, "private-value") || strings.Contains(masked[1].Text, "private-value") || strings.Contains(masked[2].Text, "raw-value") {
		t.Fatal("archive masking lost scope, identity or source immutability")
	}
}

func TestProviderLogByteLimitCannotExposePartialSecret(t *testing.T) {
	data := []byte("safe\ncredential-start-and-rest\n")
	lines, cut := ParseLogText(data, 12, true)
	if !cut || len(lines) != 1 || lines[0].Text != "safe" {
		t.Fatal("artificial byte boundary exposed an incomplete record")
	}
	lines, cut = ParseLogText([]byte("safe\npartial-private"), 100, false)
	if !cut || len(lines) != 1 {
		t.Fatal("live trace exposed an unfinished final record")
	}
	lines, cut = ParseLogText([]byte("complete final record"), 100, true)
	if cut || len(lines) != 1 || lines[0].Text != "complete final record" {
		t.Fatal("complete archive lost its final record")
	}
}
