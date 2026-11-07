package api

import (
	"testing"
	"time"
)

func TestHistoryWindowValidatesOrder(t *testing.T) {
	now := time.Now().UTC()
	for order, ok := range map[string]bool{"": true, "asc": true, "desc": true, "newest": false, "DESC": false} {
		_, _, _, msg := historyWindow(logQueryRequest{Order: order}, now, 7)
		if (msg == "") != ok {
			t.Errorf("order %q: %q", order, msg)
		}
	}
}
