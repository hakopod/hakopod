package cluster

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Connections and operation counters cover the observed primary, including
// replication, agents and monitoring. Data size covers only app's logical data.
func (c *Client) mongodbEngineMetrics(ctx context.Context, d database.Resource, o database.Observation) (database.EngineMetrics, error) {
	member, err := mongodbObservedPrimary(o)
	if err != nil {
		return database.EngineMetrics{}, err
	}
	client, closeClient, err := c.mongodbClient(ctx, d, member, true)
	if err != nil {
		return database.EngineMetrics{}, err
	}
	defer closeClient()
	status, err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}, {Key: "wiredTiger", Value: 0}, {Key: "metrics", Value: 0}, {Key: "locks", Value: 0}, {Key: "tcmalloc", Value: 0}}).Raw()
	if err != nil || len(status) > 512<<10 {
		return database.EngineMetrics{}, fmt.Errorf("MongoDB server statistics are unavailable")
	}
	stats, err := client.Database("app").RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}, {Key: "scale", Value: 1}, {Key: "freeStorage", Value: 0}}).Raw()
	if err != nil || len(stats) > 16<<10 {
		return database.EngineMetrics{}, fmt.Errorf("MongoDB application statistics are unavailable")
	}
	return parseMongoDBEngineMetrics(status, stats)
}

func parseMongoDBEngineMetrics(status, stats bson.Raw) (database.EngineMetrics, error) {
	m := database.EngineMetrics{}
	number := func(raw bson.Raw, key string) (int64, error) {
		value := raw.Lookup(key)
		switch value.Type {
		case bson.TypeInt32, bson.TypeInt64:
			n := value.AsInt64()
			if n >= 0 {
				return n, nil
			}
		case bson.TypeDouble:
			n := value.Double()
			if n >= 0 && n < math.MaxInt64 && !math.IsNaN(n) && !math.IsInf(n, 0) && n == math.Trunc(n) {
				return int64(n), nil
			}
		}
		return 0, fmt.Errorf("MongoDB statistics are incomplete")
	}
	connections, ok := status.Lookup("connections").DocumentOK()
	if !ok {
		return m, fmt.Errorf("MongoDB connection statistics are unavailable")
	}
	current, err := number(connections, "current")
	if err != nil {
		return m, err
	}
	active, err := number(connections, "active")
	if err != nil {
		return m, err
	}
	data, err := number(stats, "dataSize")
	if err != nil {
		return m, err
	}
	uptime, err := number(status, "uptime")
	if err != nil {
		return m, err
	}
	counters, ok := status.Lookup("opcounters").DocumentOK()
	if !ok {
		return m, fmt.Errorf("MongoDB operation statistics are unavailable")
	}
	var operations int64
	for _, key := range []string{"insert", "query", "update", "delete", "getmore", "command"} {
		n, err := number(counters, key)
		if err != nil || n > math.MaxInt64-operations {
			return m, fmt.Errorf("MongoDB operation statistics are invalid")
		}
		operations += n
	}
	limit := int64(200)
	m.Connections, m.ActiveConnections, m.MaxConnections, m.DataBytes, m.Commands, m.UptimeSeconds = &current, &active, &limit, &data, &operations, &uptime
	if !validDatabaseEngineMetrics(m) {
		return m, fmt.Errorf("MongoDB statistics are invalid")
	}
	stamp := time.Now().UTC()
	m.Available, m.SampledAt = true, &stamp
	return m, nil
}
