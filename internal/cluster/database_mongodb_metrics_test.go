package cluster

import (
	"math"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoDBMetricsRejectIncompleteAndOverflowingCounters(t *testing.T) {
	encode := func(v bson.D) bson.Raw {
		raw, err := bson.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	stats := encode(bson.D{{Key: "dataSize", Value: int64(1024)}})
	status := func(counter any) bson.Raw {
		return encode(bson.D{{Key: "uptime", Value: int64(50)}, {Key: "connections", Value: bson.D{{Key: "current", Value: 12}, {Key: "active", Value: 3}}}, {Key: "opcounters", Value: bson.D{{Key: "insert", Value: counter}, {Key: "query", Value: 2}, {Key: "update", Value: 3}, {Key: "delete", Value: 4}, {Key: "getmore", Value: 5}, {Key: "command", Value: 6}}}})
	}
	m, err := parseMongoDBEngineMetrics(status(int64(1)), stats)
	if err != nil || !m.Available || *m.Commands != 21 || *m.Connections != 12 || *m.DataBytes != 1024 || m.CacheHitRatio != nil || m.ReplicationLagBytes != nil {
		t.Fatal("MongoDB metric interpretation changed", err)
	}
	for _, bad := range []any{int64(-1), int64(math.MaxInt64), math.Inf(1), math.NaN(), 1.5, "1"} {
		if _, err := parseMongoDBEngineMetrics(status(bad), stats); err == nil {
			t.Fatal("invalid MongoDB counter was accepted")
		}
	}
	if _, err := parseMongoDBEngineMetrics(status(int64(1)), encode(bson.D{})); err == nil {
		t.Fatal("missing MongoDB data size was reported as zero")
	}
}
