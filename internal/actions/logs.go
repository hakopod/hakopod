package actions

import (
	"strings"

	"github.com/hakopod/hakopod/internal/logmask"
)

// MaskLogLines runs before response bounds are applied. It copies caller-owned
// slices and preserves provider line numbers and section boundaries.
func MaskLogLines(lines []LogLine, secrets ...string) []LogLine {
	mask := logmask.New(secrets...)
	// Register all commands in the fetched window before returning any line.
	// This also protects previously printed values in an archived response.
	for _, line := range lines {
		mask.Line(line.Text)
	}
	out := make([]LogLine, len(lines))
	for i, line := range lines {
		out[i] = LogLine{Number: line.Number, Text: mask.Line(line.Text)}
	}
	return out
}

// ParseLogText discards a partial record at an artificial byte boundary. Live
// provider traces also withhold their unfinished last line until it is complete.
func ParseLogText(data []byte, limit int, complete bool, secrets ...string) ([]LogLine, bool) {
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	if (truncated || !complete) && len(data) > 0 && data[len(data)-1] != '\n' {
		if end := strings.LastIndexByte(string(data), '\n'); end >= 0 {
			data = data[:end+1]
		} else {
			data = nil
		}
		truncated = true
	}
	lines := []LogLine{}
	if len(data) > 0 {
		for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if i >= 10000 {
				truncated = true
				break
			}
			lines = append(lines, LogLine{Number: int64(i + 1), Text: line})
		}
	}
	lines = MaskLogLines(lines, secrets...)
	bounded, cut := BoundLogLines(lines, false)
	return bounded, truncated || cut
}
