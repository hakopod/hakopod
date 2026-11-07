package worker

import (
	"testing"
	"time"
)

func TestBindingRefreshCadence(t *testing.T) {
	now := time.Now()
	last := bindingRefresh{at: now.Add(-time.Minute), key: "db1:3:ab"}
	for name, test := range map[string]struct {
		last bindingRefresh
		key  string
		now  time.Time
		due  bool
	}{
		"never refreshed":      {bindingRefresh{}, "db1:3:ab", now, true},
		"recently refreshed":   {last, "db1:3:ab", now, false},
		"interval passed":      {last, "db1:3:ab", last.at.Add(bindingRefreshInterval), true},
		"database revision":    {last, "db1:4:ab", now, true},
		"CA fingerprint":       {last, "db1:3:cd", now, true},
		"just inside interval": {last, "db1:3:ab", last.at.Add(bindingRefreshInterval - time.Second), false},
	} {
		if got := bindingRefreshDue(test.last, test.key, test.now); got != test.due {
			t.Errorf("%s: due=%v, want %v", name, got, test.due)
		}
	}
}
