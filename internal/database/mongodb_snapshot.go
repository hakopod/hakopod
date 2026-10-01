package database

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// MongoDB snapshots contain only app. A snapshot session fixes the read timestamp
// across collections; a change to collection identity or schema invalidates the
// capture. A caller must discard output on any error, including the final fence.
func CaptureMongoDBSnapshot(ctx context.Context, client *mongo.Client, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	db := client.Database("app", options.Database().SetReadPreference(readpref.Primary()))
	before, err := mongoDBManifest(ctx, db)
	if err != nil {
		return err
	}
	session, err := client.StartSession(options.Session().SetSnapshot(true))
	if err != nil {
		return fmt.Errorf("MongoDB snapshot session is unavailable")
	}
	defer session.EndSession(ctx)
	err = mongo.WithSession(ctx, session, func(snapshot context.Context) error {
		return WriteMongoDBArchive(out, before, func(i int, write func(bson.Raw) error) error {
			cursor, err := db.Collection(before.Collections[i].Name).Find(snapshot, bson.D{}, options.Find().SetBatchSize(32))
			if err != nil {
				return fmt.Errorf("MongoDB could not open a consistent collection snapshot")
			}
			defer cursor.Close(snapshot)
			for cursor.Next(snapshot) {
				if err := write(cursor.Current); err != nil {
					return err
				}
			}
			if cursor.Err() != nil {
				return fmt.Errorf("MongoDB snapshot cursor was interrupted or its history expired")
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	after, err := mongoDBManifest(ctx, db)
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("MongoDB collection identity or schema changed during capture")
	}
	return nil
}

// Metadata cursors are bounded independently of the data stream. Unknown schema
// options fail capture so recovery never silently drops a collection feature.
func mongoDBManifest(ctx context.Context, db *mongo.Database) (MongoDBArchive, error) {
	manifest := MongoDBArchive{Version: 1, EngineVersion: "8.0", Collections: []MongoDBCollection{}}
	cursor, err := db.ListCollections(ctx, bson.D{}, options.ListCollections().SetBatchSize(32))
	if err != nil {
		return manifest, fmt.Errorf("MongoDB collection metadata is unavailable")
	}
	defer cursor.Close(ctx)
	systemBuckets := map[string][]byte{}
	metadataBytes := 0
	for cursor.Next(ctx) {
		if len(manifest.Collections) >= MongoDBMaxCollections || len(systemBuckets) > MongoDBMaxCollections {
			return manifest, fmt.Errorf("MongoDB collection inventory exceeds its bound")
		}
		var item struct {
			Name    string   `bson:"name"`
			Type    string   `bson:"type"`
			Options bson.Raw `bson:"options"`
			Info    struct {
				UUID bson.Binary `bson:"uuid"`
			} `bson:"info"`
		}
		if cursor.Decode(&item) != nil {
			return manifest, fmt.Errorf("MongoDB collection metadata is invalid")
		}
		if item.Name == "system.views" {
			continue
		}
		if strings.HasPrefix(item.Name, "system.buckets.") {
			if item.Info.UUID.Subtype != 4 || len(item.Info.UUID.Data) != 16 {
				return manifest, fmt.Errorf("MongoDB bucket identity is unavailable")
			}
			systemBuckets[strings.TrimPrefix(item.Name, "system.buckets.")] = item.Info.UUID.Data
			continue
		}
		if item.Type == "collection" && (item.Info.UUID.Subtype != 4 || len(item.Info.UUID.Data) != 16) {
			return manifest, fmt.Errorf("MongoDB collection identity is unavailable")
		}
		entry := MongoDBCollection{Name: item.Name, Type: item.Type, UUID: item.Info.UUID.Data, Options: item.Options, Indexes: []bson.Raw{}}
		if item.Type != "view" {
			indexes, err := db.Collection(item.Name).Indexes().List(ctx, options.ListIndexes().SetBatchSize(16))
			if err != nil {
				return manifest, fmt.Errorf("MongoDB index metadata is unavailable")
			}
			for indexes.Next(ctx) {
				if len(entry.Indexes) >= 64 || len(indexes.Current) > 1<<20 {
					_ = indexes.Close(ctx)
					return manifest, fmt.Errorf("MongoDB index metadata exceeds its bound")
				}
				if _, err := mongoDBIndexOptions(indexes.Current); err != nil {
					_ = indexes.Close(ctx)
					return manifest, err
				}
				entry.Indexes = append(entry.Indexes, bytes.Clone(indexes.Current))
			}
			indexErr := indexes.Err()
			_ = indexes.Close(ctx)
			if indexErr != nil {
				return manifest, fmt.Errorf("MongoDB index metadata was interrupted")
			}
			slices.SortFunc(entry.Indexes, func(a, b bson.Raw) int {
				return strings.Compare(a.Lookup("name").StringValue(), b.Lookup("name").StringValue())
			})
		}
		metadataBytes += len(entry.Options)
		for _, index := range entry.Indexes {
			metadataBytes += len(index)
		}
		if metadataBytes > 4<<20 {
			return manifest, fmt.Errorf("MongoDB schema metadata exceeds its bound")
		}
		manifest.Collections = append(manifest.Collections, entry)
	}
	if cursor.Err() != nil {
		return manifest, fmt.Errorf("MongoDB collection metadata was interrupted")
	}
	for i, c := range manifest.Collections {
		if c.Type == "timeseries" {
			if len(systemBuckets[c.Name]) != 16 {
				return manifest, fmt.Errorf("MongoDB time series bucket identity is unavailable")
			}
			manifest.Collections[i].UUID = systemBuckets[c.Name]
			delete(systemBuckets, c.Name)
		}
	}
	if len(systemBuckets) != 0 {
		return manifest, fmt.Errorf("MongoDB has an unrecognized internal bucket collection")
	}
	slices.SortFunc(manifest.Collections, func(a, b MongoDBCollection) int {
		// Data collections precede views. MongoDB permits a view to refer to a
		// view created later, so no input-supplied dependency commands are needed.
		if (a.Type == "view") != (b.Type == "view") {
			if a.Type == "view" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return manifest, manifest.Validate()
}

func MongoDBSnapshotTargetEmpty(ctx context.Context, client *mongo.Client) error {
	cursor, err := client.Database("app").ListCollections(ctx, bson.D{}, options.ListCollections().SetNameOnly(true).SetBatchSize(1))
	if err != nil {
		return fmt.Errorf("MongoDB recovery target could not be inspected")
	}
	defer cursor.Close(ctx)
	if cursor.Next(ctx) {
		return fmt.Errorf("MongoDB recovery requires a separate empty application database")
	}
	if cursor.Err() != nil {
		return fmt.Errorf("MongoDB recovery target inventory was interrupted")
	}
	return nil
}

// The caller fences recovery and supplies the loopback-only recovery account,
// scoped to app with permission to preserve historical validation exceptions.
// Fixed commands keep archive input out of administrative databases.
func RestoreMongoDBSnapshot(ctx context.Context, client *mongo.Client, in io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := MongoDBSnapshotTargetEmpty(ctx, client); err != nil {
		return err
	}
	db := client.Database("app", options.Database().SetWriteConcern(writeconcern.Majority()).SetReadPreference(readpref.Primary()))
	var current *mongo.Collection
	var views []MongoDBCollection
	var batch []any
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := current.InsertMany(ctx, batch, options.InsertMany().SetOrdered(true).SetBypassDocumentValidation(true))
		batch, batchBytes = nil, 0
		if err != nil {
			return fmt.Errorf("MongoDB rejected recovered documents")
		}
		return nil
	}
	create := func(c MongoDBCollection) error {
		opts, err := mongoDBCollectionOptions(c)
		if err != nil {
			return err
		}
		command := append(bson.D{{Key: "create", Value: c.Name}, {Key: "writeConcern", Value: bson.D{{Key: "w", Value: "majority"}}}}, opts...)
		if err := db.RunCommand(ctx, command).Err(); err != nil {
			return fmt.Errorf("MongoDB rejected recovered collection settings")
		}
		current = db.Collection(c.Name)
		return nil
	}
	err := ReadMongoDBArchive(in, func(c MongoDBCollection) error {
		current = nil
		if c.Type == "view" {
			views = append(views, c)
			return nil
		}
		return create(c)
	}, func(raw bson.Raw) error {
		if current == nil {
			return fmt.Errorf("MongoDB recovery has no target collection")
		}
		if len(batch) == 32 || batchBytes+len(raw) > 8<<20 {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, bson.Raw(bytes.Clone(raw)))
		batchBytes += len(raw)
		return nil
	}, func(c MongoDBCollection) error {
		if err := flush(); err != nil {
			return err
		}
		if c.Type == "view" {
			return nil
		}
		indexes := bson.A{}
		for _, raw := range c.Indexes {
			index, err := mongoDBIndexOptions(raw)
			if err != nil {
				return err
			}
			indexes = append(indexes, index)
		}
		if len(indexes) != 0 {
			if err := db.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: c.Name}, {Key: "indexes", Value: indexes}, {Key: "writeConcern", Value: bson.D{{Key: "w", Value: "majority"}}}}).Err(); err != nil {
				return fmt.Errorf("MongoDB rejected recovered indexes")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range views {
		if err := create(c); err != nil {
			return err
		}
	}
	return nil
}

func mongoDBCollectionOptions(c MongoDBCollection) (bson.D, error) {
	allowed := "capped size max validator validationLevel validationAction collation clusteredIndex changeStreamPreAndPostImages timeseries expireAfterSeconds"
	if c.Type == "view" {
		allowed = "viewOn pipeline collation"
	}
	opts, err := mongoDBAllowedOptions(c.Options, allowed, "")
	if err != nil {
		return nil, err
	}
	if c.Type == "view" {
		base, ok := c.Options.Lookup("viewOn").StringValueOK()
		if !ok || !validMongoDBCollectionName(base) {
			return nil, fmt.Errorf("MongoDB view source is invalid")
		}
		if _, ok := c.Options.Lookup("pipeline").ArrayOK(); !ok {
			return nil, fmt.Errorf("MongoDB view pipeline is invalid")
		}
	}
	return opts, nil
}

func mongoDBIndexOptions(raw bson.Raw) (bson.D, error) {
	if name, ok := raw.Lookup("name").StringValueOK(); !ok || name == "" || len(name) > 128 {
		return nil, fmt.Errorf("MongoDB index name is invalid")
	}
	if _, ok := raw.Lookup("key").DocumentOK(); !ok {
		return nil, fmt.Errorf("MongoDB index key is invalid")
	}
	return mongoDBAllowedOptions(raw, "key name unique sparse expireAfterSeconds partialFilterExpression collation wildcardProjection hidden weights default_language language_override textIndexVersion 2dsphereIndexVersion bits min max bucketSize clustered", "v ns background")
}

func mongoDBAllowedOptions(raw bson.Raw, allowed, ignored string) (bson.D, error) {
	var fields bson.D
	if raw.Validate() != nil || bson.Unmarshal(raw, &fields) != nil {
		return nil, fmt.Errorf("MongoDB schema options are invalid")
	}
	seen := map[string]bool{}
	out := bson.D{}
	for _, field := range fields {
		if seen[field.Key] {
			return nil, fmt.Errorf("MongoDB schema options contain duplicate keys")
		}
		seen[field.Key] = true
		if slices.Contains(strings.Fields(ignored), field.Key) {
			continue
		}
		if !slices.Contains(strings.Fields(allowed), field.Key) {
			return nil, fmt.Errorf("MongoDB schema option is not supported by recovery")
		}
		out = append(out, field)
	}
	return out, nil
}

func validMongoDBCollectionName(name string) bool {
	return name != "" && len(name) <= 120 && !strings.ContainsAny(name, "\x00$") && !strings.HasPrefix(name, "system.")
}
