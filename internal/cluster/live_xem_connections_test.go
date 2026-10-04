package cluster

import "testing"

func TestXemRedisConnections(t *testing.T) {
	const selected = "addr=10.42.0.5:43120 db=2 cmd=ping tot-cmds=5"
	const unused = "addr=10.42.0.5:43121 db=0 cmd=NULL tot-cmds=0"
	for _, test := range []struct {
		name        string
		listing     string
		database    string
		initialized int
		unused      int
		wantError   bool
	}{
		{name: "selected database and untouched pool", listing: selected + "\n" + unused, database: "2", initialized: 1, unused: 1},
		{name: "wrong initialized database", listing: selected + "\naddr=10.42.0.5:43121 db=0 cmd=ping tot-cmds=5", database: "2", wantError: true},
		{name: "no completed backend commands", listing: unused, database: "2", wantError: true},
		{name: "other pod cannot prove backend connectivity", listing: "addr=10.42.0.6:43120 db=2 cmd=ping tot-cmds=5", database: "2", wantError: true},
		{name: "missing command count fails closed", listing: "addr=10.42.0.5:43120 db=2 cmd=ping", database: "2", wantError: true},
		{name: "null command with nonzero count is not unused", listing: selected + "\naddr=10.42.0.5:43121 db=0 cmd=NULL tot-cmds=1", database: "2", wantError: true},
		{name: "bundled database", listing: "addr=10.42.0.5:43120 db=0 cmd=ping tot-cmds=5\n" + unused, database: "0", initialized: 1, unused: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			initialized, unused, err := xemRedisConnections(test.listing, map[string]bool{"10.42.0.5": true}, test.database)
			if test.wantError {
				if err == nil {
					t.Fatal("unverified or wrong-database backend connection passed")
				}
				return
			}
			if err != nil || initialized != test.initialized || unused != test.unused {
				t.Fatalf("got initialized=%d unused=%d err=%v; want %d and %d", initialized, unused, err, test.initialized, test.unused)
			}
		})
	}
}
